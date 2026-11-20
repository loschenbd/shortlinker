package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
)

const usage = `usage: shortlinker [command]

With no command, runs the server (see --help for its flags).

  link <path>...        print a link for each file or folder (same as sl-link)
  ls [words...]         list links, or only those matching every word
  rm <slug>...          delete links
  mv <old> <new>        rename a link (its old address stops working)
  copy <slug>           copy a link to the clipboard (--markdown for [name](link))
  manage                open the manage page in your browser
  prune [-n]            remove sl-link's links whose file is gone (-n: just list them)
  swiftbar [--nested]   print the SwiftBar menu (see README)

The commands run on the hub Mac and use its token in ~/.config/shortlinker/token.
`

// runCommand runs a subcommand, or returns false if args isn't one.
func runCommand(args []string) (int, bool) {
	if len(args) == 0 {
		return 0, false
	}
	rest := args[1:]
	switch args[0] {
	case "link":
		return runLink(rest), true
	case "ls", "list":
		return runList(rest, os.Stdout), true
	case "rm":
		return runRemove(rest), true
	case "mv":
		return runRename(rest), true
	case "copy":
		return runCopy(rest), true
	case "manage":
		return runManage(rest), true
	case "prune":
		return runPrune(rest), true
	case "swiftbar":
		return runSwiftbar(rest, os.Stdout), true
	case "menu":
		return runMenuAction(rest), true
	case "help", "-h", "--help":
		fmt.Print(usage)
		return exitOK, true
	}
	return 0, false
}

// commandAPI is the hub's API for a command, or an exit code and a message.
func commandAPI(name string, timeout time.Duration) (api, string, int) {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "shortlinker %s: %v\n", name, err)
		return api{}, "", exitError
	}
	a, err := localAPI(home, timeout)
	if err != nil {
		fmt.Fprintf(os.Stderr, "shortlinker %s: %v\n", name, err)
		return api{}, "", exitError
	}
	return a, home, exitOK
}

func exitFor(name string, err error) int {
	fmt.Fprintf(os.Stderr, "shortlinker %s: %v\n", name, err)
	if errors.Is(err, errUnreachable) {
		return exitUnreachable
	}
	return exitError
}

// listing is GET /api/links?details=1.
type listing struct {
	Hub   string        `json:"hub"`
	Links []linkDetails `json:"links"`
}

func fetchListing(a api) (listing, error) {
	var l listing
	err := a.do(http.MethodGet, "/api/links?details=1", nil, &l)
	return l, err
}

// matches reports whether a link contains every word, ignoring case.
func (d linkDetails) matches(words []string) bool {
	hay := strings.ToLower(d.Slug + " " + d.Target)
	for _, w := range words {
		if !strings.Contains(hay, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

func runList(args []string, out io.Writer) int {
	asJSON := false
	var words []string
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "-h", "--help":
			fmt.Fprintln(os.Stderr, "usage: shortlinker ls [--json] [words...]")
			return exitOK
		default:
			words = append(words, a)
		}
	}
	a, _, code := commandAPI("ls", 5*time.Second)
	if code != exitOK {
		return code
	}
	l, err := fetchListing(a)
	if err != nil {
		return exitFor("ls", err)
	}
	found := []linkDetails{}
	for _, d := range l.Links {
		if d.matches(words) {
			found = append(found, d)
		}
	}
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		enc.Encode(found)
		return exitOK
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, d := range found {
		note := ""
		if d.Source == "sl-link" {
			note = "sl-link"
		}
		if d.Kind == "missing" {
			note = strings.TrimSpace(note + " missing")
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", d.Slug, d.Target, note)
	}
	tw.Flush()
	return exitOK
}

func runRemove(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: shortlinker rm <slug>...")
		return exitError
	}
	a, _, code := commandAPI("rm", 5*time.Second)
	if code != exitOK {
		return code
	}
	for _, slug := range args {
		if err := a.do(http.MethodDelete, "/api/links/"+slug, nil, nil); err != nil {
			code = maxExit(code, exitFor("rm", err))
			continue
		}
		fmt.Println("removed", slug)
	}
	return code
}

func runRename(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: shortlinker mv <old-slug> <new-slug>")
		return exitError
	}
	a, _, code := commandAPI("mv", 5*time.Second)
	if code != exitOK {
		return code
	}
	if err := a.do(http.MethodPost, "/api/links/"+args[0]+"/rename", map[string]string{"to": args[1]}, nil); err != nil {
		return exitFor("mv", err)
	}
	fmt.Printf("renamed %s to %s; links to %s no longer work\n", args[0], args[1], args[0])
	return exitOK
}

