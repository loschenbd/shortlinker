package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Phone pages: a Markdown or code file can get a page phones open instead of
// plain text, such as its SilverBullet page from sb-link, where it can be
// edited. The hub makes them, not
// sl-link. sb-link reads SilverBullet's admin password from the login
// Keychain, and macOS lets only processes started by the desktop login read
// it. The hub is a LaunchAgent, so it can. An agent running in a terminal
// multiplexer that was started over SSH (as Moshi Desktop and phones start
// one) can't. Agents can ask the hub for a page; they never see the
// password.

// phonePageExts are the files that get phone pages: Markdown, and the code and
// text files sb-link opens in SilverBullet's editor, except .html (keep this
// list in step with sb-link's). A phone opening an HTML link
// wants the page rendered, which /f/ does. Other files, such as PDFs and
// images, are fine as /f/ serves them.
var phonePageExts = map[string]bool{"md": true}

func init() {
	for _, ext := range strings.Fields("go py js jsx ts tsx sh bash zsh fish swift rs java c cpp cxx cs " +
		"dart scala r lua sql css xml json yaml yml toml proto nix tcl tex diff txt") {
		phonePageExts[ext] = true
	}
}

// getsPhonePage says whether a file of this name gets a phone page.
func getsPhonePage(path string) bool {
	return phonePageExts[strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))]
}

// phonePageTimeout bounds one run of the phone-page command. If the
// Keychain is locked, a password prompt can wait on the Mac's screen with
// nobody there; the link then works without a page.
var phonePageTimeout = 30 * time.Second

// defaultPhoneCmd is the hub's phone-page command: SHORTLINKER_PHONE_CMD,
// else sb-link.
func defaultPhoneCmd() string {
	if c := os.Getenv("SHORTLINKER_PHONE_CMD"); c != "" {
		return c
	}
	return "sb-link"
}

// phoneBin finds the phone-page command, or "" if this Mac has none. A
// launchd job's PATH is /usr/bin:/bin:/usr/sbin:/sbin, so the usual tool
// folders are searched too; sb-link installs itself in ~/.local/bin.
func phoneBin(command, home string) string {
	if strings.Contains(command, "/") {
		if isExecutable(command) {
			return command
		}
		return ""
	}
	if bin, err := exec.LookPath(command); err == nil {
		return bin
	}
	for _, dir := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		if bin := filepath.Join(dir, command); isExecutable(bin) {
			return bin
		}
	}
	return ""
}

func isExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Mode().IsRegular() && fi.Mode()&0o111 != 0
}

// phonePageLabel names a command's pages in sl-link's notes. sb-link's
// keep the words people and agents already watch for.
func phonePageLabel(command string) string {
	if filepath.Base(command) == "sb-link" {
		return "SilverBullet page"
	}
	return "phone page"
}

// makePhonePage runs the phone-page command on a Markdown or code file and returns
// the URL it prints last. The contract (sb-link's): exit 0 and print a URL
// when there's a page; exit non-zero with the reason on stderr when there
// isn't. Only exit 0 counts: sb-link's exit 4 prints a URL too, but
// SilverBullet was down, so the page may not exist yet. "" with no error
// means this Mac has no such command, so there's nothing to say.
func (s *Server) makePhonePage(ctx context.Context, abs string) (string, error) {
	bin := phoneBin(s.phoneCmd, s.home)
	if bin == "" {
		return "", nil
	}
	// One at a time: two sb-links for files in one new project would both
	// create its SilverBullet space.
	s.phoneMu.Lock()
	defer s.phoneMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, phonePageTimeout)
	defer cancel()

	name := filepath.Base(bin)
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, abs)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// A process group of its own, so a timeout also ends what the command
	// started, such as a `security` call waiting on a Keychain prompt.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = time.Second
	err := cmd.Run()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("%s took longer than %s, so it was stopped (is a Keychain prompt waiting on the Mac's screen?)", name, phonePageTimeout)
	}
	if err != nil {
		reason := commandReason(name, stderr.String())
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitCode() == 2 && reason != "":
			return "", fmt.Errorf("%s: %s", name, reason) // a file it doesn't serve
		case reason == "":
			return "", fmt.Errorf("%s: %v", name, err)
		default:
			return "", fmt.Errorf("%s (%v): %s", name, err, reason)
		}
	}
	lines := strings.Fields(strings.TrimSpace(stdout.String()))
	if len(lines) == 0 || !isWebURL(lines[len(lines)-1]) {
		return "", fmt.Errorf("%s printed no URL", name)
	}
	return lines[len(lines)-1], nil
}

// phonePageReply is what POST /api/links/<slug>/phone-page answers.
type phonePageReply struct {
	Slug   string `json:"slug"`
	Phone  string `json:"phone,omitempty"`  // the page, when there is one
	Reason string `json:"reason,omitempty"` // why the command made none
	Label  string `json:"label"`            // what to call such a page
}

// handlePhonePage makes a Markdown link's phone page and saves it with the
// link. A page the command couldn't make is still a 200, with its reason:
// the link works either way. A link that already has a page keeps it.
func (s *Server) handlePhonePage(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("slug")
	link, ok, err := s.store.Lookup(slug)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if !ok || IsRedirect(link.Target) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no file link " + slug})
		return
	}
	reply := phonePageReply{Slug: slug, Phone: link.Phone, Label: phonePageLabel(s.phoneCmd)}
	if link.Phone != "" {
		writeJSON(w, http.StatusOK, reply)
		return
	}
	full := ExpandPath(link.Target, s.home)
	if fi, err := os.Stat(full); err != nil || !fi.Mode().IsRegular() || !filepath.IsAbs(full) || !getsPhonePage(full) {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "only a Markdown or code file that exists gets a phone page"})
		return
	}
	url, err := s.makePhonePage(r.Context(), full)
	if err != nil {
		reply.Reason = err.Error()
		writeJSON(w, http.StatusOK, reply)
		return
	}
	if url != "" {
		if err := s.store.SetPhone(slug, url); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	reply.Phone = url
	writeJSON(w, http.StatusOK, reply)
}

// postPhonePage asks the hub to make a link's phone page.
func postPhonePage(a api, slug string) (phonePageReply, error) {
	var reply phonePageReply
	err := a.do(http.MethodPost, linkPath(slug, "phone-page"), struct{}{}, &reply)
	return reply, err
}
