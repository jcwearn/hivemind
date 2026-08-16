// Package lobby holds the rooms: who is in them, what they voted for, and the
// goroutine that turns those votes into game ticks.
//
// The concurrency model is one goroutine per room, and it is the whole point of
// this package. A Room's state -- the game, the players, the subscriber set --
// is owned by run() and touched by nothing else. Every mutation arrives as a
// command on a channel and is applied in that one goroutine, so none of it
// needs a mutex, and no caller can observe a half-updated room.
//
// The only lock in the package is Registry's, and it guards a map of rooms
// rather than anything inside one.
package lobby

import (
	"errors"
	"log/slog"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/jcwearn/hivemind/internal/game"
)

// ErrRoomClosed is returned by every call that reaches a room whose goroutine
// has already exited -- an idle room that was collected, or a server shutting
// down. Callers turn it into a 404, because from a player's point of view a
// room that has gone away and a room that never existed are the same thing.
var ErrRoomClosed = errors.New("lobby: room closed")

// PlayerID identifies a phone across reconnects. It lives in a signed cookie,
// so it survives a locked screen but means nothing to anybody else.
type PlayerID string

// idleTimeout is how long a room with nobody watching survives before its
// goroutine exits and the registry drops it.
const idleTimeout = 10 * time.Minute

// Per-room capacity.
//
// maxPlayers is a party-game number rather than a technical one -- a roster of
// two dozen is already unreadable across a room -- but it also bounds the cost
// of a join, which re-renders a roster that just grew by one. Without it, N
// joins cost O(N^2) rendered bytes, and a cookie-less client is a brand new
// player on every request.
//
// maxSubscribers bounds one room's share of the process-wide stream budget, so
// a single room cannot starve the other ninety-nine.
const (
	maxPlayers     = 24
	maxSubscribers = 40
)

// streamBudget is the process-wide subscriber accounting a room defers to.
// *Registry implements it; tests substitute their own.
type streamBudget interface {
	acquireStream() bool
	releaseStream()
}

// subscriber is one open SSE stream. The channel is buffered so a phone that
// has stopped reading cannot stall the room goroutine -- see broadcast.
type subscriber struct {
	ch chan []byte
}

// StreamKind distinguishes the two fan-outs a room maintains: the shared screen
// and the phones. Both get a full snapshot every tick, which is what makes
// dropping a frame safe.
type StreamKind uint8

// The two fan-outs. ScreenStream feeds the television; PlayStream feeds every
// phone, with identical bytes.
const (
	ScreenStream StreamKind = iota
	PlayStream
)

// Player is one phone in a room.
type Player struct {
	ID   PlayerID
	Name string
	Seat int // stable join order, used for colour and roster ordering
	Vote game.Direction
}

// Snapshot is the render input for one tick: everything a template needs, and
// nothing that would let it reach back into the room.
type Snapshot struct {
	Code    string
	State   game.State
	Players []Player
	Tally   map[game.Direction]int
	Voters  int
}

// Frames are the two rendered fan-outs for one snapshot. Both are produced by a
// single Render call so that a tick costs one render regardless of how many
// people are connected.
type Frames struct {
	Screen []byte
	Play   []byte
}

// RenderFunc turns a snapshot into the bytes every subscriber receives. It is a
// plain function rather than an interface because there is exactly one thing to
// do with a snapshot, and injecting it is only here to keep this package from
// importing the templates.
type RenderFunc func(Snapshot) Frames

// Options configures a room. Only Render is required.
type Options struct {
	Logger *slog.Logger
	Render RenderFunc
	Config game.Config
	Seed   [32]byte
}

// Room is a single game lobby. Every exported method is safe to call from any
// goroutine; each one sends a command and, where it needs an answer, waits for
// the room goroutine to send it back.
type Room struct {
	Code string

	cmds chan command
	// done is closed when run() returns. Senders select on it so a call into a
	// dead room fails immediately instead of blocking forever.
	done chan struct{}
	// quit asks run() to stop. Separate from done: one is the request, the
	// other is the acknowledgement.
	quit chan struct{}

	log     *slog.Logger
	render  RenderFunc
	onClose func(code string)
	// budget is the process-wide stream accounting, set by Registry.Create. Nil
	// in tests that build a room directly, which then have only the per-room
	// ceiling.
	budget streamBudget

	// Everything below is owned by run() and must not be touched from any other
	// goroutine. No mutex guards them because none is needed.
	state    game.State
	players  map[PlayerID]*Player
	streams  map[StreamKind]map[*subscriber]struct{}
	rng      *rand.Rand
	nextSeat int
	lastSeen time.Time
	dropped  int
}

// command is one mutation, applied on the room goroutine. Keeping apply on the
// command rather than in a type switch means adding a command cannot forget to
// handle a case.
type command interface {
	apply(*Room)
}

