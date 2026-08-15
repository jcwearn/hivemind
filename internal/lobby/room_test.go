package lobby

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jcwearn/hivemind/internal/game"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// testRender is enough to tell the two fan-outs apart without pulling the
// templates into this package's tests.
func testRender(s Snapshot) Frames {
	return Frames{
		Screen: fmt.Appendf(nil, "screen:%s:%d", s.Code, s.State.Score),
		Play:   fmt.Appendf(nil, "play:%s:%d", s.Code, s.State.Score),
	}
}

func testOptions() Options {
	return Options{Logger: discardLogger(), Render: testRender}
}

// idleRoom builds a room WITHOUT starting its goroutine, so commands can be
// applied synchronously and the state machine tested without any timing at all.
// This is the reason apply lives on the command rather than behind the channel.
func idleRoom(t *testing.T) *Room {
	t.Helper()
	return newRoom("TEST", testOptions(), nil)
}

// liveRoom starts a real room goroutine and stops it when the test ends.
func liveRoom(t *testing.T) *Room {
	t.Helper()
	r := newRoom("LIVE", testOptions(), nil)
	go r.run()
	t.Cleanup(r.close)
	return r
}

func TestJoinAssignsSeatsInOrder(t *testing.T) {
	r := idleRoom(t)

	for i, name := range []string{"ana", "sam", "jax"} {
		reply := make(chan Player, 1)
		joinCmd{id: PlayerID(name), name: name, reply: reply}.apply(r)
		p := <-reply
		if p.Seat != i {
			t.Errorf("%s seat = %d, want %d", name, p.Seat, i)
		}
	}

	if len(r.players) != 3 {
		t.Errorf("players = %d, want 3", len(r.players))
	}
}

// A phone that locks its screen and comes back must land in the same seat. This
// is the entire justification for the signed cookie, so it is asserted rather
// than assumed.
func TestJoinTwiceKeepsSeat(t *testing.T) {
	r := idleRoom(t)

	first := make(chan Player, 1)
	joinCmd{id: "ana", name: "ana", reply: first}.apply(r)
	joinCmd{id: "sam", name: "sam", reply: make(chan Player, 1)}.apply(r)

	again := make(chan Player, 1)
	joinCmd{id: "ana", name: "ana", reply: again}.apply(r)

	was, now := <-first, <-again
	if now.Seat != was.Seat {
		t.Errorf("seat changed on reconnect: %d, want %d", now.Seat, was.Seat)
	}
	if len(r.players) != 2 {
		t.Errorf("players = %d, want 2 -- reconnect must not add a seat", len(r.players))
	}
}

func TestJoinTwiceUpdatesName(t *testing.T) {
	r := idleRoom(t)

	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)

	reply := make(chan Player, 1)
	joinCmd{id: "ana", name: "anastasia", reply: reply}.apply(r)

	if got := (<-reply).Name; got != "anastasia" {
		t.Errorf("Name = %q, want %q", got, "anastasia")
	}
}

func TestVoteRequiresMembership(t *testing.T) {
	r := idleRoom(t)
	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)

	ok := make(chan bool, 1)
	voteCmd{id: "ana", dir: game.DirUp, reply: ok}.apply(r)
	if !<-ok {
		t.Error("Vote by a member was rejected")
	}
	if got := r.players["ana"].Vote; got != game.DirUp {
		t.Errorf("Vote = %v, want up", got)
	}

	stranger := make(chan bool, 1)
	voteCmd{id: "nobody", dir: game.DirUp, reply: stranger}.apply(r)
	if <-stranger {
		t.Error("Vote by a non-member was accepted")
	}
}

// Votes persist across ticks rather than clearing, so a player holds a
// direction instead of having to tap eight times a second.
func TestVotesPersistAcrossTicks(t *testing.T) {
	r := idleRoom(t)
	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)
	voteCmd{id: "ana", dir: game.DirUp, reply: make(chan bool, 1)}.apply(r)
	startCmd{}.apply(r)

	for range 6 {
		r.step()
	}

	if got := r.players["ana"].Vote; got != game.DirUp {
		t.Errorf("Vote = %v after six ticks, want up -- votes must persist", got)
	}
	if r.state.Dir != game.DirUp {
		t.Errorf("heading = %v, want up -- the held vote should have steered", r.state.Dir)
	}
}

func TestStartIsIdempotentMidRound(t *testing.T) {
	r := idleRoom(t)
	startCmd{}.apply(r)
	for range 5 {
		r.step()
	}
	r.state.Score = 9

	startCmd{}.apply(r)

	if r.state.Score != 9 {
		t.Errorf("Score = %d, want 9 -- a second Start must not reset a running round", r.state.Score)
	}
}

