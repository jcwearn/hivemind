package ui

import (
	"strconv"

	"github.com/jcwearn/hivemind/internal/game"
	"github.com/jcwearn/hivemind/internal/lobby"
)

// itoa keeps the templates free of strconv noise. Every number these templates
// print is a small count -- score, lives, votes -- so there is nothing to format.
func itoa(n int) string { return strconv.Itoa(n) }

func isCountdown(s lobby.Snapshot) bool { return s.State.Phase == game.PhaseCountdown }

// maxPips caps how many blocks a vote tally draws. Past a dozen the bar stops
// being readable at TV distance and the number beside it is doing the work
// anyway.
const maxPips = 12

// seatColours is how many distinct player colours the stylesheet defines. Seats
// wrap around it, so a room of thirty reuses colours rather than running out.
const seatColours = 8

// directions is the fixed order the tally and the controller lay their four
// headings out in, so nothing moves between frames.
var directions = []game.Direction{game.DirUp, game.DirLeft, game.DirRight, game.DirDown}

// rows slices the flat grid into display rows.
//
// The board is drawn as rows of cells rather than a CSS grid because a CSS grid
// needs its column count in a style attribute, and a per-render style attribute
// is the one thing in a template that cannot be written without either
// sanitiser workarounds or hardcoding the board width in the stylesheet.
func rows(s game.State) [][]game.Cell {
	cells := s.Grid()
	out := make([][]game.Cell, 0, s.Cfg.Height)
	for y := range s.Cfg.Height {
		out = append(out, cells[y*s.Cfg.Width:(y+1)*s.Cfg.Width])
	}
	return out
}

func cellClass(c game.Cell) string {
	switch c {
	case game.CellHead:
		return "cell head"
	case game.CellBody:
		return "cell body"
	case game.CellFood:
		return "cell food"
	default:
		return "cell"
	}
}

func seatClass(seat int) string {
	switch seat % seatColours {
	case 0:
		return "seat seat-0"
	case 1:
		return "seat seat-1"
	case 2:
		return "seat seat-2"
	case 3:
		return "seat seat-3"
	case 4:
		return "seat seat-4"
	case 5:
		return "seat seat-5"
	case 6:
		return "seat seat-6"
	default:
		return "seat seat-7"
	}
}

func dirGlyph(d game.Direction) string {
	switch d {
	case game.DirUp:
		return "▲"
	case game.DirDown:
		return "▼"
	case game.DirLeft:
		return "◀"
	default:
		return "▶"
	}
}

// pips returns the number of filled blocks to draw for n votes, and how many
// empty ones follow it.
func pips(n int) (on, off int) {
	on = min(n, maxPips)
	return on, maxPips - on
}

// phaseHeadline is what the big screen shouts between rounds.
func phaseHeadline(s lobby.Snapshot) string {
	switch s.State.Phase {
	case game.PhaseCountdown:
		return "GET READY"
	case game.PhaseGameOver:
		return "GAME OVER"
	case game.PhasePlaying:
		return ""
	default:
		if len(s.Players) == 0 {
			return "WAITING FOR PLAYERS"
		}
		return "READY WHEN YOU ARE"
	}
}

// canStart reports whether the host's button should be live. A round with
// nobody in it would tick along steering nothing, which reads as broken.
func canStart(s lobby.Snapshot) bool {
	if len(s.Players) == 0 {
		return false
	}
	return s.State.Phase == game.PhaseLobby || s.State.Phase == game.PhaseGameOver
}

func startLabel(s lobby.Snapshot) string {
	if s.State.Phase == game.PhaseGameOver {
		return "PLAY AGAIN"
	}
	return "START"
}

// lives renders the shared life pool as filled and spent hearts.
func lives(s lobby.Snapshot) (left, spent int) {
	left = s.State.Lives
	spent = s.State.Cfg.StartLives - left
	return left, max(spent, 0)
}
