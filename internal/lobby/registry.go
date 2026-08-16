package lobby

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
)

// codeAlphabet omits I, L, O, U, 0 and 1. The first four are read wrong off a
// TV across a room, and the last two are typed wrong on a phone keyboard. What
// is left is 30 symbols, so a four character code is 810,000 rooms -- more than
// enough for a game whose rooms are collected ten minutes after everybody
// leaves.
const codeAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789"

const codeLength = 4

// ErrNoCode is returned when the registry cannot find an unused room code. It
// means something is badly wrong -- 810,000 codes are not exhausted by a party
// game -- so callers should treat it as a 500 rather than retrying.
var ErrNoCode = errors.New("lobby: could not allocate a room code")

// ErrTooManyRooms is returned when the registry is at capacity. Callers should
// turn it into a 503 and say so: it is a temporary condition, not a fault.
var ErrTooManyRooms = errors.New("lobby: too many rooms")

// ErrTooManyStreams is returned when the process-wide subscriber budget is
// exhausted. Also a 503.
var ErrTooManyStreams = errors.New("lobby: too many streams")

// Capacity. These exist because anyone on the internet can create a room, and
// nothing else stops them.
//
// Rooms are cheap once an empty one stops rendering (see Room.broadcast), but
// they are not free: each is a goroutine, two timers and a handful of maps, and
// each survives at least idleTimeout before it can be collected. Without a
// ceiling, creation rate times idleTimeout is the steady-state room count, and
// a script reaches an unrecoverable number in well under a minute.
//
// maxStreams is the process-wide budget rather than a per-room one, because
// per-room limits multiply: 100 rooms of 40 subscribers would be 4,000 open
// connections at roughly 15-30 KB each. This is the number that actually bounds
// memory.
const (
	maxRooms   = 100
	maxStreams = 500
)

// Registry owns the set of live rooms.
//
// Its mutex is the only lock in this package, and it guards the map alone.
// Nothing inside a Room is reachable through it: callers get a *Room and then
// talk to that room's goroutine over channels.
type Registry struct {
	mu    sync.RWMutex
	rooms map[string]*Room
	// streams is the process-wide count of open subscribers. It lives here
	// rather than in Room because the budget is global -- a room cannot know
	// what the other ninety-nine are using.
	streams int

	log    *slog.Logger
	render RenderFunc
	opts   Options
}

// NewRegistry returns an empty registry. The options are the template every
// room is built from; Seed is filled in per room.
func NewRegistry(opts Options) *Registry {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Registry{
		rooms:  make(map[string]*Room),
		log:    log,
		render: opts.Render,
		opts:   opts,
	}
}

// Create allocates a room with an unused code and starts its goroutine.
func (reg *Registry) Create() (*Room, error) {
	var seed [32]byte
	if _, err := rand.Read(seed[:]); err != nil {
		return nil, fmt.Errorf("lobby: seed room: %w", err)
	}

	opts := reg.opts
	opts.Seed = seed

	reg.mu.Lock()
	// Checked under the same lock that inserts, so concurrent creates cannot
	// both see room for one more.
	if len(reg.rooms) >= maxRooms {
		reg.mu.Unlock()
		return nil, ErrTooManyRooms
	}
	code, err := reg.freeCodeLocked()
	if err != nil {
		reg.mu.Unlock()
		return nil, err
	}
	room := newRoom(code, opts, reg.remove)
	room.budget = reg
	reg.rooms[code] = room
	count := len(reg.rooms)
	reg.mu.Unlock()

	go room.run()

	reg.log.Info("room created", "room", code, "rooms", count)
	return room, nil
}

// freeCodeLocked picks a code nothing is using. The caller must hold the write
// lock, which is what makes "check then insert" atomic -- generating the code
// outside the lock would let two hosts pressing the button at the same instant
// land on the same room.
func (reg *Registry) freeCodeLocked() (string, error) {
	// 64 attempts is not a real limit on collisions, it is a guard against
	// spinning forever if the entropy source starts returning constants.
	for range 64 {
		code, err := newCode()
		if err != nil {
			return "", err
		}
		if _, taken := reg.rooms[code]; !taken {
			return code, nil
		}
	}
	return "", ErrNoCode
}

func newCode() (string, error) {
	buf := make([]byte, codeLength)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("lobby: generate code: %w", err)
	}
	// Modulo bias is real here and deliberately ignored: 256 mod 30 leaves the
	// first 16 symbols very slightly favoured. Room codes are collision-checked
	// and live for minutes, so the bias costs nothing an attacker could use and
	// rejection sampling would only add a loop to read wrong later.
	out := make([]byte, codeLength)
	for i, b := range buf {
		out[i] = codeAlphabet[int(b)%len(codeAlphabet)]
	}
	return string(out), nil
}

// Get finds a room. Codes are matched case-insensitively, because nobody types
// a room code with the shift key held.
func (reg *Registry) Get(code string) (*Room, bool) {
	code = NormalizeCode(code)

	reg.mu.RLock()
	defer reg.mu.RUnlock()
	room, ok := reg.rooms[code]
	return room, ok
}

// NormalizeCode puts a user-typed code into the form the registry stores.
func NormalizeCode(code string) string {
	return strings.ToUpper(strings.TrimSpace(code))
}

// remove is called by a room's goroutine as it exits, so a collected room stops
// being findable at the same moment it stops being able to answer.
func (reg *Registry) remove(code string) {
	reg.mu.Lock()
	delete(reg.rooms, code)
	count := len(reg.rooms)
	reg.mu.Unlock()

	reg.log.Info("room closed", "room", code, "rooms", count)
}

// Count reports how many rooms are live.
func (reg *Registry) Count() int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return len(reg.rooms)
}

// Streams reports how many subscribers are open across every room.
func (reg *Registry) Streams() int {
	reg.mu.RLock()
	defer reg.mu.RUnlock()
	return reg.streams
}

// acquireStream claims one unit of the process-wide subscriber budget.
//
// It is deliberately a Registry method taking the whole-process view: a room
// asking "may I accept another stream" cannot answer that itself. Rooms reach
// it through the streamBudget interface so the package's tests can exercise a
// room without standing up a registry.
func (reg *Registry) acquireStream() bool {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.streams >= maxStreams {
		return false
	}
	reg.streams++
	return true
}

// releaseStream returns a unit of the budget. Releasing more than was acquired
// would be a bug in Room's subscribe/unsubscribe pairing, so it floors at zero
// rather than going negative and hiding it.
func (reg *Registry) releaseStream() {
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if reg.streams > 0 {
		reg.streams--
	}
}

// Close stops every room and waits for each goroutine to exit.
//
// The rooms are snapshotted under the lock and closed outside it: each room's
// exit path calls remove, which takes the same lock, so holding it across
// room.close() would deadlock on the first room.
func (reg *Registry) Close() {
	reg.mu.RLock()
	rooms := make([]*Room, 0, len(reg.rooms))
	for _, r := range reg.rooms {
		rooms = append(rooms, r)
	}
	reg.mu.RUnlock()

	for _, r := range rooms {
		r.close()
	}
}
