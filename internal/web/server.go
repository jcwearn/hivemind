// Package web is the HTTP surface: routing, server-sent events, and the signed
// cookie that lets a phone keep its seat.
//
// The transport split is the design decision worth knowing about. State flows
// down over SSE, and player input flows up over ordinary form posts. SSE is
// plain HTTP, so it crosses Cloudflare Tunnel with no configuration, reconnects
// on its own, and can be read in a test with a bufio.Scanner. Input is
// low-frequency and discrete -- a vote, a join, a start -- which is exactly what
// a form post is for. Nothing here needs a WebSocket.
package web

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/a-h/templ"
	"github.com/jcwearn/hivemind/internal/lobby"
	"github.com/jcwearn/hivemind/internal/ui"
	"github.com/jcwearn/hivemind/static"
)

// Options configures the server.
type Options struct {
	Logger *slog.Logger
	// BaseURL is the origin players reach this server on. It is what goes in
	// the QR code, so it has to be the public address rather than the listen
	// address -- behind Cloudflare Tunnel those are never the same.
	BaseURL      string
	CookieSecret []byte
	SecureCookie bool
}

// Server holds the router's dependencies.
type Server struct {
	log     *slog.Logger
	rooms   *lobby.Registry
	baseURL string
	cookies *cookieCodec

	// shutdown is closed once, and every live SSE stream selects on it. See
	// Shutdown for why this exists at all.
	shutdown     chan struct{}
	shutdownOnce sync.Once
}

// New builds a server and the room registry behind it.
func New(opts Options) *Server {
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}

	s := &Server{
		log:      log,
		baseURL:  opts.BaseURL,
		cookies:  newCookieCodec(opts.CookieSecret, opts.SecureCookie),
		shutdown: make(chan struct{}),
	}

	s.rooms = lobby.NewRegistry(lobby.Options{
		Logger: log,
		Render: render,
	})

	return s
}

// render turns one snapshot into the two frames every subscriber receives.
//
// Both are produced in a single call, on the room's goroutine, so a tick costs
// one render no matter how many people are connected. This is the function that
// makes that claim true, and it is the reason lobby takes a RenderFunc instead
// of importing these templates itself.
func render(snap lobby.Snapshot) lobby.Frames {
	return lobby.Frames{
		Screen: renderFragment(ui.ScreenFrame(snap)),
		Play:   renderFragment(ui.PlayFrame(snap)),
	}
}

// renderFragment renders a component to bytes.
//
// A render error here would mean a template bug rather than anything about this
// request, and there is no useful way to report it to twenty phones mid-round --
// so it is logged by the caller's absence of output and the frame is skipped.
// templ's generated code does not fail on well-formed input.
func renderFragment(c templ.Component) []byte {
	var buf bytes.Buffer
	if err := c.Render(context.Background(), &buf); err != nil {
		slog.Error("render fragment", "error", err)
		return nil
	}
	return buf.Bytes()
}

// Routes builds the router.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()

	// Go 1.22 patterns: method and wildcards are part of the pattern, so there
	// is nothing here a third-party router would add.
	mux.HandleFunc("GET /{$}", s.handleHome)
	mux.HandleFunc("POST /rooms", s.handleCreateRoom)
	mux.HandleFunc("POST /join", s.handleJoinByCode)

	mux.HandleFunc("GET /j/{code}", s.handleJoinPage)
	mux.HandleFunc("POST /j/{code}", s.handleJoinSubmit)

	mux.HandleFunc("GET /r/{code}/screen", s.handleScreen)
	mux.HandleFunc("GET /r/{code}/screen/events", s.handleScreenEvents)
	mux.HandleFunc("GET /r/{code}/play", s.handlePlay)
	mux.HandleFunc("GET /r/{code}/play/events", s.handlePlayEvents)
	mux.HandleFunc("POST /r/{code}/vote", s.handleVote)
	mux.HandleFunc("POST /r/{code}/name", s.handleRename)
	mux.HandleFunc("POST /r/{code}/start", s.handleStart)

	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle("GET /static/", http.StripPrefix("/static/", staticHandler()))

	return recoverer(s.log)(requestLog(s.log)(mux))
}

// staticHandler serves the embedded assets.
//
// Vendored files carry their version in the filename, so they can be cached
// forever; a new htmx means a new path. The stylesheet and script do not, so
// they get revalidated. Getting this backwards is how a party ends up debugging
// a stale controller.
func staticHandler() http.Handler {
	fileServer := http.FileServerFS(static.FS)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "vendor/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Shutdown ends every live SSE stream and stops every room.
//
// It has to run BEFORE http.Server.Shutdown, and the ordering is not an
// optimisation. An SSE response is an ordinary response as far as net/http is
// concerned -- not a hijacked connection -- so http.Server.Shutdown waits for
// each handler to return on its own. A stream that is working correctly never
// does, so without this the server would block for the whole shutdown timeout
// on every connected phone, every time.
func (s *Server) Shutdown() {
	s.shutdownOnce.Do(func() {
		close(s.shutdown)
		s.rooms.Close()
	})
}

// Rooms exposes the registry for tests.
func (s *Server) Rooms() *lobby.Registry { return s.rooms }
