package main

import (
	"bytes"
	"crypto/hmac"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Exit codes for `sl-link` and the other commands, matching sb-link's where
// they overlap.
const (
	exitOK          = 0
	exitError       = 1
	exitNoSuchPath  = 2
	exitUnreachable = 4
)

// defaultLocalPort is the hub's loopback-only port, which holds the API.
const defaultLocalPort = "7778"

const linkUsage = `usage: sl-link [options] <path>...   (or: shortlinker link ...)

Prints one link per file or folder. The link opens the file on whichever Mac
you click it on, in any app that opens web links, and shows it on a phone.
Linking the same path again gives the same link. Run it on the hub Mac.

  --finder      a Mac shows the file in Finder instead of opening it
  --selection   link what's selected in Finder (instead of paths)
  --copy        also copy the links to the clipboard
  --markdown    print (and copy) [name](link) instead of the bare link
  --notify      say what happened in a notification (for menu bar use)
  --note TEXT   save why the link was made; shows in the manage page

The link records which agent session made it, from SHORTLINKER_SESSION or
Claude Code's CLAUDE_CODE_SESSION_ID, so the manage page can lead back to it.

A Markdown or code file can get a page phones open instead of plain text,
where it can be edited: sl-link asks the hub to run sb-link (a SilverBullet
linker) once, and the hub saves the URL it prints. The hub runs it because
sb-link needs the login Keychain, which a session started over SSH can't
use. Without sb-link, nothing changes.

Exit codes: 0 ok; 1 other error; 2 no such file or folder; 4 hub unreachable.
The hub's address comes from SHORTLINKER_URL, else ~/.config/shortlinker/url,
which the first run fills in from this Mac's tailnet name.
`

// errUnreachable marks failures to reach the hub at all.
var errUnreachable = errors.New("hub unreachable")

// errNoAPI means the address answered but isn't a hub (an opener agent).
var errNoAPI = errors.New("this Mac isn't the shortlinker hub, so it can't make links")

// errNotHub means whatever answers on the API's port couldn't prove it's the
// hub, so nothing was sent to it.
var errNotHub = errors.New("what answers on the API's port couldn't prove it's the hub (it doesn't know the token in ~/.config/shortlinker/token), so the token wasn't sent; if the token changed, restart the hub")

// linker registers a target with the hub and returns its slug and its
// phone page, if it has one.
type linker func(target string) (slug, phone string, err error)

// linkOptions are what linkPaths needs; tests swap in fakes.
type linkOptions struct {
	home, hubURL string
	finder       bool // add ?finder: a Mac shows the file in Finder
	markdown     bool // print [name](link)
	register     linker
	// phonePage asks the hub to make and save the page phones should get
	// for a Markdown link (its SilverBullet page). The reply says why
	// there's none; an empty reply means no page and nothing to say.
	phonePage func(slug string) (phonePageReply, error)
}

// api talks to the hub's local API with its token. Before the first
// request it checks that the hub is what's listening (see prove).
type api struct {
	base, token string
	client      *http.Client
	proof       *proofOnce // shared by copies of this api; nil checks before every request
}

type proofOnce struct {
	once sync.Once
	err  error
}

// localAPI is the hub's API on this Mac: SHORTLINKER_API, else
// http://127.0.0.1:7778, with the token from ~/.config/shortlinker/token.
func localAPI(home string, timeout time.Duration) (api, error) {
	base := os.Getenv("SHORTLINKER_API")
	if base == "" {
		base = "http://127.0.0.1:" + defaultLocalPort
	}
	token, err := readToken(home)
	if err != nil {
		return api{}, err
	}
	return api{base: strings.TrimRight(base, "/"), token: token, client: &http.Client{Timeout: timeout}, proof: &proofOnce{}}, nil
}

// prove checks that what answers on the API's port knows the token,
// without sending it. Anything on this Mac can bind 127.0.0.1:7778 while
// the hub is down (another account's process, say); without this check,
// every command, the menu bar plugin included, would hand it the token.
func (a api) prove() error {
	if a.proof == nil {
		return a.challenge()
	}
	a.proof.once.Do(func() { a.proof.err = a.challenge() })
	return a.proof.err
}

func (a api) challenge() error {
	nonce := randomHex(16)
	resp, err := a.client.Get(a.base + "/api/hub-proof?n=" + nonce)
	if err != nil {
		return fmt.Errorf("%w (is it running? ~/Library/Logs/shortlinker.log): %v", errUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return errNoAPI // an opener agent, which has no API
	}
	var body struct {
		Proof string `json:"proof"`
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK || json.Unmarshal(raw, &body) != nil ||
		!hmac.Equal([]byte(body.Proof), []byte(hubProof(a.token, nonce))) {
		return errNotHub
	}
	return nil
}

// do sends a request (with a JSON body if body isn't nil) and decodes the
// JSON reply into result.
func (a api) do(method, path string, body, result any) error {
	if err := a.prove(); err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w (is it running? ~/Library/Logs/shortlinker.log): %v", errUnreachable, err)
	}
	defer resp.Body.Close()
	var reply struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if json.Unmarshal(raw, &reply) != nil || reply.Error == "" {
			// The hub always explains itself in JSON; a bare 404 or 405 is
			// an opener agent, which has no API.
			if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
				return errNoAPI
			}
			reply.Error = resp.Status
		}
		return fmt.Errorf("hub refused: %s", reply.Error)
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("hub sent an unreadable reply: %v", err)
	}
	return nil
}