func runCopy(args []string) int {
	markdown, notify := false, false
	var slugs []string
	for _, a := range args {
		switch a {
		case "--markdown":
			markdown = true
		case "--notify":
			notify = true
		default:
			slugs = append(slugs, a)
		}
	}
	if len(slugs) != 1 {
		fmt.Fprintln(os.Stderr, "usage: shortlinker copy [--markdown] <slug>")
		return exitError
	}
	a, home, code := commandAPI("copy", 5*time.Second)
	if code != exitOK {
		return code
	}
	l, err := fetchListing(a)
	if err != nil {
		return exitFor("copy", err)
	}
	var found *linkDetails
	for i := range l.Links {
		if l.Links[i].Slug == slugs[0] {
			found = &l.Links[i]
		}
	}
	if found == nil {
		return exitFor("copy", fmt.Errorf("unknown slug %q", slugs[0]))
	}
	hub := l.Hub
	if hub == "" {
		if hub, err = publicHubURL(home); err != nil {
			return exitFor("copy", err)
		}
	}
	text := hub + "/" + found.Slug
	name := found.Slug
	if found.Kind != "url" {
		name = nameOf(found.Target)
	}
	if markdown {
		text = markdownLink(name, text)
	}
	if err := copyToClipboard(text); err != nil {
		return exitFor("copy", err)
	}
	fmt.Println(text)
	if notify {
		notifyUser("Link copied", name)
	}
	return exitOK
}

func runManage(args []string) int {
	a, _, code := commandAPI("manage", 5*time.Second)
	if code != exitOK {
		return code
	}
	var reply struct {
		URL string `json:"url"`
	}
	if err := a.do(http.MethodPost, "/api/manage-code", struct{}{}, &reply); err != nil {
		return exitFor("manage", err)
	}
	if len(args) > 0 && args[0] == "--print" {
		fmt.Println(reply.URL)
		return exitOK
	}
	if out, err := exec.Command("/usr/bin/open", reply.URL).CombinedOutput(); err != nil {
		return exitFor("manage", fmt.Errorf("open: %v: %s", err, strings.TrimSpace(string(out))))
	}
	return exitOK
}

const pruneUsage = `usage: shortlinker prune [-n]

Removes links that sl-link made whose file or folder no longer exists, and
prints their slugs. Your own links are never touched. -n only lists them.
`

// runPrune is the `shortlinker prune` entry point.
func runPrune(args []string) int {
	dryRun, notify := false, false
	for _, a := range args {
		switch a {
		case "-n", "--dry-run":
			dryRun = true
		case "--notify":
			notify = true
		case "-h", "--help":
			fmt.Fprint(os.Stderr, pruneUsage)
			return exitOK
		default:
			fmt.Fprint(os.Stderr, pruneUsage)
			return exitError
		}
	}
	a, _, code := commandAPI("prune", 10*time.Second)
	if code != exitOK {
		return code
	}
	var result struct {
		Removed []string `json:"removed"`
	}
	if err := a.do(http.MethodPost, "/api/prune", map[string]bool{"dry_run": dryRun}, &result); err != nil {
		return exitFor("prune", err)
	}
	verb := "Removed"
	if dryRun {
		verb = "Would remove"
	}
	summary := fmt.Sprintf("%s %d link(s) whose file is gone.", verb, len(result.Removed))
	fmt.Println(summary)
	for _, slug := range result.Removed {
		fmt.Println("  " + slug)
	}
	if notify {
		notifyUser(summary, strings.Join(result.Removed, ", "))
	}
	return exitOK
}

// runMenuAction runs a menu bar action detached from SwiftBar. SwiftBar
// kills an action's whole process group about 2 s after the click, and
// linking a Markdown file can take longer (its phone page), so the action
// runs as a child in a session of its own. SwiftBar's PATH is launchd's
// bare one, so the child gets the usual tool folders back.
func runMenuAction(args []string) int {
	if len(args) == 0 {
		return exitError
	}
	self, err := os.Executable()
	if err != nil {
		return exitError
	}
	home, _ := os.UserHomeDir()
	cmd := exec.Command(self, args...)
	cmd.Env = append(os.Environ(), "PATH="+menuPath(home, os.Getenv("PATH")))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		notifyUser("shortlinker", err.Error())
		return exitError
	}
	cmd.Process.Release()
	return exitOK
}

func menuPath(home, path string) string {
	dirs := []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/local/bin"}
	if path == "" {
		path = "/usr/bin:/bin:/usr/sbin:/sbin"
	}
	return strings.Join(dirs, ":") + ":" + path
}

func copyToClipboard(text string) error {
	cmd := exec.Command("/usr/bin/pbcopy")
	cmd.Stdin = strings.NewReader(text)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("pbcopy: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// notifyUser shows a notification. Text goes in as arguments, never into
// the script, so quotes in a file name can't break it.
func notifyUser(title, text string) {
	exec.Command("/usr/bin/osascript",
		"-e", "on run argv",
		"-e", "display notification (item 2 of argv) with title (item 1 of argv)",
		"-e", "end run",
		title, text).Run()
}

// finderSelection is the POSIX paths of what's selected in Finder. The
// first call asks you to let the calling app control Finder.
func finderSelection() ([]string, error) {
	out, err := exec.Command("/usr/bin/osascript",
		"-e", `tell application "Finder" to set sel to selection as alias list`,
		"-e", `set out to ""`,
		"-e", `repeat with f in sel`,
		"-e", `set out to out & POSIX path of f & linefeed`,
		"-e", `end repeat`,
		"-e", `return out`,
	).Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return nil, fmt.Errorf("couldn't read Finder's selection: %s", strings.TrimSpace(string(exit.Stderr)))
		}
		return nil, fmt.Errorf("couldn't read Finder's selection: %v", err)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