func newRoom(code string, opts Options, onClose func(string)) *Room {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	cfg := opts.Config
	if cfg.Width == 0 {
		cfg = game.DefaultConfig()
	}

	rng := rand.New(rand.NewChaCha8(opts.Seed))

	r := &Room{
		Code:     code,
		cmds:     make(chan command),
		done:     make(chan struct{}),
		quit:     make(chan struct{}),
		log:      log.With("room", code),
		render:   opts.Render,
		onClose:  onClose,
		state:    game.New(cfg, rng),
		players:  make(map[PlayerID]*Player),
		rng:      rng,
		lastSeen: time.Now(),
	}
	r.streams = map[StreamKind]map[*subscriber]struct{}{
		ScreenStream: {},
		PlayStream:   {},
	}
	return r
}

// run is the room. It owns every field the commands touch, and it is the only
// goroutine that ever does.
func (r *Room) run() {
	defer close(r.done)
	if r.onClose != nil {
		defer r.onClose(r.Code)
	}
	// Hand back whatever this room still holds. A collected room's SSE handlers
	// return when they see Done, and their deferred cancel then finds nothing
	// to unsubscribe -- so without this the budget would leak by exactly the
	// number of people who were watching when the room went away.
	defer r.releaseAllStreams()

	interval := game.TickInterval(r.state.Score)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	idle := time.NewTicker(time.Minute)
	defer idle.Stop()

	for {
		select {
		case <-r.quit:
			return

		case c := <-r.cmds:
			c.apply(r)

		case <-ticker.C:
			r.step()
			// The ramp is the difficulty curve, so the ticker has to follow the
			// score rather than being set once at construction.
			if want := game.TickInterval(r.state.Score); want != interval {
				interval = want
				ticker.Reset(interval)
			}

		case <-idle.C:
			if r.subscriberCount() == 0 && time.Since(r.lastSeen) > idleTimeout {
				r.log.Info("room idle, closing", "idle_for", time.Since(r.lastSeen).Round(time.Second))
				return
			}
		}
	}
}

// step advances the game one tick and pushes the result to everyone. It is
// called only from run.
func (r *Room) step() {
	// Lobby and game over are inert: nothing advances, and every command that
	// changes what they look like already broadcasts for itself. Ticking a
	// frame out anyway was pure waste -- a room sitting on its lobby screen
	// rendered two full HTML fragments 2.5 times a second for as long as it
	// existed.
	if r.state.Phase != game.PhasePlaying && r.state.Phase != game.PhaseCountdown {
		return
	}

	dir := game.Tally(r.votes(), r.state.Dir)
	r.state = game.Step(r.state, dir, r.rng)
	r.broadcast()
}

// votes collects the current standing intention of every player.
//
// Votes persist until changed rather than clearing each tick. At up to eight
// ticks a second the alternative is a game about tapping quickly, which is a
// worse party game and unkind to whoever has the oldest phone.
func (r *Room) votes() map[string]game.Direction {
	out := make(map[string]game.Direction, len(r.players))
	for id, p := range r.players {
		out[string(id)] = p.Vote
	}
	return out
}

// snapshot builds the render input. Players are sorted by seat so the roster
// does not shuffle between frames.
func (r *Room) snapshot() Snapshot {
	players := make([]Player, 0, len(r.players))
	for _, p := range r.players {
		players = append(players, *p)
	}
	sort.Slice(players, func(i, j int) bool { return players[i].Seat < players[j].Seat })

	tally := make(map[game.Direction]int, 4)
	voters := 0
	banned := r.state.Dir.Opposite()
	for _, p := range players {
		if p.Vote == game.DirNone || p.Vote == banned {
			continue
		}
		tally[p.Vote]++
		voters++
	}

	return Snapshot{
		Code:    r.Code,
		State:   r.state,
		Players: players,
		Tally:   tally,
		Voters:  voters,
	}
}

// broadcast renders once and hands the same bytes to every subscriber.
//
// This is the reason a tick costs the same with twenty phones connected as with
// one, and the select/default is the reason one slow phone cannot slow the game
// down for everybody else. Dropping is safe because every frame is a complete
// snapshot rather than a delta: a subscriber that misses one is fully repaired
// by the next, roughly 300ms later.
func (r *Room) broadcast() {
	if r.render == nil {
		return
	}

	// Nobody is watching, so there is nothing to render FOR. Checked before
	// r.render rather than inside the fan-out loop, which is the whole point:
	// rendering first and then discovering the subscriber set was empty is how
	// an abandoned room burned ~12 KB of HTML 2.5 times a second for its full
	// ten-minute lifetime, and it is what made room flooding cheap.
	if r.subscriberCount() == 0 {
		return
	}

	frames := r.render(r.snapshot())

	for kind, subs := range r.streams {
		frame := frames.Screen
		if kind == PlayStream {
			frame = frames.Play
		}
		for s := range subs {
			select {
			case s.ch <- frame:
			default:
				r.dropped++
			}
		}
	}
}

