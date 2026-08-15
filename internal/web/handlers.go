package web

import (
	"encoding/json"
	"net/http"
	"strings"
	"unicode"

	"github.com/a-h/templ"
	"github.com/jcwearn/hivemind/internal/game"
	"github.com/jcwearn/hivemind/internal/lobby"
	"github.com/jcwearn/hivemind/internal/ui"
)

// maxNameLen matches the input's maxlength. Enforced again here because the
// attribute is a courtesy to the browser, not a constraint on the request.
const maxNameLen = 12

func (s *Server) handleHome(w http.ResponseWriter, r *http.Request) {
	s.page(w, r, http.StatusOK, ui.Home(""))
}

func (s *Server) handleCreateRoom(w http.ResponseWriter, r *http.Request) {
	room, err := s.rooms.Create()
	if err != nil {
		s.log.Error("create room", "error", err)
		s.page(w, r, http.StatusInternalServerError, ui.Home("could not start a game just now."))
		return
	}
	http.Redirect(w, r, "/r/"+room.Code+"/screen", http.StatusSeeOther)
}

// handleJoinByCode backs the code box on the landing page.
func (s *Server) handleJoinByCode(w http.ResponseWriter, r *http.Request) {
	code := lobby.NormalizeCode(r.FormValue("code"))
	if _, ok := s.rooms.Get(code); !ok {
		s.page(w, r, http.StatusNotFound, ui.Home("no room called "+code+"."))
		return
	}
	http.Redirect(w, r, "/j/"+code, http.StatusSeeOther)
}

func (s *Server) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	code := lobby.NormalizeCode(r.PathValue("code"))
	if _, ok := s.rooms.Get(code); !ok {
		s.page(w, r, http.StatusNotFound, ui.NotFound(code))
		return
	}
	s.page(w, r, http.StatusOK, ui.JoinPage(code, ""))
}

func (s *Server) handleJoinSubmit(w http.ResponseWriter, r *http.Request) {
	code := lobby.NormalizeCode(r.PathValue("code"))
	room, ok := s.rooms.Get(code)
	if !ok {
		s.page(w, r, http.StatusNotFound, ui.NotFound(code))
		return
	}

	name := sanitizeName(r.FormValue("name"))
	if name == "" {
		s.page(w, r, http.StatusBadRequest, ui.JoinPage(code, "pick a name with at least one letter in it."))
		return
	}

	// Before any body is written, because it sets a cookie.
	id := s.cookies.playerID(w, r)

	if _, err := room.Join(id, name); err != nil {
		s.page(w, r, http.StatusNotFound, ui.NotFound(code))
		return
	}
	http.Redirect(w, r, "/r/"+code+"/play", http.StatusSeeOther)
}

func (s *Server) handleScreen(w http.ResponseWriter, r *http.Request) {
	code := lobby.NormalizeCode(r.PathValue("code"))
	room, ok := s.rooms.Get(code)
	if !ok {
		s.page(w, r, http.StatusNotFound, ui.NotFound(code))
		return
	}

	snap, err := room.Snapshot()
	if err != nil {
		s.page(w, r, http.StatusNotFound, ui.NotFound(code))
		return
	}

	joinURL := s.baseURL + "/j/" + code
	qr, err := ui.NewQR(joinURL)
	if err != nil {
		// A QR that will not encode is not worth losing the room over -- the
		// code is printed underneath it in letters anybody can type.
		s.log.Error("encode qr", "error", err, "url", joinURL)
	}

	s.page(w, r, http.StatusOK, ui.ScreenPage(snap, displayURL(joinURL), qr))
}

func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	code := lobby.NormalizeCode(r.PathValue("code"))
	room, ok := s.rooms.Get(code)
	if !ok {
		s.page(w, r, http.StatusNotFound, ui.NotFound(code))
		return
	}

	id := s.cookies.playerID(w, r)

	snap, err := room.Snapshot()
	if err != nil {
		s.page(w, r, http.StatusNotFound, ui.NotFound(code))
		return
	}

	me, ok := findPlayer(snap, id)
	if !ok {
		// Reaching the controller without a seat means a bookmarked URL, a
		// cleared cookie, or a room that was collected and remade. Send them
		// through the join flow rather than showing a controller that steers
		// nothing.
		http.Redirect(w, r, "/j/"+code, http.StatusSeeOther)
		return
	}

	s.page(w, r, http.StatusOK, ui.PlayPage(snap, me))
}

func (s *Server) handleScreenEvents(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, lobby.ScreenStream)
}

func (s *Server) handlePlayEvents(w http.ResponseWriter, r *http.Request) {
	s.stream(w, r, lobby.PlayStream)
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	room, ok := s.rooms.Get(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	dir := game.ParseDirection(r.FormValue("dir"))
	if dir == game.DirNone {
		http.Error(w, "unknown direction", http.StatusBadRequest)
		return
	}

	id := s.cookies.playerID(w, r)
	member, err := room.Vote(id, dir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !member {
		// A stale cookie against a live room. Tell htmx to send them to the
		// join page rather than silently swallowing every tap.
		w.Header().Set("HX-Redirect", "/j/"+room.Code)
		w.WriteHeader(http.StatusOK)
		return
	}

	// No body on purpose. The only per-player state on the controller is which
	// button looks pressed, and the browser already knew that before this
	// request left the phone.
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	room, ok := s.rooms.Get(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := room.Start(); err != nil {
		http.NotFound(w, r)
		return
	}
	// The resulting state arrives on everybody's stream, including the caller's.
	w.WriteHeader(http.StatusNoContent)
}

// handleHealth reports liveness and a little about what the process is doing.
// A static 200 would come back green from a server that had lost every room.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	select {
	case <-s.shutdown:
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "shutting_down"})
		return
	default:
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"rooms":  s.rooms.Count(),
	})
}

// page renders a full document.
func (s *Server) page(w http.ResponseWriter, r *http.Request, status int, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := c.Render(r.Context(), w); err != nil {
		// The status line is already written, so there is nothing to report to
		// the client beyond a truncated page.
		s.log.Error("render page", "error", err, "path", r.URL.Path)
	}
}

func findPlayer(snap lobby.Snapshot, id lobby.PlayerID) (lobby.Player, bool) {
	for _, p := range snap.Players {
		if p.ID == id {
			return p, true
		}
	}
	return lobby.Player{}, false
}

// sanitizeName trims a submitted name to something printable and short enough
// to fit a roster.
//
// Control characters are stripped rather than escaped: templ escapes on output
// anyway, so this is about a name that renders as an invisible smear rather
// than about injection.
func sanitizeName(raw string) string {
	cleaned := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)

	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return ""
	}

	// Counted in runes, so an emoji name is truncated to twelve characters
	// rather than to twelve bytes and a broken glyph.
	runes := []rune(cleaned)
	if len(runes) > maxNameLen {
		runes = runes[:maxNameLen]
	}
	return string(runes)
}

// displayURL strips the scheme for the join hint on the big screen. Nobody
// reads "https://" off a television, and the QR carries the real URL.
func displayURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	return u
}
