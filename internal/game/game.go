// Package game holds the rules of hivemind's snake, and nothing else.
//
// Everything here is a pure function of its arguments. There is no clock, no
// I/O, and no global randomness -- callers pass a *rand.Rand, and the tick loop
// in internal/lobby owns time. That is what makes the whole rule set testable
// by table without a single fake, and it is why the interesting bugs in this
// program cannot hide here.
package game

import (
	"math/rand/v2"
	"time"
)

// Direction is a heading. The zero value means "no opinion", which is what an
// absent or cleared vote is.
type Direction uint8

// The four headings, plus the absence of one.
const (
	DirNone Direction = iota
	DirUp
	DirDown
	DirLeft
	DirRight
)

func (d Direction) String() string {
	switch d {
	case DirUp:
		return "up"
	case DirDown:
		return "down"
	case DirLeft:
		return "left"
	case DirRight:
		return "right"
	default:
		return "none"
	}
}

// Opposite reports the heading that reverses d. DirNone has no opposite.
func (d Direction) Opposite() Direction {
	switch d {
	case DirUp:
		return DirDown
	case DirDown:
		return DirUp
	case DirLeft:
		return DirRight
	case DirRight:
		return DirLeft
	default:
		return DirNone
	}
}

// ParseDirection maps the wire form used by the controller buttons.
func ParseDirection(s string) Direction {
	switch s {
	case "up":
		return DirUp
	case "down":
		return DirDown
	case "left":
		return DirLeft
	case "right":
		return DirRight
	default:
		return DirNone
	}
}

// Point is a grid coordinate. Origin is top-left, so DirUp decreases Y.
type Point struct{ X, Y int }

// Phase is where a room is in the round lifecycle.
type Phase uint8

// The round lifecycle. Only Start moves out of Lobby or GameOver; Step drives
// everything else.
const (
	PhaseLobby Phase = iota
	PhaseCountdown
	PhasePlaying
	PhaseGameOver
)

func (p Phase) String() string {
	switch p {
	case PhaseCountdown:
		return "countdown"
	case PhasePlaying:
		return "playing"
	case PhaseGameOver:
		return "gameover"
	default:
		return "lobby"
	}
}

// Cell is what occupies one grid square, for rendering.
type Cell uint8

// What a rendered square can contain.
const (
	CellEmpty Cell = iota
	CellHead
	CellBody
	CellFood
)

// Config is the fixed shape of a game. It does not change during a round.
type Config struct {
	Width      int
	Height     int
	StartLives int
}

// DefaultConfig is tuned for a phone-sized controller and a TV-sized board: big
// enough that a committee can steer it somewhere, small enough that the whole
// grid is one readable HTML fragment.
func DefaultConfig() Config {
	return Config{Width: 24, Height: 18, StartLives: 3}
}

// State is a complete snapshot of a round. It is copied by value on every tick,
// so every field must stay cheap to copy -- Snake is the only allocation, and
// Step is careful to build a fresh slice rather than alias the previous one.
type State struct {
	Cfg   Config
	Phase Phase
	Snake []Point // head first
	Dir   Direction
	Food  Point
	Score int
	Lives int
	// Countdown counts ticks remaining before play resumes. Only meaningful in
	// PhaseCountdown.
	Countdown int
}

// countdownTicks is how long the pause lasts after a death, and before the
// first move of a round. Long enough to read "get ready", short enough that
// nobody puts their phone down.
const countdownTicks = 3

// New returns a room's opening state: a snake parked in the middle, no food
// yet, waiting for the host to start.
func New(cfg Config, rng *rand.Rand) State {
	s := State{
		Cfg:   cfg,
		Phase: PhaseLobby,
		Lives: cfg.StartLives,
	}
	s.reset(rng)
	return s
}

// reset re-centres the snake and re-places the food, leaving score, lives and
// phase alone. Used both at construction and after a death.
func (s *State) reset(rng *rand.Rand) {
	mid := Point{X: s.Cfg.Width / 2, Y: s.Cfg.Height / 2}
	s.Snake = []Point{mid, {X: mid.X - 1, Y: mid.Y}, {X: mid.X - 2, Y: mid.Y}}
	s.Dir = DirRight
	s.Food = placeFood(s.Cfg, s.Snake, rng)
}

// Start moves a lobby into its opening countdown. It is a no-op unless the room
// is in PhaseLobby or PhaseGameOver, so a stray double-click cannot restart a
// round that is already running.
func Start(s State, rng *rand.Rand) State {
	if s.Phase != PhaseLobby && s.Phase != PhaseGameOver {
		return s
	}
	s.Score = 0
	s.Lives = s.Cfg.StartLives
	s.Phase = PhaseCountdown
	s.Countdown = countdownTicks
	s.reset(rng)
	return s
}