// releaseAllStreams returns every still-attached subscriber's budget unit. Runs
// on the room goroutine as it exits, so it does not race the maps.
func (r *Room) releaseAllStreams() {
	if r.budget == nil {
		return
	}
	for _, subs := range r.streams {
		for sub := range subs {
			delete(subs, sub)
			r.budget.releaseStream()
		}
	}
}

func (r *Room) subscriberCount() int {
	n := 0
	for _, subs := range r.streams {
		n += len(subs)
	}
	return n
}

// send delivers a command to the room goroutine, or reports that it has gone.
func (r *Room) send(c command) error {
	select {
	case r.cmds <- c:
		return nil
	case <-r.done:
		return ErrRoomClosed
	}
}

// close asks the room goroutine to stop and waits for it to acknowledge.
// Calling it twice is safe; the second call returns once done is already shut.
func (r *Room) close() {
	select {
	case r.quit <- struct{}{}:
		<-r.done
	case <-r.done:
	}
}

// Dropped reports how many frames have been discarded because a subscriber was
// not keeping up. Exposed for the health endpoint: a number that climbs steadily
// means somebody's connection is worse than the game can hide.
func (r *Room) Dropped() (int, error) {
	reply := make(chan int, 1)
	if err := r.send(statsCmd{reply: reply}); err != nil {
		return 0, err
	}
	return <-reply, nil
}

// --- commands ---

// joinResult distinguishes "seated" from "the room is full", which the caller
// has to tell apart to show the right page.
type joinResult struct {
	player Player
	seated bool
}

type joinCmd struct {
	id    PlayerID
	name  string
	reply chan joinResult
}

func (c joinCmd) apply(r *Room) {
	r.lastSeen = time.Now()

	// A returning phone keeps its seat. This is the whole reason PlayerID is a
	// signed cookie rather than a per-connection value: locking a screen mid
	// round should not cost somebody their place.
	if p, ok := r.players[c.id]; ok {
		renamed := c.name != "" && c.name != p.Name
		if renamed {
			p.Name = c.name
		}
		c.reply <- joinResult{player: *p, seated: true}
		// Only when something actually changed. Rejoining is common -- every
		// reload of the controller does it -- and a broadcast per reload would
		// be a frame nobody needs.
		if renamed {
			r.broadcast()
		}
		return
	}

	// Checked only on the new-seat path, so a returning player is never locked
	// out of a room they are already in.
	if len(r.players) >= maxPlayers {
		c.reply <- joinResult{}
		return
	}

	p := &Player{ID: c.id, Name: c.name, Seat: r.nextSeat}
	r.nextSeat++
	r.players[c.id] = p
	r.log.Info("player joined", "player", c.id, "name", c.name, "seat", p.Seat)
	c.reply <- joinResult{player: *p, seated: true}
	r.broadcast()
}

// Join adds a player, or returns the existing one if this phone has been here
// before. It reports false when the room is full, which is a normal outcome
// rather than an error.
func (r *Room) Join(id PlayerID, name string) (Player, bool, error) {
	reply := make(chan joinResult, 1)
	if err := r.send(joinCmd{id: id, name: name, reply: reply}); err != nil {
		return Player{}, false, err
	}
	res := <-reply
	return res.player, res.seated, nil
}

// renameResult carries whether the player was in the room at all, which is how
// a stale cookie from a collected room surfaces to the caller.
type renameResult struct {
	player Player
	member bool
}

type renameCmd struct {
	id    PlayerID
	name  string
	reply chan renameResult
}

func (c renameCmd) apply(r *Room) {
	p, ok := r.players[c.id]
	if !ok {
		c.reply <- renameResult{}
		return
	}

	r.lastSeen = time.Now()
	if c.name != "" && c.name != p.Name {
		p.Name = c.name
		r.log.Info("player renamed", "player", c.id, "name", c.name)
		// Broadcast, because the roster on the shared screen is the only place
		// this is visible to anybody but the person who typed it. joinCmd's
		// rename path deliberately does the same.
		r.broadcast()
	}
	c.reply <- renameResult{player: *p, member: true}
}

// Rename changes a player's display name. It reports false if this phone is not
// in the room, and never creates a seat -- that is Join's job, and conflating
// the two would let a stale cookie silently reappear in the roster.
func (r *Room) Rename(id PlayerID, name string) (Player, bool, error) {
	reply := make(chan renameResult, 1)
	if err := r.send(renameCmd{id: id, name: name, reply: reply}); err != nil {
		return Player{}, false, err
	}
	res := <-reply
	return res.player, res.member, nil
}

type leaveCmd struct{ id PlayerID }

