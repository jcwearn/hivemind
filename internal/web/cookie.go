package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/jcwearn/hivemind/internal/lobby"
)

// cookieName holds the player identity. It is scoped to the whole site rather
// than to a room path, so one phone keeps one identity across however many
// rooms it visits in an evening.
const cookieName = "hm_player"

// cookieMaxAge is generous on purpose: it only has to outlast a party.
const cookieMaxAge = 12 * 60 * 60

// cookieCodec mints and verifies the signed player identity.
//
// There are no accounts here and nothing worth stealing -- the identity's only
// power is to reclaim a seat in a game of snake. The signature is not protecting
// a secret, it is stopping somebody from typing another player's id into their
// own cookie jar and voting as them, which is a real thing a teenager at a party
// will try within about four minutes.
type cookieCodec struct {
	secret []byte
	secure bool
}

func newCookieCodec(secret []byte, secure bool) *cookieCodec {
	return &cookieCodec{secret: secret, secure: secure}
}

// playerID returns the identity carried by this request, minting and setting a
// new one if there is not a valid one already.
//
// It writes the cookie as a side effect, so it must be called before anything
// writes a response body.
func (c *cookieCodec) playerID(w http.ResponseWriter, r *http.Request) lobby.PlayerID {
	if ck, err := r.Cookie(cookieName); err == nil {
		if id, ok := c.verify(ck.Value); ok {
			return id
		}
	}

	id := lobby.PlayerID(randomID())
	http.SetCookie(w, &http.Cookie{
		Name:  cookieName,
		Value: c.sign(id),
		Path:  "/",
		// A room link is opened from a QR scan or a message from a friend, so
		// the cookie has to survive a cross-site navigation. Lax does that and
		// still refuses to ride along on a cross-site POST.
		SameSite: http.SameSiteLaxMode,
		HttpOnly: true,
		Secure:   c.secure,
		MaxAge:   cookieMaxAge,
	})
	return id
}

func (c *cookieCodec) sign(id lobby.PlayerID) string {
	mac := hmac.New(sha256.New, c.secret)
	mac.Write([]byte(id))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return string(id) + "." + sig
}

func (c *cookieCodec) verify(value string) (lobby.PlayerID, bool) {
	id, sig, ok := strings.Cut(value, ".")
	if !ok || id == "" {
		return "", false
	}

	want := hmac.New(sha256.New, c.secret)
	want.Write([]byte(id))
	expected := base64.RawURLEncoding.EncodeToString(want.Sum(nil))

	// Constant time, because comparing MACs with == is the mistake this whole
	// package exists to not make.
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return "", false
	}
	return lobby.PlayerID(id), true
}

func randomID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand does not fail on any platform this runs on, and there is
		// no sensible degraded mode for "cannot identify players".
		panic("web: crypto/rand unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(buf)
}