// runLink is the `sl-link` / `shortlinker link` entry point.
func runLink(args []string) int {
	var o linkOptions
	var selection, copyOut, notify bool
	var paths []string
	meta := Meta{Origin: "sl-link", Session: agentSession()}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if v, ok := strings.CutPrefix(a, "--note="); ok {
			meta.Note = v
			continue
		}
		switch a {
		case "--note":
			if i+1 == len(args) {
				fmt.Fprintln(os.Stderr, "sl-link: --note needs some text")
				return exitError
			}
			i++
			meta.Note = args[i]
		case "-h", "--help":
			fmt.Fprint(os.Stderr, linkUsage)
			return exitOK
		case "-f", "--finder":
			o.finder = true
		case "--markdown":
			o.markdown = true
		case "--selection":
			selection = true
		case "--copy":
			copyOut = true
		case "--notify":
			notify = true
		default:
			paths = append(paths, a)
		}
	}
	fail := func(code int, err error) int {
		fmt.Fprintln(os.Stderr, "sl-link:", err)
		if notify {
			notifyUser("Couldn't make a link", err.Error())
		}
		return code
	}
	if selection {
		meta.Origin = "finder"
		sel, err := finderSelection()
		if err != nil {
			return fail(exitError, err)
		}
		paths = append(paths, sel...)
	}
	if len(paths) == 0 {
		if selection {
			return fail(exitError, errors.New("nothing is selected in Finder"))
		}
		fmt.Fprint(os.Stderr, linkUsage)
		return exitError
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return fail(exitError, err)
	}
	hubURL, err := publicHubURL(home)
	if err != nil {
		return fail(exitError, err)
	}
	hub, err := localAPI(home, 3*time.Second)
	if err != nil {
		return fail(exitError, err)
	}
	o.home, o.hubURL = home, hubURL
	o.register = func(target string) (string, string, error) { return postLink(hub, target, meta) }
	slow := hub // sb-link can take several seconds
	slow.client = &http.Client{Timeout: phonePageTimeout + 15*time.Second}
	o.phonePage = func(slug string) (phonePageReply, error) { return postPhonePage(slow, slug) }

	var out, errOut bytes.Buffer
	code := linkPaths(paths, o, &out, &errOut)
	os.Stdout.Write(out.Bytes())
	os.Stderr.Write(errOut.Bytes())
	links := strings.TrimSpace(out.String())
	if copyOut && links != "" {
		if err := copyToClipboard(links + "\n"); err != nil {
			return fail(exitError, err)
		}
	}
	if notify {
		n := strings.Count(links, "\n") + 1
		switch {
		case links == "":
			notifyUser("Couldn't make a link", firstLine(errOut.String()))
		case copyOut && n == 1:
			notifyUser("Link copied", nameOf(paths[0]))
		case copyOut:
			notifyUser(fmt.Sprintf("%d links copied", n), "")
		}
	}
	return code
}

// linkPaths links each path and prints its URL, one per line, in order.
func linkPaths(paths []string, o linkOptions, out, errOut io.Writer) int {
	code := exitOK
	for _, p := range paths {
		abs, err := filepath.Abs(ExpandPath(p, o.home))
		if err != nil {
			fmt.Fprintf(errOut, "sl-link: %s: %v\n", p, err)
			code = exitError
			continue
		}
		info, err := os.Stat(abs)
		if err != nil {
			fmt.Fprintf(errOut, "sl-link: %s: no such file or folder\n", p)
			code = maxExit(code, exitNoSuchPath)
			continue
		}
		slug, phone, err := o.register(tildePath(abs, o.home))
		if err != nil {
			fmt.Fprintf(errOut, "sl-link: %s: %v\n", p, err)
			if errors.Is(err, errUnreachable) {
				code = maxExit(code, exitUnreachable)
			} else {
				code = maxExit(code, exitError)
			}
			continue
		}
		if phone == "" && !info.IsDir() && getsPhonePage(abs) && o.phonePage != nil {
			// The link works either way; without a page, phones get plain text.
			reply, err := o.phonePage(slug)
			if err == nil && reply.Reason != "" {
				err = errors.New(reply.Reason)
			}
			if err != nil {
				label := reply.Label
				if label == "" {
					label = "phone page"
				}
				fmt.Fprintf(errOut, "sl-link: %s: no %s, so phones get plain text: %v\n", p, label, err)
			}
		}
		link := strings.TrimRight(o.hubURL, "/") + "/" + slug
		if o.finder {
			link += "?finder"
		}
		if o.markdown {
			link = markdownLink(nameOf(abs), link)
		}
		fmt.Fprintln(out, link)
	}
	return code
}

