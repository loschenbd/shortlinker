package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTokenIsMadeOncePrivately(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cfg", "token")
	first, err := loadOrCreateToken(path)
	if err != nil || len(first) != 64 {
		t.Fatalf("token = %q, %v", first, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("token mode = %v, want 0600", fi.Mode().Perm())
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("folder mode = %v, want 0700", fi.Mode().Perm())
	}
	os.Chmod(path, 0o644)
	again, err := loadOrCreateToken(path)
	if err != nil || again != first {
		t.Errorf("second load = %q, %v; want the same token", again, err)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("a readable token wasn't made private again: %v", fi.Mode().Perm())
	}
}

func TestReadTokenSaysWhereItLooked(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SHORTLINKER_TOKEN", "")
	if _, err := readToken(home); err == nil || !strings.Contains(err.Error(), "run this on the hub Mac") {
		t.Errorf("err = %v", err)
	}
	t.Setenv("SHORTLINKER_TOKEN", "from-env")
	if tok, _ := readToken(home); tok != "from-env" {
		t.Errorf("token = %q, want from-env", tok)
	}
}

func TestSessionsExpireAndDieWithTheToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := newAPIAuth("secret")
	a.now = func() time.Time { return now }
	code := a.newCode()
	session, expires, ok := a.redeem(code)
	if !ok || !expires.Equal(now.Add(sessionTTL)) || !a.valid(session) {
		t.Fatalf("redeem = %q, %v, %v", session, expires, ok)
	}
	if a.isToken(session) || !a.isToken("secret") {
		t.Error("isToken mixes up the token and a session")
	}
	for _, forged := range []string{
		"s1." + strings.Split(session, ".")[1] + ".00",
		"s1.9999999999." + strings.Split(session, ".")[2], // a later expiry, same mac
		"secret ", "", "s1..",
	} {
		if a.valid(forged) {
			t.Errorf("valid(%q) = true", forged)
		}
	}
	if newAPIAuth("rotated").valid(session) {
		t.Error("a session survived a new token")
	}
	now = now.Add(sessionTTL + time.Second)
	if a.valid(session) {
		t.Error("an expired session still works")
	}
	late := a.newCode()
	now = now.Add(codeTTL + time.Second)
	if _, _, ok := a.redeem(late); ok {
		t.Error("an expired code still works")
	}
}