func (c leaveCmd) apply(r *Room) {
	if _, ok := r.players[c.id]; !ok {
		return
	}
	delete(r.players, c.id)
	r.lastSeen = time.Now()
	r.log.Info("player left", "player", c.id)
	r.broadcast()
}

// Leave removes a player from the roster.
func (r *Room) Leave(id PlayerID) error { return r.send(leaveCmd{id: id}) }

type voteCmd struct {
	id    PlayerID
	dir   game.Direction
	reply chan bool
}

func (c voteCmd) apply(r *Room) {
	r.lastSeen = time.Now()
	p, ok := r.players[c.id]
	if !ok {
		c.reply <- false
		return
	}
	p.Vote = c.dir
	c.reply <- true

	// While a round is running the tick is already pushing a frame every few
	// hundred milliseconds, and a broadcast per vote on top of that would make a
	// room of twenty people quadratically noisy for no visible gain.
	//
	// Between rounds there is no tick, so this is the only thing that moves the
	// tally on the shared screen -- and people absolutely do fiddle with the
	// buttons while waiting for the host to press start.
	if r.state.Phase != game.PhasePlaying && r.state.Phase != game.PhaseCountdown {
		r.broadcast()
	}
}

// Vote records a player's standing intention. It reports false if the player is
// not in this room, which is how a stale cookie from a collected room surfaces.
//
// Deliberately no broadcast here: votes arrive far more often than ticks, and
// the tally is already on the next frame at most 400ms away. Pushing a frame per
// keystroke would make a room of twenty people quadratically noisy for no
// visible gain.
func (r *Room) Vote(id PlayerID, dir game.Direction) (bool, error) {
	reply := make(chan bool, 1)
	if err := r.send(voteCmd{id: id, dir: dir, reply: reply}); err != nil {
		return false, err
	}
	return <-reply, nil
}

type startCmd struct{}

func (c startCmd) apply(r *Room) {
	r.lastSeen = time.Now()
	before := r.state.Phase
	r.state = game.Start(r.state, r.rng)
	if r.state.Phase != before {
		r.log.Info("round started", "players", len(r.players))
	}
	r.broadcast()
}

// Start begins a round. It is idempotent mid-round, so a host mashing the
// button cannot reset the score.
func (r *Room) Start() error { return r.send(startCmd{}) }

// subscribeResult reports whether the stream was accepted, and why not.
type subscribeResult struct {
	initial  []byte
	accepted bool
}

type subscribeCmd struct {
	kind  StreamKind
	sub   *subscriber
	reply chan subscribeResult
}

func (c subscribeCmd) apply(r *Room) {
	r.lastSeen = time.Now()

	if r.subscriberCount() >= maxSubscribers {
		c.reply <- subscribeResult{}
		return
	}
	// The process-wide budget is claimed on the room goroutine so the count and
	// the map insert cannot disagree. Released by unsubscribeCmd, and by the
	// run loop's exit path for anything still attached when a room closes.
	if r.budget != nil && !r.budget.acquireStream() {
		c.reply <- subscribeResult{}
		return
	}

	r.streams[c.kind][c.sub] = struct{}{}

	// The initial frame goes back on the reply channel rather than through the
	// subscriber's buffer, so a new screen paints immediately instead of
	// showing nothing until the next tick.
	frames := Frames{}
	if r.render != nil {
		frames = r.render(r.snapshot())
	}
	if c.kind == PlayStream {
		c.reply <- subscribeResult{initial: frames.Play, accepted: true}
	} else {
		c.reply <- subscribeResult{initial: frames.Screen, accepted: true}
	}
}

type unsubscribeCmd struct {
	kind StreamKind
	sub  *subscriber
}

func (c unsubscribeCmd) apply(r *Room) {
	// Only release the budget if this subscriber was actually counted. cancel()
	// is called from a defer and can run after a room has already torn down its
	// subscribers, and double-releasing would hand out capacity that is still
	// in use.
	if _, ok := r.streams[c.kind][c.sub]; !ok {
		return
	}
	delete(r.streams[c.kind], c.sub)
	if r.budget != nil {
		r.budget.releaseStream()
	}
	r.lastSeen = time.Now()
}

type statsCmd struct{ reply chan int }

func (c statsCmd) apply(r *Room) { c.reply <- r.dropped }

type snapshotCmd struct{ reply chan Snapshot }

func (c snapshotCmd) apply(r *Room) { c.reply <- r.snapshot() }

// Snapshot returns the room's current state. Used by the handlers that render a
// full page rather than a stream frame.
func (r *Room) Snapshot() (Snapshot, error) {
	reply := make(chan Snapshot, 1)
	if err := r.send(snapshotCmd{reply: reply}); err != nil {
		return Snapshot{}, err
	}
	return <-reply, nil
}
