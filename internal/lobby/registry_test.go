package lobby

import (
	"strings"
	"testing"
	"time"
)

func TestCreateAllocatesDistinctCodes(t *testing.T) {
	reg := NewRegistry(testOptions())
	t.Cleanup(reg.Close)

	seen := make(map[string]bool)
	for range 50 {
		room, err := reg.Create()
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if seen[room.Code] {
			t.Fatalf("duplicate room code %q", room.Code)
		}
		seen[room.Code] = true

		if len(room.Code) != codeLength {
			t.Errorf("code %q has length %d, want %d", room.Code, len(room.Code), codeLength)
		}
		for _, c := range room.Code {
			if !strings.ContainsRune(codeAlphabet, c) {
				t.Errorf("code %q contains %q, which is outside the alphabet", room.Code, c)
			}
		}
	}

	if got := reg.Count(); got != 50 {
		t.Errorf("Count = %d, want 50", got)
	}
}

// Codes are read off a TV and typed on a phone, so the alphabet must not
// contain the characters that get confused doing either.
func TestCodeAlphabetExcludesAmbiguousCharacters(t *testing.T) {
	for _, c := range "ILOU01" {
		if strings.ContainsRune(codeAlphabet, c) {
			t.Errorf("alphabet contains ambiguous character %q", c)
		}
	}
}

func TestGetIsCaseAndSpaceInsensitive(t *testing.T) {
	reg := NewRegistry(testOptions())
	t.Cleanup(reg.Close)

	room, err := reg.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for _, typed := range []string{
		room.Code,
		strings.ToLower(room.Code),
		"  " + strings.ToLower(room.Code) + "  ",
	} {
		got, ok := reg.Get(typed)
		if !ok {
			t.Errorf("Get(%q) did not find the room", typed)
			continue
		}
		if got != room {
			t.Errorf("Get(%q) returned a different room", typed)
		}
	}

	if _, ok := reg.Get("ZZZZ"); ok {
		t.Error("Get found a room that was never created")
	}
}

// A room removes itself from the registry as its goroutine exits, so a
// collected room stops being findable at the same instant it stops being able
// to answer. If those two moments came apart, a player could be handed a room
// that immediately fails every call.
func TestClosedRoomIsRemovedFromRegistry(t *testing.T) {
	reg := NewRegistry(testOptions())
	t.Cleanup(reg.Close)

	room, err := reg.Create()
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	code := room.Code

	room.close()

	deadline := time.After(2 * time.Second)
	for {
		if _, ok := reg.Get(code); !ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("closed room is still in the registry")
		case <-time.After(5 * time.Millisecond):
		}
	}

	if got := reg.Count(); got != 0 {
		t.Errorf("Count = %d, want 0", got)
	}
}

// Registry.Close snapshots rooms under the lock and closes them outside it,
// because each room's exit path takes the same lock to remove itself. Holding
// it across the close would deadlock on the first room, so this test would hang
// rather than fail.
func TestCloseStopsEveryRoom(t *testing.T) {
	reg := NewRegistry(testOptions())

	rooms := make([]*Room, 0, 8)
	for range 8 {
		room, err := reg.Create()
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		rooms = append(rooms, room)
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		reg.Close()
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Registry.Close deadlocked")
	}

	for _, r := range rooms {
		select {
		case <-r.Done():
		default:
			t.Errorf("room %s is still running after Close", r.Code)
		}
	}
	if got := reg.Count(); got != 0 {
		t.Errorf("Count = %d, want 0", got)
	}
}

func TestNormalizeCode(t *testing.T) {
	tests := []struct{ in, want string }{
		{"abcd", "ABCD"},
		{" abcd ", "ABCD"},
		{"AbCd", "ABCD"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeCode(tt.in); got != tt.want {
			t.Errorf("NormalizeCode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
