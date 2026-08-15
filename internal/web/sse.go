package web

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/jcwearn/hivemind/internal/lobby"
)

// heartbeatInterval is how often an otherwise idle stream emits a comment.
//
// Cloudflare cuts an idle proxied connection at 100 seconds, and a room sitting
// in its lobby with nobody voting produces no frames at all -- the game is not
// running, so there is nothing to send. Without this, every phone that joined
// early would silently lose its stream before the host pressed start. Twenty
// seconds leaves room for a missed beat or two before anything notices.
const heartbeatInterval = 20 * time.Second

// stream is the SSE endpoint behind both /screen/events and /play/events.
func (s *Server) stream(w http.ResponseWriter, r *http.Request, kind lobby.StreamKind) {
	room, ok := s.rooms.Get(r.PathValue("code"))
	if !ok {
		http.NotFound(w, r)
		return
	}

	// Asserted before the room is told about this subscriber, so a server that
	// cannot stream fails cleanly instead of leaving a subscription behind.
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	initial, frames, cancel, err := room.Stream(kind)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	// Nginx and several other proxies buffer a response until it completes,
	// which for a stream means forever. Cloudflare honours the event-stream
	// content type on its own, but this costs a header and removes the failure
	// mode entirely for anything else somebody puts in front of this.
	h.Set("X-Accel-Buffering", "no")
	// Deliberately no Connection: keep-alive. It is hop-by-hop, meaningless on
	// HTTP/1.1 where it is already the default, and illegal in HTTP/2.
	w.WriteHeader(http.StatusOK)

	// The first frame goes out before the loop so a screen paints on load
	// rather than staying blank until the next tick.
	if err := writeEvent(w, "frame", initial); err != nil {
		return
	}
	flusher.Flush()

	beat := time.NewTicker(heartbeatInterval)
	defer beat.Stop()

	for {
		select {
		case <-r.Context().Done():
			// The phone went away, locked, or navigated off.
			return

		case <-s.shutdown:
			// The process is going down. Returning here is what lets
			// http.Server.Shutdown finish promptly instead of waiting out its
			// timeout on a stream that would otherwise never end.
			return

		case <-room.Done():
			// The room was collected while this stream was open.
			return

		case frame := <-frames:
			if err := writeEvent(w, "frame", frame); err != nil {
				return
			}
			flusher.Flush()

		case <-beat.C:
			// A comment. The browser ignores it; every proxy between here and
			// the phone sees traffic.
			if _, err := io.WriteString(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeEvent encodes one SSE message.
//
// The per-line prefixing is not optional. SSE is a line-oriented format, so a
// payload containing a newline has to be split across several `data:` lines,
// which the browser rejoins with "\n" on the other side. Rendered HTML is full
// of newlines, so a naive single-line write would truncate every frame at the
// first one -- and the visible symptom is a page that mostly works, which is
// the worst kind of bug to find at a party.
func writeEvent(w io.Writer, event string, data []byte) error {
	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteByte('\n')

	// Carriage returns are line terminators in SSE too, so they are normalised
	// away rather than left to split a line somewhere the browser does not
	// expect.
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))

	for _, line := range bytes.Split(data, []byte("\n")) {
		b.WriteString("data: ")
		b.Write(line)
		b.WriteByte('\n')
	}
	b.WriteByte('\n')

	_, err := w.Write(b.Bytes())
	return err
}
