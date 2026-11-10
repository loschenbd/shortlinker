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

// Exit codes for `sl-link` and `shortlinker prune`, matching sb-link's where
// they overlap.
const (
	exitOK          = 0
	exitError       = 1
	exitNoSuchPath  = 2
	exitUnreachable = 4
)

// defaultLocalPort is the hub's loopback-only port, which holds the API.
const defaultLocalPort = "7778"

const linkUsage = `usage: sl-link <path>...   (or: shortlinker link <path>...)

Prints one link per file or folder. The link opens the file on whichever Mac
you click it on (Ghostty, Moshi Desktop, Notion) and shows it on a phone.
Linking the same path again gives the same link. Run it on the hub Mac.

Exit codes: 0 ok; 1 other error; 2 no such file or folder; 4 hub unreachable.
The hub's address comes from SHORTLINKER_URL, else ~/.config/shortlinker/url,
which the first run fills in from this Mac's tailnet name.
`

const pruneUsage = `usage: shortlinker prune [-n]

Removes links that sl-link made whose file or folder no longer exists, and
prints their slugs. Hand-made links are never touched. -n only lists them.
`

// errUnreachable marks failures to reach the hub at all.
var errUnreachable = errors.New("hub unreachable")

// errNoAPI means the address answered but isn't a hub (an opener agent).
var errNoAPI = errors.New("this Mac isn't the shortlinker hub, so it can't make links")

// linker registers a target with the hub and returns its slug.
type linker func(target string) (string, error)

func localAPI() string { return "http://127.0.0.1:" + defaultLocalPort }

// runLink is the `sl-link` / `shortlinker link` entry point.
func runLink(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprint(os.Stderr, linkUsage)
		if len(args) == 0 {
			return exitError
		}
		return exitOK
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "sl-link:", err)
		return exitError
	}
	hubURL, err := publicHubURL(home)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sl-link:", err)
		return exitError
	}
	client := &http.Client{Timeout: 3 * time.Second}
	register := func(target string) (string, error) { return postLink(client, localAPI(), target) }
	return linkPaths(args, home, hubURL, register, os.Stdout, os.Stderr)
}

// linkPaths links each path and prints its URL, one per line, in order.
func linkPaths(paths []string, home, hubURL string, register linker, out, errOut io.Writer) int {
	code := exitOK
	for _, p := range paths {
		abs, err := filepath.Abs(ExpandPath(p, home))
		if err != nil {
			fmt.Fprintf(errOut, "sl-link: %s: %v\n", p, err)
			code = exitError
			continue
		}
		if _, err := os.Stat(abs); err != nil {
			fmt.Fprintf(errOut, "sl-link: %s: no such file or folder\n", p)
			code = maxExit(code, exitNoSuchPath)
			continue
		}
		slug, err := register(tildePath(abs, home))
		if err != nil {
			fmt.Fprintf(errOut, "sl-link: %s: %v\n", p, err)
			if errors.Is(err, errUnreachable) {
				code = maxExit(code, exitUnreachable)
			} else {
				code = maxExit(code, exitError)
			}
			continue
		}
		fmt.Fprintf(out, "%s/%s\n", strings.TrimRight(hubURL, "/"), slug)
	}
	return code
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

// postJSON sends a JSON body to the hub's local API and decodes the reply.
func postJSON(client *http.Client, url string, body, result any) error {
	data, _ := json.Marshal(body)
	resp, err := client.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w (is it running? ~/Library/Logs/shortlinker.log): %v", errUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		return errNoAPI
	}
	var reply struct {
		Error string `json:"error"`
	}
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		if json.Unmarshal(raw, &reply) != nil || reply.Error == "" {
			reply.Error = resp.Status
		}
		return fmt.Errorf("hub refused: %s", reply.Error)
	}
	if err := json.Unmarshal(raw, result); err != nil {
		return fmt.Errorf("hub sent an unreadable reply: %v", err)
	}
	return nil
}

func postLink(client *http.Client, base, target string) (string, error) {
	var result struct {
		Slug string `json:"slug"`
	}
	if err := postJSON(client, strings.TrimRight(base, "/")+"/api/links", map[string]string{"target": target}, &result); err != nil {
		return "", err
	}
	if result.Slug == "" {
		return "", fmt.Errorf("hub sent no slug")
	}
	return result.Slug, nil
}

// runPrune is the `shortlinker prune` entry point.
func runPrune(args []string) int {
	dryRun := false
	for _, a := range args {
		switch a {
		case "-n", "--dry-run":
			dryRun = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, pruneUsage)
			return exitOK
		default:
			fmt.Fprint(os.Stderr, pruneUsage)
			return exitError
		}
	}
	var result struct {
		Removed []string `json:"removed"`
	}
	client := &http.Client{Timeout: 10 * time.Second}
	if err := postJSON(client, localAPI()+"/api/prune", map[string]bool{"dry_run": dryRun}, &result); err != nil {
		fmt.Fprintln(os.Stderr, "shortlinker prune:", err)
		if errors.Is(err, errUnreachable) {
			return exitUnreachable
		}
		return exitError
	}
	verb := "Removed"
	if dryRun {
		verb = "Would remove"
	}
	fmt.Printf("%s %d link(s) whose file is gone.\n", verb, len(result.Removed))
	for _, slug := range result.Removed {
		fmt.Println("  " + slug)
	}
	return exitOK
}

// publicHubURL is the address links are printed with: SHORTLINKER_URL, else
// the saved ~/.config/shortlinker/url, else https://<this Mac's tailnet
// name>, which `tailscale serve` maps to the hub. That last lookup is saved:
// the macOS tailscale CLI starts the whole app and takes 10-19 s a call.
func publicHubURL(home string) (string, error) {
	if u := os.Getenv("SHORTLINKER_URL"); u != "" {
		return strings.TrimRight(u, "/"), nil
	}
	cache := filepath.Join(home, ".config", "shortlinker", "url")
	if data, err := os.ReadFile(cache); err == nil {
		if u := strings.TrimSpace(string(data)); u != "" {
			return strings.TrimRight(u, "/"), nil
		}
	}
	u, err := tailnetURL()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(cache), 0o755); err == nil {
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