func TestLeaveRemovesPlayer(t *testing.T) {
	r := idleRoom(t)
	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)

	leaveCmd{id: "ana"}.apply(r)

	if len(r.players) != 0 {
		t.Errorf("players = %d, want 0", len(r.players))
	}
	// Leaving twice must not panic or resurrect anyone.
	leaveCmd{id: "ana"}.apply(r)
}

func TestSnapshotSortsBySeatAndExcludesBlockedVotes(t *testing.T) {
	r := idleRoom(t)
	for _, name := range []string{"ana", "sam", "jax"} {
		joinCmd{id: PlayerID(name), name: name, reply: make(chan Player, 1)}.apply(r)
	}

	r.state.Dir = game.DirRight
	voteCmd{id: "ana", dir: game.DirUp, reply: make(chan bool, 1)}.apply(r)
	voteCmd{id: "sam", dir: game.DirLeft, reply: make(chan bool, 1)}.apply(r) // reverses into the neck
	voteCmd{id: "jax", dir: game.DirUp, reply: make(chan bool, 1)}.apply(r)

	snap := r.snapshot()

	for i, p := range snap.Players {
		if p.Seat != i {
			t.Errorf("players[%d].Seat = %d, want %d -- roster must be seat-ordered", i, p.Seat, i)
		}
	}
	if snap.Tally[game.DirUp] != 2 {
		t.Errorf("Tally[up] = %d, want 2", snap.Tally[game.DirUp])
	}
	if snap.Tally[game.DirLeft] != 0 {
		t.Errorf("Tally[left] = %d, want 0 -- a reversal is not a countable vote", snap.Tally[game.DirLeft])
	}
	if snap.Voters != 2 {
		t.Errorf("Voters = %d, want 2", snap.Voters)
	}
}

// The central claim of this package: a subscriber that stops reading degrades
// its own frame rate and nobody else's. If this test hangs, the actor model is
// broken and every other guarantee here is worthless.
func TestSlowSubscriberDoesNotBlockTheRoom(t *testing.T) {
	r := liveRoom(t)

	_, slow, cancelSlow, err := r.Stream(ScreenStream)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer cancelSlow()

	_, fast, cancelFast, err := r.Stream(ScreenStream)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer cancelFast()

	// Never read from slow. Each Join broadcasts, so this overruns its 4-frame
	// buffer several times over.
	drained := make(chan int, 1)
	go func() {
		n := 0
		deadline := time.After(3 * time.Second)
		for {
			select {
			case <-fast:
				n++
			case <-deadline:
				drained <- n
				return
			}
		}
	}()

	for i := range 20 {
		done := make(chan Player, 1)
		if err := r.send(joinCmd{id: PlayerID(fmt.Sprint(i)), name: "p", reply: done}); err != nil {
			t.Fatalf("join %d: %v", i, err)
		}
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("room goroutine blocked on join %d -- a slow subscriber stalled it", i)
		}
	}

	// The room must still answer promptly with the slow stream still wedged.
	answered := make(chan struct{})
	go func() {
		defer close(answered)
		if _, err := r.Snapshot(); err != nil {
			t.Errorf("Snapshot: %v", err)
		}
	}()
	select {
	case <-answered:
	case <-time.After(2 * time.Second):
		t.Fatal("room goroutine did not answer Snapshot -- it is blocked")
	}

	if got := <-drained; got == 0 {
		t.Error("the reading subscriber received nothing")
	}

	if dropped, err := r.Dropped(); err != nil {
		t.Fatalf("Dropped: %v", err)
	} else if dropped == 0 {
		t.Error("expected frames to be dropped for the subscriber that stopped reading")
	}

	if len(slow) != cap(slow) {
		t.Errorf("slow buffer = %d/%d, want full", len(slow), cap(slow))
	}
}

func TestStreamDeliversInitialFrameImmediately(t *testing.T) {
	r := liveRoom(t)

	initial, _, cancel, err := r.Stream(ScreenStream)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer cancel()

	if want := "screen:LIVE:0"; string(initial) != want {
		t.Errorf("initial = %q, want %q", initial, want)
	}

	play, _, cancelPlay, err := r.Stream(PlayStream)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer cancelPlay()

	if want := "play:LIVE:0"; string(play) != want {
		t.Errorf("initial play frame = %q, want %q", play, want)
	}
}