// commandReason turns a command's stderr ("name: <reason>" lines) into one
// line, without the repeated "name: ".
func commandReason(name, stderr string) string {
	var parts []string
	for _, line := range strings.Split(strings.TrimSpace(stderr), "\n") {
		if line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), name+":")); line != "" {
			parts = append(parts, line)
		}
	}
	return strings.Join(parts, "; ")
}

func maxExit(a, b int) int {
	if b > a {
		return b
	}
	return a
}

// tildePath writes paths under home as ~/..., the way links.txt does.
func tildePath(abs, home string) string {
	if abs == home {
		return "~"
	}
	if rel, ok := strings.CutPrefix(abs, home+string(filepath.Separator)); ok {
		return "~/" + rel
	}
	return abs
}

// nameOf is what a link to path is called in Markdown and notifications.
func nameOf(path string) string {
	return filepath.Base(strings.TrimRight(path, "/"))
}

func markdownLink(name, url string) string {
	name = strings.NewReplacer(`\`, `\\`, `[`, `\[`, `]`, `\]`).Replace(name)
	return "[" + name + "](" + url + ")"
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func postLink(a api, target string, meta Meta) (slug, phone string, err error) {
	var result struct {
		Slug  string `json:"slug"`
		Phone string `json:"phone"`
	}
	body := map[string]string{"target": target, "origin": meta.Origin, "session": meta.Session, "note": meta.Note}
	if err := a.do(http.MethodPost, "/api/links", body, &result); err != nil {
		return "", "", err
	}
	if result.Slug == "" {
		return "", "", fmt.Errorf("hub sent no slug")
	}
	return result.Slug, result.Phone, nil
}

// agentSession is the agent session running sl-link, if it says:
// SHORTLINKER_SESSION, else Claude Code's CLAUDE_CODE_SESSION_ID.
func agentSession() string {
	if s := os.Getenv("SHORTLINKER_SESSION"); s != "" {
		return s
	}
	return os.Getenv("CLAUDE_CODE_SESSION_ID")
}

// savedHubURL is the hub's address if it's known without asking tailscale:
// SHORTLINKER_URL, else ~/.config/shortlinker/url.
func savedHubURL(home string) string {
	if u := os.Getenv("SHORTLINKER_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	if data, err := os.ReadFile(filepath.Join(configDir(home), "url")); err == nil {
		return strings.TrimRight(strings.TrimSpace(string(data)), "/")
	}
	return ""
}

// publicHubURL is the address links are printed with: the saved one, else
// https://<this Mac's tailnet name>, which `tailscale serve` maps to the
// hub. That last lookup is saved: the macOS tailscale CLI starts the whole
// app and takes 10-19 s a call.
func publicHubURL(home string) (string, error) {
	if u := savedHubURL(home); u != "" {
		return u, nil
	}
	u, err := tailnetURL()
	if err != nil {
		return "", err
	}
	cache := filepath.Join(configDir(home), "url")
	if err := os.MkdirAll(filepath.Dir(cache), 0o700); err == nil {
		os.WriteFile(cache, []byte(u+"\n"), 0o644) // best effort; next run looks it up again
	}
	return u, nil
}

func tailnetURL() (string, error) {
	out, err := exec.Command(tailscaleBin(), "status", "--json", "--peers=false").Output()
	if err != nil {
		return "", fmt.Errorf("can't read this Mac's tailnet name (set SHORTLINKER_URL): %v", err)
	}
	var status struct {
		Self struct{ DNSName string }
	}
	if err := json.Unmarshal(out, &status); err != nil || status.Self.DNSName == "" {
		return "", fmt.Errorf("can't read this Mac's tailnet name (set SHORTLINKER_URL)")
	}
	return "https://" + strings.TrimSuffix(status.Self.DNSName, "."), nil
}