// Tally reduces every player's vote to the one heading the snake will take.
//
// The rules, in order:
//
//   - DirNone is not a vote, it is the absence of one.
//   - A vote to reverse straight back into the neck is discarded rather than
//     counted. It is always fatal and never what the voter meant, and dropping
//     it here means the griefer who spams it just wastes their own vote.
//   - Plurality wins.
//   - A tie keeps the current heading. That is deliberately not a coin flip:
//     players need to be able to predict what a deadlocked room does, and
//     "nobody agreed, so we keep going" is the rule everyone works out for
//     themselves within about two ticks.
func Tally(votes map[string]Direction, current Direction) Direction {
	var counts [5]int
	banned := current.Opposite()

	for _, v := range votes {
		if v == DirNone || v == banned {
			continue
		}
		counts[v]++
	}

	best, bestCount, tied := current, 0, false
	for d := DirUp; d <= DirRight; d++ {
		switch n := counts[d]; {
		case n > bestCount:
			best, bestCount, tied = d, n, false
		case n == bestCount && n > 0 && d != best:
			tied = true
		}
	}

	if bestCount == 0 || tied {
		return current
	}
	return best
}

// Step advances the round by exactly one tick, given the heading Tally chose.
// It never mutates its argument.
func Step(s State, dir Direction, rng *rand.Rand) State {
	switch s.Phase {
	case PhaseCountdown:
		s.Countdown--
		if s.Countdown <= 0 {
			s.Phase = PhasePlaying
			s.Countdown = 0
		}
		return s
	case PhasePlaying:
		// fall through to the move below
	default:
		// Lobby and game over are inert; only Start moves out of them.
		return s
	}

	if dir != DirNone {
		s.Dir = dir
	}

	head := advance(s.Snake[0], s.Dir)

	if outOfBounds(head, s.Cfg) || hitsSelf(head, s.Snake) {
		return s.die(rng)
	}

	ate := head == s.Food

	// A fresh slice every tick. Aliasing the previous state's backing array
	// would let a broadcast frame mutate under a subscriber that had not yet
	// rendered it, which is exactly the bug the snapshot design exists to
	// prevent.
	grown := make([]Point, 0, len(s.Snake)+1)
	grown = append(grown, head)
	if ate {
		grown = append(grown, s.Snake...)
	} else {
		grown = append(grown, s.Snake[:len(s.Snake)-1]...)
	}
	s.Snake = grown

	if ate {
		s.Score++
		s.Food = placeFood(s.Cfg, s.Snake, rng)
	}

	return s
}

// die spends a life. The pool is shared by the whole room rather than per
// player: this is a co-op game, and a party that eliminates people one at a
// time leaves its funniest participants watching.
func (s State) die(rng *rand.Rand) State {
	s.Lives--
	if s.Lives <= 0 {
		s.Lives = 0
		s.Phase = PhaseGameOver
		return s
	}
	s.Phase = PhaseCountdown
	s.Countdown = countdownTicks
	s.reset(rng)
	return s
}

func advance(p Point, d Direction) Point {
	switch d {
	case DirUp:
		p.Y--
	case DirDown:
		p.Y++
	case DirLeft:
		p.X--
	case DirRight:
		p.X++
	}
	return p
}

func outOfBounds(p Point, cfg Config) bool {
	return p.X < 0 || p.Y < 0 || p.X >= cfg.Width || p.Y >= cfg.Height
}

// hitsSelf excludes the tail: it vacates the square on the same tick the head
// enters it, so following your own tail is legal and feels right to play.
func hitsSelf(head Point, snake []Point) bool {
	for _, p := range snake[:len(snake)-1] {
		if p == head {
			return true
		}
	}
	return false
}

// placeFood picks uniformly among free squares. Building the candidate list is
// O(W*H) per apple, which at 24x18 is noise, and it is the only approach that
// cannot loop forever as the board fills up.
func placeFood(cfg Config, snake []Point, rng *rand.Rand) Point {
	occupied := make(map[Point]struct{}, len(snake))
	for _, p := range snake {
		occupied[p] = struct{}{}
	}

	free := make([]Point, 0, cfg.Width*cfg.Height-len(snake))
	for y := range cfg.Height {
		for x := range cfg.Width {
			p := Point{X: x, Y: y}
			if _, taken := occupied[p]; !taken {
				free = append(free, p)
			}
		}
	}
	if len(free) == 0 {
		// The snake fills the board. Unreachable in practice, and parking the
		// food under the head is better than panicking in front of a room.
		return snake[0]
	}
	return free[rng.IntN(len(free))]
}

// Grid flattens the state into one Cell per square, row-major, for rendering.
// It is a spatial index rather than presentation: the caller decides what a
// CellFood looks like.
func (s State) Grid() []Cell {
	cells := make([]Cell, s.Cfg.Width*s.Cfg.Height)
	idx := func(p Point) int { return p.Y*s.Cfg.Width + p.X }

	if !outOfBounds(s.Food, s.Cfg) {
		cells[idx(s.Food)] = CellFood
	}
	for i, p := range s.Snake {
		if outOfBounds(p, s.Cfg) {
			continue
		}
		if i == 0 {
			cells[idx(p)] = CellHead
		} else {
			cells[idx(p)] = CellBody
		}
	}
	return cells
}

// TickInterval is how long the room waits between steps. The ramp is the
// difficulty curve: a room that is doing well gets less time to argue.
func TickInterval(score int) time.Duration {
	const (
		start = 400 * time.Millisecond
		floor = 120 * time.Millisecond
		step  = 10 * time.Millisecond
	)
	d := start - time.Duration(score)*step
	return max(d, floor)
}
