package web

import (
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

// maxRequestBody is the ceiling on anything a client can POST.
//
// The largest legitimate body here is a twelve-character name or a
// four-character room code -- a few dozen bytes. 4 KB is generous enough to
// never be hit by a real client and small enough that the expansion this app
// does on form values cannot matter.
//
// Without it, r.FormValue falls back to net/http's 10 MiB default, and the
// handlers amplify from there: NormalizeCode uppercases the whole thing,
// sanitizeName converts it to []rune at 4 bytes each, and an unknown code is
// concatenated into an error message and HTML-escaped. One request could hold
// tens of megabytes; a handful of them could exhaust the container.
const maxRequestBody = 4 << 10

// limitBody caps request bodies on everything except the event streams, which
// have no body to speak of and must not have their long-lived responses
// disturbed.
//
// MaxBytesReader makes the read fail rather than merely truncating, so an
// oversized body surfaces as a handler error instead of silently becoming a
// short one.
func limitBody(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil && r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, maxRequestBody)
		}
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code for the log line. net/http gives no
// other way to see what a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the wrapper's target so SSE still works through the
// middleware. Without this the type assertion in stream() fails and every
// stream 500s -- wrapping a ResponseWriter silently drops the interfaces it
// does not reimplement, which is the classic way to break streaming with
// logging middleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func requestLog(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(rec, r)

			// Streams are logged when they END, and they last for as long as
			// somebody is playing, so their duration is a measure of the party
			// rather than of the server. Logged at debug to keep them out of
			// the way.
			level := slog.LevelInfo
			if isEventStream(rec.Header().Get("Content-Type")) {
				level = slog.LevelDebug
			}

			log.Log(r.Context(), level, "request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", rec.status,
				"duration", time.Since(start).Round(time.Millisecond).String(),
			)
		})
	}
}

func isEventStream(contentType string) bool {
	return strings.HasPrefix(contentType, "text/event-stream")
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				// http.ErrAbortHandler is the documented way for a handler to
				// give up on a connection, and net/http suppresses it. Passing
				// it along keeps that contract instead of logging a stack trace
				// for something deliberate.
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}

				log.Error("panic recovered",
					"error", rec,
					"path", r.URL.Path,
					"stack", string(debug.Stack()),
				)
				http.Error(w, "internal server error", http.StatusInternalServerError)
			}()

			next.ServeHTTP(w, r)
		})
	}
}
