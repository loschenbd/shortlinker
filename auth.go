package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The local port's API needs the hub's token. Binding to 127.0.0.1 keeps
// the tailnet out, but not other accounts on the same Mac, or any process
// at all: loopback is shared. Without the token, any of them could register
// a path (say ~/.ssh/id_ed25519) and read it back through /f/.
//
// The token lives in ~/.config/shortlinker/token, readable by you only.
// sl-link and the other commands send it as "Authorization: Bearer <token>".
// The manage page can't read that file, so `shortlinker manage` asks for a
// one-time code with the token and opens the page with it; the page trades
// the code for a session that expires.

// configDir holds the token, the links files and the saved hub address.
func configDir(home string) string { return filepath.Join(home, ".config", "shortlinker") }

func tokenPath(home string) string { return filepath.Join(configDir(home), "token") }

// loadOrCreateToken returns the hub's token, making one on first run.
func loadOrCreateToken(path string) (string, error) {
	if data, err := os.ReadFile(path); err == nil {
		if t := strings.TrimSpace(string(data)); t != "" {
			if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
				if err := os.Chmod(path, 0o600); err != nil {
					return "", err
				}
			}
			return t, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	t := randomHex(32)
	if err := os.WriteFile(path, []byte(t+"\n"), 0o600); err != nil {
		return "", err
	}
	return t, nil
}

// errNoToken means this Mac has no token: it isn't the hub, or the hub has
// never started.
var errNoToken = errors.New("no hub token")

// readToken is the token the commands send: SHORTLINKER_TOKEN, else the file.
func readToken(home string) (string, error) {
	if t := os.Getenv("SHORTLINKER_TOKEN"); t != "" {
		return t, nil
	}
	data, err := os.ReadFile(tokenPath(home))
	if errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("%w at %s: run this on the hub Mac (the hub writes it when it starts)", errNoToken, tokenPath(home))
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on macOS
	}
	return hex.EncodeToString(b)
}

const (
	codeTTL    = 2 * time.Minute
	sessionTTL = 12 * time.Hour
)

// hubProof is what the hub answers to a command's nonce before the command
// sends the token (see handleHubProof): only something that knows the token
// can work it out. The label keeps it from being any other MAC the hub makes.
func hubProof(token, nonce string) string {
	h := hmac.New(sha256.New, []byte(token))
	h.Write([]byte("shortlinker hub proof\x00" + nonce))
	return hex.EncodeToString(h.Sum(nil))
}

func isNonce(n string) bool {
	if len(n) < 32 || len(n) > 64 {
		return false
	}
	_, err := hex.DecodeString(n)
	return err == nil
}

// apiAuth checks the local API's credentials: the token itself, or a
// session the manage page got by trading in a one-time code.
type apiAuth struct {
	token string
	now   func() time.Time

	mu    sync.Mutex
	codes map[string]time.Time // one-time code → when it expires
}

func newAPIAuth(token string) *apiAuth {
	return &apiAuth{token: token, now: time.Now, codes: map[string]time.Time{}}
}

// newCode returns a one-time code that opens the manage page.
func (a *apiAuth) newCode() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := a.now()
	for c, exp := range a.codes {
		if now.After(exp) {
			delete(a.codes, c)
		}
	}
	c := randomHex(16)
	a.codes[c] = now.Add(codeTTL)
	return c
}

// redeem trades a code for a session, once.
func (a *apiAuth) redeem(code string) (session string, expires time.Time, ok bool) {
	a.mu.Lock()
	exp, found := a.codes[code]
	delete(a.codes, code)
	a.mu.Unlock()
	now := a.now()
	if !found || now.After(exp) {
		return "", time.Time{}, false
	}
	expires = now.Add(sessionTTL)
	return a.session(expires), expires, true
}

// session is "s1.<expiry>.<hmac>": the hub can check one without keeping
// any, and they all stop working when the token changes.
func (a *apiAuth) session(expires time.Time) string {
	unix := strconv.FormatInt(expires.Unix(), 10)
	return "s1." + unix + "." + a.mac(unix)
}

func (a *apiAuth) mac(unix string) string {
	h := hmac.New(sha256.New, []byte(a.token))
	h.Write([]byte("shortlinker session " + unix))
	return hex.EncodeToString(h.Sum(nil))
}

// valid reports whether a bearer credential is the token or a live session.
func (a *apiAuth) valid(cred string) bool {
	if a == nil || a.token == "" || cred == "" {
		return false
	}
	if subtle.ConstantTimeCompare([]byte(cred), []byte(a.token)) == 1 {
		return true
	}
	parts := strings.Split(cred, ".")
	if len(parts) != 3 || parts[0] != "s1" {
		return false
	}
	unix, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || a.now().After(time.Unix(unix, 0)) {
		return false
	}
	return hmac.Equal([]byte(parts[2]), []byte(a.mac(parts[1])))
}

// isToken reports whether cred is the token itself, not a session.
func (a *apiAuth) isToken(cred string) bool {
	return a != nil && a.token != "" && subtle.ConstantTimeCompare([]byte(cred), []byte(a.token)) == 1
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// fromOtherSite reports whether a web page on another origin sent the
// request. The manage page itself is same-origin; curl and the commands
// send no Sec-Fetch-Site at all. Pages served under /f/ on the public port
// count as another site: a different port is a different origin.
func fromOtherSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return true
	}
	return false
}

// requireAuth guards the local API: no other site's pages, and a valid
// token or session.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if fromOtherSite(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "links can't be changed from a web page"})
			return
		}
		if !s.auth.valid(bearer(r)) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{
				"error": "this needs the hub's token: send Authorization: Bearer $(cat ~/.config/shortlinker/token)",
			})
			return
		}
		next(w, r)
	}
}
