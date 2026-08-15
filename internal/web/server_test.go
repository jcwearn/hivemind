package web

import (
	"bufio"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func testServer(t *testing.T) (srv *Server, ts *httptest.Server) {
	t.Helper()

	srv = New(Options{
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		BaseURL:      "http://example.test",
		CookieSecret: []byte("test-secret-not-used-anywhere-real"),
	})

	ts = httptest.NewServer(srv.Routes())
	t.Cleanup(func() {
		srv.Shutdown()
		ts.Close()
	})
	return srv, ts
}

// client returns an http.Client with its own cookie jar, which is what makes it
// a distinct "phone" for the purposes of these tests.
func client(t *testing.T, ts *httptest.Server) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar: %v", err)
	}
	return &http.Client{Jar: jar, Timeout: 5 * time.Second}
}

// hostRoom creates a room and returns its code.
func hostRoom(t *testing.T, ts *httptest.Server) string {
	t.Helper()

	c := client(t, ts)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := c.PostForm(ts.URL+"/rooms", nil)
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("create room status = %d, want 303", resp.StatusCode)
	}
	loc := resp.Header.Get("Location")
	code := strings.TrimSuffix(strings.TrimPrefix(loc, "/r/"), "/screen")
	if code == "" || code == loc {
		t.Fatalf("could not read a room code out of %q", loc)
	}
	return code
}

// join puts a named player in a room and returns the client holding their seat.
func join(t *testing.T, ts *httptest.Server, code, name string) *http.Client {
	t.Helper()

	c := client(t, ts)
	resp, err := c.PostForm(ts.URL+"/j/"+code, url.Values{"name": {name}})
	if err != nil {
		t.Fatalf("join as %s: %v", name, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("join as %s: status %d", name, resp.StatusCode)
	}
	return c
}

// readFrames streams SSE from url and sends each complete frame's payload on
// the returned channel. It stops when the response ends.
//
// This is the whole reason the transport is SSE rather than a WebSocket: the
// test-side decoder is a bufio.Scanner.
func readFrames(t *testing.T, c *http.Client, streamURL string) (frames <-chan string, stop func()) {
	t.Helper()

	req, err := http.NewRequest(http.MethodGet, streamURL, http.NoBody)
	if err != nil {
		t.Fatalf("stream request: %v", err)
	}

	// A dedicated client: the shared one has a Timeout, which would cut a
	// long-lived stream off mid-test.
	streamClient := &http.Client{Jar: c.Jar}
	// bodyclose cannot see through the goroutine below; the body is closed by
	// its defer, and again by the returned stop.
	resp, err := streamClient.Do(req) //nolint:bodyclose // closed by the reader goroutine and by stop

	if err != nil {
		t.Fatalf("open stream: %v", err)
	}

	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		_ = resp.Body.Close()
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}

	out := make(chan string, 64)
	go func() {
		defer close(out)
		defer func() { _ = resp.Body.Close() }()

		scanner := bufio.NewScanner(resp.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

		var payload strings.Builder
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "data: "):
				if payload.Len() > 0 {
					payload.WriteByte('\n')
				}
				payload.WriteString(strings.TrimPrefix(line, "data: "))
			case line == "":
				if payload.Len() > 0 {
					select {
					case out <- payload.String():
					default:
					}
					payload.Reset()
				}
			}
		}
	}()

	return out, func() { _ = resp.Body.Close() }
}

func TestHomePageRenders(t *testing.T) {
	_, ts := testServer(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("get home: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	for _, want := range []string{"HIVEMIND", `action="/rooms"`, `action="/join"`} {
		if !strings.Contains(string(body), want) {
			t.Errorf("home page missing %q", want)
		}
	}
}

func TestHealthReportsRoomCount(t *testing.T) {
	_, ts := testServer(t)

	hostRoom(t, ts)
	hostRoom(t, ts)

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), `"rooms":2`) {
		t.Errorf("healthz = %s, want a count of 2 rooms", body)
	}
}

// Health must not stay green through a shutdown, or a load balancer will keep
// sending players to a process that is on its way out.
func TestHealthGoesUnavailableOnShutdown(t *testing.T) {
	srv, ts := testServer(t)

	srv.Shutdown()

	resp, err := http.Get(ts.URL + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 during shutdown", resp.StatusCode)
	}
}

