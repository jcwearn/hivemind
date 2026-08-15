package ui

import (
	"context"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jcwearn/hivemind/internal/game"
	"github.com/jcwearn/hivemind/internal/lobby"
)

// testRNG is deterministic so food placement cannot make a render test flaky.
func testRNG() *rand.Rand { return rand.New(rand.NewPCG(1, 2)) }

func TestNewQREncodesAndLeavesAQuietZone(t *testing.T) {
	q, err := NewQR("http://example.test/j/ABCD")
	if err != nil {
		t.Fatalf("NewQR: %v", err)
	}

	if q.Path == "" {
		t.Fatal("path is empty")
	}
	if !strings.HasPrefix(q.Path, "M") {
		t.Errorf("path does not start with a moveto: %.20q", q.Path)
	}

	// Every module is inset by the quiet zone, so nothing may be drawn inside
	// it. A scanner will refuse a code drawn hard against its own edge.
	if !strings.HasPrefix(q.Path, "M4 4") {
		t.Errorf("first module at %.6q, want it offset by the quiet zone", q.Path)
	}

	// The extent has to leave room for the margin on both sides.
	if q.Extent < 2*quietZone+21 {
		t.Errorf("Extent = %d, too small to hold a QR plus its quiet zone", q.Extent)
	}
}

func TestNewQRRejectsInputItCannotEncode(t *testing.T) {
	// Well past the capacity of any QR version at error correction level M.
	_, err := NewQR(strings.Repeat("x", 8000))
	if err == nil {
		t.Error("NewQR accepted a payload no QR code can hold")
	}
}

func TestRowsSlicesTheGridByHeight(t *testing.T) {
	s := game.New(game.Config{Width: 6, Height: 4, StartLives: 3}, testRNG())

	got := rows(s)

	if len(got) != 4 {
		t.Fatalf("rows = %d, want 4", len(got))
	}
	for i, r := range got {
		if len(r) != 6 {
			t.Errorf("row %d has %d cells, want 6", i, len(r))
		}
	}
}

func TestPipsClampAtMax(t *testing.T) {
	tests := []struct {
		votes, on, off int
	}{
		{0, 0, maxPips},
		{3, 3, maxPips - 3},
		{maxPips, maxPips, 0},
		{maxPips + 50, maxPips, 0},
	}
	for _, tt := range tests {
		on, off := pips(tt.votes)
		if on != tt.on || off != tt.off {
			t.Errorf("pips(%d) = (%d, %d), want (%d, %d)", tt.votes, on, off, tt.on, tt.off)
		}
	}
}

func TestSeatClassWrapsAroundThePalette(t *testing.T) {
	first := seatClass(0)
	if seatClass(seatColours) != first {
		t.Errorf("seat %d = %q, want it to wrap to %q", seatColours, seatClass(seatColours), first)
	}
	// Every seat must land on a class the stylesheet actually defines.
	for seat := range 3 * seatColours {
		if !strings.HasPrefix(seatClass(seat), "seat seat-") {
			t.Errorf("seatClass(%d) = %q", seat, seatClass(seat))
		}
	}
}

func TestCanStartOnlyBetweenRounds(t *testing.T) {
	withPhase := func(p game.Phase, players int) lobby.Snapshot {
		s := lobby.Snapshot{State: game.New(game.DefaultConfig(), testRNG())}
		s.State.Phase = p
		for i := range players {
			s.Players = append(s.Players, lobby.Player{Seat: i, Name: "p"})
		}
		return s
	}

	tests := []struct {
		name    string
		snap    lobby.Snapshot
		want    bool
		wantLbl string
	}{
		{"lobby with players", withPhase(game.PhaseLobby, 2), true, "START"},
		{"lobby with nobody", withPhase(game.PhaseLobby, 0), false, "START"},
		{"mid round", withPhase(game.PhasePlaying, 2), false, "START"},
		{"countdown", withPhase(game.PhaseCountdown, 2), false, "START"},
		{"game over", withPhase(game.PhaseGameOver, 2), true, "PLAY AGAIN"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := canStart(tt.snap); got != tt.want {
				t.Errorf("canStart = %v, want %v", got, tt.want)
			}
			if got := startLabel(tt.snap); got != tt.wantLbl {
				t.Errorf("startLabel = %q, want %q", got, tt.wantLbl)
			}
		})
	}
}

func TestLivesSplitsIntoRemainingAndSpent(t *testing.T) {
	s := lobby.Snapshot{State: game.New(game.Config{Width: 4, Height: 4, StartLives: 3}, testRNG())}
	s.State.Lives = 1

	left, spent := lives(s)
	if left != 1 || spent != 2 {
		t.Errorf("lives = (%d, %d), want (1, 2)", left, spent)
	}

	// A life count above the configured start must not produce a negative bar.
	s.State.Lives = 9
	if _, spent := lives(s); spent != 0 {
		t.Errorf("spent = %d, want 0", spent)
	}
}

// A player-supplied name must not be able to inject markup. templ escapes on
// output, but the roster is the one place a stranger's text reaches the host's
// television, so it is asserted rather than trusted.
func TestPlayerNamesAreEscapedInTheRoster(t *testing.T) {
	s := lobby.Snapshot{
		Code:  "ABCD",
		State: game.New(game.DefaultConfig(), testRNG()),
		Players: []lobby.Player{
			{Seat: 0, Name: `<script>alert(1)</script>`},
		},
	}

	var out strings.Builder
	if err := ScreenFrame(s).Render(context.Background(), &out); err != nil {
		t.Fatalf("render: %v", err)
	}

	got := out.String()
	if strings.Contains(got, "<script>") {
		t.Error("a player name was rendered as live markup")
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Errorf("expected the name to be escaped, got: %.200q", got)
	}
}

func TestScreenAndPlayFramesRender(t *testing.T) {
	s := lobby.Snapshot{
		Code:    "ABCD",
		State:   game.New(game.DefaultConfig(), testRNG()),
		Players: []lobby.Player{{Seat: 0, Name: "ana"}},
		Tally:   map[game.Direction]int{game.DirUp: 1},
		Voters:  1,
	}

	var screen strings.Builder
	if err := ScreenFrame(s).Render(context.Background(), &screen); err != nil {
		t.Fatalf("ScreenFrame: %v", err)
	}
	if !strings.Contains(screen.String(), "ana") {
		t.Error("screen frame does not list the player")
	}
	if !strings.Contains(screen.String(), `class="pip on"`) {
		t.Error("screen frame does not show the vote")
	}

	var play strings.Builder
	if err := PlayFrame(s).Render(context.Background(), &play); err != nil {
		t.Fatalf("PlayFrame: %v", err)
	}
	if !strings.Contains(play.String(), "SCORE") {
		t.Error("play frame has no score")
	}
}
