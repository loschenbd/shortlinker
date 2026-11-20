package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

A Markdown file can get a page phones open instead of plain text: sl-link
runs the command in SHORTLINKER_PHONE_CMD (default sb-link, a SilverBullet
linker) once and saves the URL it prints. Without that command, nothing
changes.

Exit codes: 0 ok; 1 other error; 2 no such file or folder; 4 hub unreachable.
The hub's address comes from SHORTLINKER_URL, else ~/.config/shortlinker/url,
which the first run fills in from this Mac's tailnet name.
`

// errUnreachable marks failures to reach the hub at all.
var errUnreachable = errors.New("hub unreachable")

// errNoAPI means the address answered but isn't a hub (an opener agent).
var errNoAPI = errors.New("this Mac isn't the shortlinker hub, so it can't make links")

// linker registers a target with the hub and returns its slug and its
// phone page, if it has one.
type linker func(target string) (slug, phone string, err error)

// linkOptions are what linkPaths needs; tests swap in fakes.
type linkOptions struct {
	home, hubURL string
	finder       bool   // add ?finder: a Mac shows the file in Finder
	markdown     bool   // print [name](link)
	phoneLabel   string // what the notes call a phone page ("SilverBullet page" for sb-link)
	register     linker
	// phonePage finds the page phones should get for a Markdown file (its
	// SilverBullet page); "" with no error means there isn't one.
	phonePage func(abs string) (string, error)
	savePhone func(slug, url string) error
}

// api talks to the hub's local API with its token.
type api struct {
	base, token string
	client      *http.Client
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
	return api{base: strings.TrimRight(base, "/"), token: token, client: &http.Client{Timeout: timeout}}, nil
}

// do sends a request (with a JSON body if body isn't nil) and decodes the
// JSON reply into result.
func (a api) do(method, path string, body, result any) error {
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
	for _, a := range args {
		switch a {
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
	o.register = func(target string) (string, string, error) { return postLink(hub, target) }
	o.phonePage = phonePageFor(phoneCommand())
	o.phoneLabel = phonePageLabel(phoneCommand())
	o.savePhone = func(slug, url string) error { return postPhone(hub, slug, url) }

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
		if phone == "" && !info.IsDir() && strings.EqualFold(filepath.Ext(abs), ".md") && o.phonePage != nil {
			label := o.phoneLabel
			if label == "" {
				label = phonePageLabel("sb-link")
			}
			// The link works either way; without a page, phones get plain text.
			if url, err := o.phonePage(abs); err != nil {
				fmt.Fprintf(errOut, "sl-link: %s: no %s, so phones get plain text: %v\n", p, label, err)
			} else if url != "" {
				if err := o.savePhone(slug, url); err != nil {
					fmt.Fprintf(errOut, "sl-link: %s: couldn't save its %s: %v\n", p, label, err)
				}
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

// phoneCommand is the command that finds a Markdown file's phone page.
func phoneCommand() string {
	if c := os.Getenv("SHORTLINKER_PHONE_CMD"); c != "" {
		return c
	}
	return "sb-link"
}

// phonePageLabel names a command's pages in sl-link's notes. sb-link's
// keep the words people and agents already watch for.
func phonePageLabel(command string) string {
	if filepath.Base(command) == "sb-link" {
		return "SilverBullet page"
	}
	return "phone page"
}

// phonePageFor runs a phone-page command on a Markdown file and returns the
// URL it prints last. The contract (sb-link's): exit 0 and print a URL when
// there's a page; exit non-zero with the reason on stderr when there isn't.
// Only exit 0 counts: sb-link's exit 4 prints a URL too, but SilverBullet
// was down, so the page may not exist yet. The command can take several
// seconds, which is why the page is saved with the link rather than looked
// up on every tap. With no such command on this Mac there's no page and
// nothing to say.
func phonePageFor(command string) func(abs string) (string, error) {
	return func(abs string) (string, error) {
		bin, err := exec.LookPath(command)
		if err != nil {
			return "", nil
		}
		name := filepath.Base(command)
		var stderr bytes.Buffer
		cmd := exec.Command(bin, abs)
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		if err != nil {
			reason := commandReason(name, stderr.String())
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 2 && reason != "" {
				return "", fmt.Errorf("%s: %s", name, reason)
			}
			if reason == "" {
				return "", fmt.Errorf("%s: %v", name, err)
			}
			return "", fmt.Errorf("%s (%v): %s", name, err, reason)
		}
		lines := strings.Fields(strings.TrimSpace(string(stdout)))
		if len(lines) == 0 || !isWebURL(lines[len(lines)-1]) {
			return "", fmt.Errorf("%s printed no URL", name)
		}
		return lines[len(lines)-1], nil
	}
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

func postLink(a api, target string) (slug, phone string, err error) {
	var result struct {
		Slug  string `json:"slug"`
		Phone string `json:"phone"`
	}
	if err := a.do(http.MethodPost, "/api/links", map[string]string{"target": target}, &result); err != nil {
		return "", "", err
	}
	if result.Slug == "" {
		return "", "", fmt.Errorf("hub sent no slug")
	}
	return result.Slug, result.Phone, nil
}

func postPhone(a api, slug, url string) error {
	return a.do(http.MethodPost, "/api/links/"+slug+"/phone", map[string]string{"url": url}, nil)
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
	bin, err := exec.LookPath("tailscale")
	if err != nil {
		bin = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
	}
	out, err := exec.Command(bin, "status", "--json", "--peers=false").Output()
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