func TestUnknownRoomIsNotFound(t *testing.T) {
	_, ts := testServer(t)

	for _, path := range []string{"/j/ZZZZ", "/r/ZZZZ/screen", "/r/ZZZZ/play"} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("get %s: %v", path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s status = %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestScreenPageCarriesJoinCodeAndQR(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)

	resp, err := http.Get(ts.URL + "/r/" + code + "/screen")
	if err != nil {
		t.Fatalf("get screen: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(resp.Body)
	got := string(body)

	if !strings.Contains(got, code) {
		t.Error("screen page does not show the room code")
	}
	if !strings.Contains(got, `sse-connect="/r/`+code+`/screen/events"`) {
		t.Error("screen page does not connect to its event stream")
	}
	// The QR encodes the public base URL, not the test server's address.
	if !strings.Contains(got, "example.test/j/"+code) {
		t.Error("screen page does not show the public join URL")
	}
	if !strings.Contains(got, "<path d=") {
		t.Error("screen page has no QR path")
	}
}

// The controller is not reachable without a seat, because a controller that
// steers nothing looks broken rather than empty.
func TestPlayWithoutJoiningRedirectsToJoin(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)

	c := client(t, ts)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := c.Get(ts.URL + "/r/" + code + "/play")
	if err != nil {
		t.Fatalf("get play: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/j/"+code {
		t.Errorf("Location = %q, want /j/%s", got, code)
	}
}

func TestJoinRequiresAName(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)

	c := client(t, ts)
	resp, err := c.PostForm(ts.URL+"/j/"+code, url.Values{"name": {"   "}})
	if err != nil {
		t.Fatalf("join: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a blank name", resp.StatusCode)
	}
}

// The seat is the point of the signed cookie: a phone that locks its screen and
// comes back has to land where it left off.
func TestReturningPhoneKeepsItsSeat(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)

	c := join(t, ts, code, "ana")

	// Same jar, same cookie -- as if the page were reloaded.
	resp, err := c.Get(ts.URL + "/r/" + code + "/play")
	if err != nil {
		t.Fatalf("reload controller: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 -- the seat was lost", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "ana") {
		t.Error("controller does not show the returning player's name")
	}
}

// A forged cookie must not be able to vote as somebody else.
func TestForgedCookieIsRejected(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)
	join(t, ts, code, "ana")

	c := client(t, ts)
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/r/"+code+"/vote", strings.NewReader("dir=up"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "someone-elses-id.not-a-real-signature"})

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("vote: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The forged identity is discarded and a fresh one minted, which is not a
	// member of the room -- so htmx is told to send them to the join page.
	if got := resp.Header.Get("HX-Redirect"); got != "/j/"+code {
		t.Errorf("HX-Redirect = %q, want /j/%s -- a forged cookie must not vote", got, code)
	}
}

func TestVoteRejectsUnknownDirection(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)
	c := join(t, ts, code, "ana")

	resp, err := c.PostForm(ts.URL+"/r/"+code+"/vote", url.Values{"dir": {"sideways"}})
	if err != nil {
		t.Fatalf("vote: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

// The end-to-end path this whole program exists to serve: two phones join, both
// vote, and the shared screen shows the result.
func TestTwoPlayersVoteAndTheScreenAgrees(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)

	ana := join(t, ts, code, "ana")
	sam := join(t, ts, code, "sam")

	frames, stop := readFrames(t, client(t, ts), ts.URL+"/r/"+code+"/screen/events")
	defer stop()

	// The first frame is sent on subscribe, before any tick.
	first := waitForFrame(t, frames, func(f string) bool { return strings.Contains(f, "ana") })
	if !strings.Contains(first, "sam") {
		t.Error("roster does not list both players")
	}

	for _, c := range []*http.Client{ana, sam} {
		resp, err := c.PostForm(ts.URL+"/r/"+code+"/vote", url.Values{"dir": {"up"}})
		if err != nil {
			t.Fatalf("vote: %v", err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			t.Fatalf("vote status = %d, want 204", resp.StatusCode)
		}
	}

	// Two votes for up means two lit pips in the tally.
	got := waitForFrame(t, frames, func(f string) bool {
		return strings.Count(f, `class="pip on"`) == 2
	})
	if got == "" {
		t.Fatal("no frame showed both votes")
	}
}

// Starting a round has to be visible on the screen, because the screen is the
// only place the host is looking.
func TestStartBeginsARound(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)
	join(t, ts, code, "ana")

	frames, stop := readFrames(t, client(t, ts), ts.URL+"/r/"+code+"/screen/events")
	defer stop()

	waitForFrame(t, frames, func(f string) bool { return strings.Contains(f, "READY WHEN YOU ARE") })

	resp, err := http.Post(ts.URL+"/r/"+code+"/start", "", nil)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = resp.Body.Close()

	waitForFrame(t, frames, func(f string) bool { return strings.Contains(f, "GET READY") })

	// And then the countdown clears and play begins.
	waitForFrame(t, frames, func(f string) bool {
		return !strings.Contains(f, "GET READY") && strings.Contains(f, `class="cell head"`)
	})
}

// Every phone in a room receives the same bytes, which is what makes one render
// per tick enough no matter how many people are connected.
func TestAllPlayStreamsReceiveIdenticalFrames(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)
	join(t, ts, code, "ana")
	join(t, ts, code, "sam")

	a, stopA := readFrames(t, client(t, ts), ts.URL+"/r/"+code+"/play/events")
	defer stopA()
	b, stopB := readFrames(t, client(t, ts), ts.URL+"/r/"+code+"/play/events")
	defer stopB()

	fa := waitForFrame(t, a, func(string) bool { return true })
	fb := waitForFrame(t, b, func(string) bool { return true })

	if fa != fb {
		t.Errorf("play frames differ between phones:\n a=%q\n b=%q", fa, fb)
	}
}

// A frame has to survive the newline-per-data-line encoding intact. If this
// breaks, pages render as a truncated fragment and mostly look fine, which is
// the worst way for it to fail.
func TestFramesSurviveSSEEncoding(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)
	join(t, ts, code, "ana")

	frames, stop := readFrames(t, client(t, ts), ts.URL+"/r/"+code+"/screen/events")
	defer stop()

	frame := waitForFrame(t, frames, func(f string) bool { return strings.Contains(f, "roster") })

	// The fragment must be complete: opening hud through closing roster.
	if !strings.HasPrefix(frame, `<div class="hud">`) {
		t.Errorf("frame does not start with the hud: %.80q", frame)
	}
	if !strings.HasSuffix(strings.TrimSpace(frame), "</ul>") {
		t.Errorf("frame is truncated, does not end with </ul>: %.80q", frame[max(0, len(frame)-80):])
	}
	if strings.Contains(frame, "data: ") {
		t.Error("frame contains an un-decoded SSE prefix")
	}
}

func TestStreamEndsWhenServerShutsDown(t *testing.T) {
	srv, ts := testServer(t)
	code := hostRoom(t, ts)

	frames, stop := readFrames(t, client(t, ts), ts.URL+"/r/"+code+"/screen/events")
	defer stop()

	waitForFrame(t, frames, func(string) bool { return true })

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.Shutdown()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown blocked with a stream open")
	}

	// The frame channel closes when the response body ends.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-frames:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("stream did not end after shutdown")
		}
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	_, ts := testServer(t)

	tests := []struct {
		path      string
		wantCache string
	}{
		{"/static/styles.css", "no-cache"},
		{"/static/app.js", "no-cache"},
		{"/static/vendor/htmx-2.0.10.min.js", "public, max-age=31536000, immutable"},
		{"/static/vendor/htmx-ext-sse-2.2.4.js", "public, max-age=31536000, immutable"},
	}

	for _, tt := range tests {
		resp, err := http.Get(ts.URL + tt.path)
		if err != nil {
			t.Fatalf("get %s: %v", tt.path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d, want 200", tt.path, resp.StatusCode)
			continue
		}
		if len(body) == 0 {
			t.Errorf("%s is empty", tt.path)
		}
		if got := resp.Header.Get("Cache-Control"); got != tt.wantCache {
			t.Errorf("%s Cache-Control = %q, want %q", tt.path, got, tt.wantCache)
		}
	}
}

func TestJoinByCodeIsCaseInsensitive(t *testing.T) {
	_, ts := testServer(t)
	code := hostRoom(t, ts)

	c := client(t, ts)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, err := c.PostForm(ts.URL+"/join", url.Values{"code": {strings.ToLower(code)}})
	if err != nil {
		t.Fatalf("join by code: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/j/"+code {
		t.Errorf("Location = %q, want /j/%s", got, code)
	}
}

// waitForFrame reads until a frame satisfies match, or the test times out.
func waitForFrame(t *testing.T, frames <-chan string, match func(string) bool) string {
	t.Helper()

	deadline := time.After(10 * time.Second)
	for {
		select {
		case f, ok := <-frames:
			if !ok {
				t.Fatal("stream closed before a matching frame arrived")
			}
			if match(f) {
				return f
			}
		case <-deadline:
			t.Fatal("timed out waiting for a matching frame")
		}
	}
}