func TestCancelUnsubscribes(t *testing.T) {
	r := liveRoom(t)

	_, _, cancel, err := r.Stream(ScreenStream)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	cancel()

	// Reach into the room the only legal way: a command.
	counted := make(chan int, 1)
	if err := r.send(countStreamsCmd{reply: counted}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got := <-counted; got != 0 {
		t.Errorf("subscribers = %d after cancel, want 0", got)
	}
}

func TestCallsIntoAClosedRoomFail(t *testing.T) {
	r := newRoom("GONE", testOptions(), nil)
	go r.run()
	r.close()

	if _, err := r.Join("ana", "ana"); !errors.Is(err, ErrRoomClosed) {
		t.Errorf("Join error = %v, want ErrRoomClosed", err)
	}
	if _, err := r.Snapshot(); !errors.Is(err, ErrRoomClosed) {
		t.Errorf("Snapshot error = %v, want ErrRoomClosed", err)
	}
	if err := r.Start(); !errors.Is(err, ErrRoomClosed) {
		t.Errorf("Start error = %v, want ErrRoomClosed", err)
	}

	select {
	case <-r.Done():
	default:
		t.Error("Done is not closed after close()")
	}

	// Closing twice must not panic or hang.
	r.close()
}

// countStreamsCmd exists only for the tests, which is why it lives here rather
// than in room.go: it is the honest way to read owned state from outside the
// room goroutine, and defining it in the test file keeps it out of the API.
type countStreamsCmd struct{ reply chan int }

func (c countStreamsCmd) apply(r *Room) { c.reply <- r.subscriberCount() }

func TestRenameChangesTheNameAndBroadcasts(t *testing.T) {
	r := idleRoom(t)
	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)

	// A subscriber is the only way to observe that the roster was re-sent, and
	// the roster is the only place a rename is visible to anybody else.
	sub := &subscriber{ch: make(chan []byte, 4)}
	subscribeCmd{kind: ScreenStream, sub: sub, reply: make(chan []byte, 1)}.apply(r)
	drain(sub.ch)

	reply := make(chan renameResult, 1)
	renameCmd{id: "ana", name: "anastasia", reply: reply}.apply(r)

	res := <-reply
	if !res.member {
		t.Fatal("Rename reported the player was not a member")
	}
	if res.player.Name != "anastasia" {
		t.Errorf("Name = %q, want %q", res.player.Name, "anastasia")
	}
	if got := r.players["ana"].Name; got != "anastasia" {
		t.Errorf("stored name = %q, want %q", got, "anastasia")
	}
	if len(sub.ch) == 0 {
		t.Error("a rename did not broadcast -- the shared screen would keep the old name")
	}
}

func TestRenameKeepsTheSeat(t *testing.T) {
	r := idleRoom(t)
	first := make(chan Player, 1)
	joinCmd{id: "ana", name: "ana", reply: first}.apply(r)
	joinCmd{id: "sam", name: "sam", reply: make(chan Player, 1)}.apply(r)
	was := <-first

	reply := make(chan renameResult, 1)
	renameCmd{id: "ana", name: "anastasia", reply: reply}.apply(r)

	if got := (<-reply).player.Seat; got != was.Seat {
		t.Errorf("seat changed on rename: %d, want %d", got, was.Seat)
	}
	if len(r.players) != 2 {
		t.Errorf("players = %d, want 2 -- rename must not add a seat", len(r.players))
	}
}

// A rename must never create a player. Conflating it with Join would let a
// stale cookie from a collected room quietly reappear in the roster.
func TestRenameDoesNotCreateAPlayer(t *testing.T) {
	r := idleRoom(t)

	reply := make(chan renameResult, 1)
	renameCmd{id: "stranger", name: "nobody", reply: reply}.apply(r)

	if (<-reply).member {
		t.Error("Rename reported membership for a player that was never in the room")
	}
	if len(r.players) != 0 {
		t.Errorf("players = %d, want 0 -- rename created a seat", len(r.players))
	}
}

func TestRenameToTheSameNameDoesNotBroadcast(t *testing.T) {
	r := idleRoom(t)
	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)

	sub := &subscriber{ch: make(chan []byte, 4)}
	subscribeCmd{kind: ScreenStream, sub: sub, reply: make(chan []byte, 1)}.apply(r)
	drain(sub.ch)

	renameCmd{id: "ana", name: "ana", reply: make(chan renameResult, 1)}.apply(r)

	if len(sub.ch) != 0 {
		t.Error("a no-op rename broadcast a frame nobody needed")
	}
}

// Rejoining is what every reload of the controller does, so it must not cost a
// frame unless something actually changed.
func TestRejoinBroadcastsOnlyWhenTheNameChanges(t *testing.T) {
	r := idleRoom(t)
	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)

	sub := &subscriber{ch: make(chan []byte, 4)}
	subscribeCmd{kind: ScreenStream, sub: sub, reply: make(chan []byte, 1)}.apply(r)
	drain(sub.ch)

	joinCmd{id: "ana", name: "ana", reply: make(chan Player, 1)}.apply(r)
	if len(sub.ch) != 0 {
		t.Error("an unchanged rejoin broadcast a frame")
	}

	joinCmd{id: "ana", name: "anastasia", reply: make(chan Player, 1)}.apply(r)
	if len(sub.ch) == 0 {
		t.Error("a rejoin under a new name did not broadcast")
	}
}

// drain empties a subscriber's buffer so a test can assert on what arrives next.
func drain(ch chan []byte) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}
