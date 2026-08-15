package game

import (
	"math/rand/v2"
	"testing"
	"time"
)

// testRNG is deterministic, so a failure reproduces. Food placement is the only
// consumer, and the tests that care about where the food lands set it directly.
func testRNG() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func testConfig() Config { return Config{Width: 10, Height: 8, StartLives: 3} }

// playing returns a state mid-round with the snake laid out horizontally,
// head first, pointing right. Food is parked in a corner well out of the way so
// tests that do not care about it never trip over it.
func playing() State {
	return State{
		Cfg:   testConfig(),
		Phase: PhasePlaying,
		Snake: []Point{{X: 5, Y: 4}, {X: 4, Y: 4}, {X: 3, Y: 4}},
		Dir:   DirRight,
		Food:  Point{X: 9, Y: 0},
		Lives: 3,
	}
}

func TestTally(t *testing.T) {
	tests := []struct {
		name    string
		votes   map[string]Direction
		current Direction
		want    Direction
	}{
		{
			name:    "no votes keeps heading",
			votes:   map[string]Direction{},
			current: DirRight,
			want:    DirRight,
		},
		{
			name:    "abstentions are not votes",
			votes:   map[string]Direction{"a": DirNone, "b": DirNone},
			current: DirUp,
			want:    DirUp,
		},
		{
			name:    "plurality wins",
			votes:   map[string]Direction{"a": DirUp, "b": DirUp, "c": DirDown},
			current: DirRight,
			want:    DirUp,
		},
		{
			name:    "single vote wins",
			votes:   map[string]Direction{"a": DirDown},
			current: DirRight,
			want:    DirDown,
		},
		{
			name:    "two-way tie keeps heading",
			votes:   map[string]Direction{"a": DirUp, "b": DirDown},
			current: DirRight,
			want:    DirRight,
		},
		{
			name:    "three-way tie keeps heading",
			votes:   map[string]Direction{"a": DirUp, "b": DirDown, "c": DirLeft},
			current: DirRight,
			want:    DirRight,
		},
		{
			name:    "tie with the current heading still keeps it",
			votes:   map[string]Direction{"a": DirUp, "b": DirRight},
			current: DirRight,
			want:    DirRight,
		},
		{
			name:    "reversal into the neck is discarded",
			votes:   map[string]Direction{"a": DirLeft},
			current: DirRight,
			want:    DirRight,
		},
		{
			name:    "discarded reversal does not beat a real vote",
			votes:   map[string]Direction{"a": DirLeft, "b": DirLeft, "c": DirUp},
			current: DirRight,
			want:    DirUp,
		},
		{
			name:    "majority still wins over a plurality of one",
			votes:   map[string]Direction{"a": DirUp, "b": DirUp, "c": DirDown, "d": DirLeft},
			current: DirLeft,
			want:    DirUp,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Tally(tt.votes, tt.current); got != tt.want {
				t.Errorf("Tally() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestStepMoves(t *testing.T) {
	tests := []struct {
		name     string
		dir      Direction
		wantHead Point
	}{
		{"right", DirRight, Point{X: 6, Y: 4}},
		{"up decreases Y", DirUp, Point{X: 5, Y: 3}},
		{"down increases Y", DirDown, Point{X: 5, Y: 5}},
		{"none keeps heading", DirNone, Point{X: 6, Y: 4}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Step(playing(), tt.dir, testRNG())
			if got.Snake[0] != tt.wantHead {
				t.Errorf("head = %v, want %v", got.Snake[0], tt.wantHead)
			}
			if len(got.Snake) != 3 {
				t.Errorf("length = %d, want 3 (no food eaten)", len(got.Snake))
			}
		})
	}
}

func TestStepDoesNotMutateInput(t *testing.T) {
	before := playing()
	snapshot := make([]Point, len(before.Snake))
	copy(snapshot, before.Snake)

	Step(before, DirUp, testRNG())

	for i := range snapshot {
		if before.Snake[i] != snapshot[i] {
			t.Fatalf("Step mutated its argument at %d: %v, want %v", i, before.Snake[i], snapshot[i])
		}
	}
}

// A frame handed to a subscriber must not change under it. Aliasing the
// previous backing array is the easy way to get this wrong, so it is asserted
// directly rather than left to Step's implementation staying careful.
func TestStepDoesNotAliasPreviousSnake(t *testing.T) {
	s := playing()
	next := Step(s, DirUp, testRNG())
	after := Step(next, DirUp, testRNG())

	if &next.Snake[0] == &after.Snake[0] {
		t.Fatal("consecutive states share a backing array")
	}
	if next.Snake[0] != (Point{X: 5, Y: 3}) {
		t.Errorf("earlier state was overwritten: head = %v", next.Snake[0])
	}
}

func TestStepEatsFood(t *testing.T) {
	s := playing()
	s.Food = Point{X: 6, Y: 4} // directly ahead

	got := Step(s, DirRight, testRNG())

	if got.Score != 1 {
		t.Errorf("Score = %d, want 1", got.Score)
	}
	if len(got.Snake) != 4 {
		t.Errorf("length = %d, want 4 after growing", len(got.Snake))
	}
	if got.Food == (Point{X: 6, Y: 4}) {
		t.Error("food was not moved after being eaten")
	}
}

func TestStepWallCollision(t *testing.T) {
	tests := []struct {
		name string
		head Point
		dir  Direction
	}{
		{"right wall", Point{X: 9, Y: 4}, DirRight},
		{"left wall", Point{X: 0, Y: 4}, DirLeft},
		{"top wall", Point{X: 5, Y: 0}, DirUp},
		{"bottom wall", Point{X: 5, Y: 7}, DirDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := playing()
			s.Snake = []Point{tt.head, {X: tt.head.X, Y: tt.head.Y + 1}, {X: tt.head.X, Y: tt.head.Y + 2}}
			s.Dir = tt.dir

			got := Step(s, tt.dir, testRNG())

			if got.Lives != 2 {
				t.Errorf("Lives = %d, want 2", got.Lives)
			}
			if got.Phase != PhaseCountdown {
				t.Errorf("Phase = %v, want countdown", got.Phase)
			}
		})
	}
}

func TestStepSelfCollision(t *testing.T) {
	// A tight coil: moving up from the head walks straight into the body.
	s := playing()
	s.Snake = []Point{
		{X: 5, Y: 4},
		{X: 4, Y: 4},
		{X: 4, Y: 3},
		{X: 5, Y: 3},
		{X: 6, Y: 3},
	}
	s.Dir = DirRight

	got := Step(s, DirUp, testRNG())

	if got.Lives != 2 {
		t.Errorf("Lives = %d, want 2 after eating itself", got.Lives)
	}
	if got.Phase != PhaseCountdown {
		t.Errorf("Phase = %v, want countdown", got.Phase)
	}
}

// The tail vacates its square on the same tick the head enters it, so chasing
// your own tail is legal. Getting this wrong makes the game feel broken in a
// way players notice immediately and cannot articulate.
func TestStepIntoVacatingTailIsLegal(t *testing.T) {
	s := playing()
	s.Snake = []Point{
		{X: 5, Y: 4},
		{X: 5, Y: 5},
		{X: 6, Y: 5},
		{X: 6, Y: 4},
	}
	s.Dir = DirUp

	got := Step(s, DirRight, testRNG())

	if got.Lives != 3 {
		t.Errorf("Lives = %d, want 3 -- following the tail must be legal", got.Lives)
	}
	if got.Snake[0] != (Point{X: 6, Y: 4}) {
		t.Errorf("head = %v, want the square the tail just left", got.Snake[0])
	}
}

func TestStepLastLifeEndsGame(t *testing.T) {
	s := playing()
	s.Lives = 1
	s.Snake = []Point{{X: 9, Y: 4}, {X: 8, Y: 4}, {X: 7, Y: 4}}

	got := Step(s, DirRight, testRNG())

	if got.Phase != PhaseGameOver {
		t.Errorf("Phase = %v, want gameover", got.Phase)
	}
	if got.Lives != 0 {
		t.Errorf("Lives = %d, want 0", got.Lives)
	}
}

func TestCountdownAdvancesThenPlays(t *testing.T) {
	s := playing()
	s.Phase = PhaseCountdown
	s.Countdown = 2
	head := s.Snake[0]

	s = Step(s, DirUp, testRNG())
	if s.Phase != PhaseCountdown || s.Countdown != 1 {
		t.Fatalf("after one tick: phase=%v countdown=%d, want countdown/1", s.Phase, s.Countdown)
	}
	if s.Snake[0] != head {
		t.Error("snake moved during the countdown")
	}

	s = Step(s, DirUp, testRNG())
	if s.Phase != PhasePlaying {
		t.Fatalf("phase = %v, want playing", s.Phase)
	}

	s = Step(s, DirUp, testRNG())
	if s.Snake[0] == head {
		t.Error("snake did not move once play resumed")
	}
}

func TestLobbyAndGameOverAreInert(t *testing.T) {
	for _, phase := range []Phase{PhaseLobby, PhaseGameOver} {
		t.Run(phase.String(), func(t *testing.T) {
			s := playing()
			s.Phase = phase
			head := s.Snake[0]

			got := Step(s, DirUp, testRNG())

			if got.Snake[0] != head {
				t.Errorf("snake moved in phase %v", phase)
			}
			if got.Phase != phase {
				t.Errorf("phase changed to %v", got.Phase)
			}
		})
	}
}

func TestStartResetsScoreAndLives(t *testing.T) {
	s := New(testConfig(), testRNG())
	s.Phase = PhaseGameOver
	s.Score = 12
	s.Lives = 0

	got := Start(s, testRNG())

	if got.Phase != PhaseCountdown {
		t.Errorf("Phase = %v, want countdown", got.Phase)
	}
	if got.Score != 0 {
		t.Errorf("Score = %d, want 0", got.Score)
	}
	if got.Lives != testConfig().StartLives {
		t.Errorf("Lives = %d, want %d", got.Lives, testConfig().StartLives)
	}
}

func TestStartIsIgnoredMidRound(t *testing.T) {
	s := playing()
	s.Score = 7

	got := Start(s, testRNG())

	if got.Score != 7 {
		t.Errorf("Score = %d -- Start must not restart a running round", got.Score)
	}
}

func TestPlaceFoodNeverLandsOnTheSnake(t *testing.T) {
	cfg := Config{Width: 4, Height: 3, StartLives: 1}
	snake := []Point{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 2, Y: 0}, {X: 3, Y: 0}, {X: 0, Y: 1}}
	rng := testRNG()

	for range 200 {
		food := placeFood(cfg, snake, rng)
		if outOfBounds(food, cfg) {
			t.Fatalf("food %v is off the board", food)
		}
		for _, p := range snake {
			if food == p {
				t.Fatalf("food %v landed on the snake", food)
			}
		}
	}
}

func TestGrid(t *testing.T) {
	s := playing()
	cells := s.Grid()

	if len(cells) != s.Cfg.Width*s.Cfg.Height {
		t.Fatalf("len = %d, want %d", len(cells), s.Cfg.Width*s.Cfg.Height)
	}

	at := func(p Point) Cell { return cells[p.Y*s.Cfg.Width+p.X] }

	if got := at(Point{X: 5, Y: 4}); got != CellHead {
		t.Errorf("head cell = %v, want CellHead", got)
	}
	if got := at(Point{X: 4, Y: 4}); got != CellBody {
		t.Errorf("body cell = %v, want CellBody", got)
	}
	if got := at(Point{X: 9, Y: 0}); got != CellFood {
		t.Errorf("food cell = %v, want CellFood", got)
	}
	if got := at(Point{X: 0, Y: 7}); got != CellEmpty {
		t.Errorf("empty cell = %v, want CellEmpty", got)
	}
}

func TestTickIntervalRampsAndFloors(t *testing.T) {
	first := TickInterval(0)
	later := TickInterval(10)

	if later >= first {
		t.Errorf("TickInterval(10) = %v, want faster than TickInterval(0) = %v", later, first)
	}
	if got := TickInterval(10_000); got != 120*time.Millisecond {
		t.Errorf("TickInterval(10000) = %v, want the 120ms floor", got)
	}
}

func TestParseDirectionRoundTrips(t *testing.T) {
	for _, d := range []Direction{DirUp, DirDown, DirLeft, DirRight} {
		if got := ParseDirection(d.String()); got != d {
			t.Errorf("ParseDirection(%q) = %v, want %v", d.String(), got, d)
		}
	}
	if got := ParseDirection("sideways"); got != DirNone {
		t.Errorf("ParseDirection(garbage) = %v, want DirNone", got)
	}
}
