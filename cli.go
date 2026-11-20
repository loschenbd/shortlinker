package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"
	"unicode"
)

const usage = `usage: shortlinker [command]

With no command, runs the server (see --help for its flags).

  link <path>...        print a link for each file or folder (same as sl-link)
  ls [options] [words]  list links, or only those matching every word:
                          --kept      only kept links
                          --archived  include archived links
                          --removed   include removed ones (tombstones)
                          --unclicked only links nobody has clicked that aren't kept
                          --project P only links in a project whose path contains P
                          --older N   only links made more than N days ago
                          --json      print JSON
  keep <slug>...        keep links: they never archive, and prune leaves them
  unkeep <slug>...      stop keeping links
  note <slug> <text>    set a link's note (--clear removes it)
  rm <slug>...          delete links (their slugs are never given to another file)
  mv <old> <new>        rename a link and keep it (the old address sends clicks on)
  archive <slug>...     hide links from the lists (they still work)
  restore <slug>...     bring archived links back
  copy <slug>           copy a link to the clipboard (--markdown for [name](link))
  manage                open the manage page in your browser (--print: print its address)
  prune [-n]            remove links that aren't kept whose file is gone (-n: just list them)
  export                print every link as text: slug = target | phone page
  import [file]         add the links in a file (or stdin) in export's format, kept
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
	case "keep":
		return runKeep(rest, true), true
	case "unkeep":
		return runKeep(rest, false), true
	case "note":
		return runNote(rest), true
	case "export":
		return runExport(rest, os.Stdout), true
	case "import":
		return runImport(rest, os.Stdin), true
	case "archive":
		return runArchive(rest, true), true
	case "restore":
		return runArchive(rest, false), true
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

func fetchListing(a api) (listing, error) { return fetchListingWith(a, "") }

// fetchListingWith adds query parameters, e.g. "&archived=1".
func fetchListingWith(a api, extra string) (listing, error) {
	var l listing
	err := a.do(http.MethodGet, "/api/links?details=1"+extra, nil, &l)
	return l, err
}

// matches reports whether a link contains every word, ignoring case.
func (d linkDetails) matches(words []string) bool {
	hay := strings.ToLower(d.Slug + " " + d.Target + " " + d.Note + " " + d.Project)
	for _, w := range words {
		if !strings.Contains(hay, strings.ToLower(w)) {
			return false
		}
	}
	return true
}

func runList(args []string, out io.Writer) int {
	var asJSON, unclicked, keptOnly bool
	var project, extra string
	olderDays := -1
	var words []string
	usage := func() int {
		fmt.Fprintln(os.Stderr, "usage: shortlinker ls [--kept] [--archived] [--removed] [--unclicked] [--project P] [--older DAYS] [--json] [words...]")
		return exitError
	}
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--json":
			asJSON = true
		case "--archived":
			extra += "&archived=1"
		case "--removed":
			extra += "&removed=1"
		case "--unclicked":
			unclicked = true
		case "--kept":
			keptOnly = true
		case "--project", "--older":
			if i+1 == len(args) {
				return usage()
			}
			i++
			if a == "--project" {
				project = strings.ToLower(args[i])
				break
			}
			n, err := strconv.Atoi(strings.TrimSuffix(args[i], "d"))
			if err != nil || n < 0 {
				return usage()
			}
			olderDays = n
		case "-h", "--help":
			usage()
			return exitOK
		default:
			words = append(words, a)
		}
	}
	a, _, code := commandAPI("ls", 5*time.Second)
	if code != exitOK {
		return code
	}
	l, err := fetchListingWith(a, extra)
	if err != nil {
		return exitFor("ls", err)
	}
	found := []linkDetails{}
	cutoff := time.Now().Add(-time.Duration(olderDays) * 24 * time.Hour)
	for _, d := range l.Links {
		switch {
		case !d.matches(words):
		case unclicked && (d.Kept || d.Clicks > 0):
		case keptOnly && !d.Kept:
		case project != "" && !strings.Contains(strings.ToLower(d.Project), project):
		case olderDays >= 0 && (d.Created == nil || d.Created.After(cutoff)):
		default:
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
		if d.Kept {
			note = "kept"
		}
		if d.Kind == "missing" {
			note = strings.TrimSpace(note + " missing")
		}
		switch {
		case d.Removed != nil:
			note = strings.TrimSpace(note + " removed")
		case d.Archived != nil:
			note = strings.TrimSpace(note + " archived")
		}
		if d.Clicks > 0 {
			note = strings.TrimSpace(note + " " + plural(d.Clicks, "click"))
		}
		if d.Note != "" {
			note = strings.TrimSpace(note + " · " + d.Note)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\n", printable(d.Slug), printable(d.Target), printable(note))
	}
	tw.Flush()
	return exitOK
}

func runArchive(args []string, archived bool) int {
	name := map[bool]string{true: "archive", false: "restore"}[archived]
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: shortlinker %s <slug>...\n", name)
		return exitError
	}
	a, _, code := commandAPI(name, 5*time.Second)
	if code != exitOK {
		return code
	}
	for _, slug := range args {
		if err := a.do(http.MethodPost, linkPath(slug, "archive"), map[string]bool{"archived": archived}, nil); err != nil {
			code = maxExit(code, exitFor(name, err))
			continue
		}
		fmt.Printf("%sd %s\n", name, slug)
	}
	return code
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
		if err := a.do(http.MethodDelete, linkPath(slug, ""), nil, nil); err != nil {
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
	if err := a.do(http.MethodPost, linkPath(args[0], "rename"), map[string]string{"to": args[1]}, nil); err != nil {
		return exitFor("mv", err)
	}
	fmt.Printf("renamed %s to %s, and kept it; links to %s send clicks on to it\n", args[0], args[1], args[0])
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
		text = markdownLink(printable(name), text)
	}
	if err := copyToClipboard(text); err != nil {
		return exitFor("copy", err)
	}
	fmt.Println(printable(text))
	if notify {
		notifyUser("Link copied", printable(name))
	}
	return exitOK
}

// linkPath is the API path of a link, or of one of its actions.
func linkPath(slug, action string) string {
	p := "/api/links/" + url.PathEscape(slug)
	if action != "" {
		p += "/" + action
	}
	return p
}

// printable shows control characters as \x1b and the like. File names can
// hold them, and printed raw they can drive the terminal: retitle the
// window, or write the clipboard (OSC 52).
func printable(s string) string {
	if strings.IndexFunc(s, unicode.IsControl) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsControl(r) {
			fmt.Fprintf(&b, "\\x%02x", r)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func runKeep(args []string, kept bool) int {
	name := map[bool]string{true: "keep", false: "unkeep"}[kept]
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "usage: shortlinker %s <slug>...\n", name)
		return exitError
	}
	a, _, code := commandAPI(name, 5*time.Second)
	if code != exitOK {
		return code
	}
	for _, slug := range args {
		if err := a.do(http.MethodPost, linkPath(slug, "keep"), map[string]bool{"kept": kept}, nil); err != nil {
			code = maxExit(code, exitFor(name, err))
			continue
		}
		if kept {
			fmt.Println("kept", slug)
		} else {
			fmt.Println("stopped keeping", slug, "(it archives if nobody uses it)")
		}
	}
	return code
}

func runNote(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: shortlinker note <slug> <text...> | --clear")
		return exitError
	}
	note := strings.Join(args[1:], " ")
	if note == "--clear" {
		note = ""
	}
	a, _, code := commandAPI("note", 5*time.Second)
	if code != exitOK {
		return code
	}
	if err := a.do(http.MethodPost, linkPath(args[0], "note"), map[string]string{"note": note}, nil); err != nil {
		return exitFor("note", err)
	}
	return exitOK
}

// runExport prints every link that isn't removed, kept ones first, in the
// text format import reads. A link's note goes on a comment line above it,
// which import reads back as its note.
func runExport(args []string, out io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "usage: shortlinker export > links.txt")
		return exitError
	}
	a, _, code := commandAPI("export", 10*time.Second)
	if code != exitOK {
		return code
	}
	l, err := fetchListingWith(a, "&archived=1")
	if err != nil {
		return exitFor("export", err)
	}
	fmt.Fprintf(out, "# shortlinker links, exported %s. `shortlinker import` reads this back.\n", time.Now().Format("2006-01-02"))
	section := ""
	for _, d := range l.Links {
		if s := map[bool]string{true: "Kept", false: "Not kept (import keeps them)"}[d.Kept]; s != section {
			fmt.Fprintf(out, "\n# %s\n\n", s)
			section = s
		}
		if strings.IndexFunc(d.Target+d.Phone, unicode.IsControl) >= 0 {
			fmt.Fprintf(os.Stderr, "shortlinker export: skipping %s: its target has a control character, which the text format can't hold\n", d.Slug)
			code = exitError
			continue
		}
		if d.Note != "" {
			fmt.Fprintln(out, "# "+d.Note)
		}
		fmt.Fprintln(out, formatLine(d.Slug, d.Target, d.Phone))
	}
	return code
}

// runImport adds the links in a file (or stdin) in export's format, as kept
// links. A slug that's already in use, even by a removed link, is left as
// it is and reported.
func runImport(args []string, stdin io.Reader) int {
	var in io.Reader = stdin
	switch {
	case len(args) > 1:
		fmt.Fprintln(os.Stderr, "usage: shortlinker import [file]")
		return exitError
	case len(args) == 1 && args[0] != "-":
		f, err := os.Open(args[0])
		if err != nil {
			return exitFor("import", err)
		}
		defer f.Close()
		in = f
	}
	data, err := io.ReadAll(io.LimitReader(in, 16<<20))
	if err != nil {
		return exitFor("import", err)
	}
	text := string(data)
	entries, warnings := parseEntries(text)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "shortlinker import:", w)
	}
	notes := commentNotes(strings.Split(text, "\n"))
	a, _, code := commandAPI("import", 10*time.Second)
	if code != exitOK {
		return code
	}
	l, err := fetchListingWith(a, "&archived=1&removed=1")
	if err != nil {
		return exitFor("import", err)
	}
	have := map[string]linkDetails{}
	for _, d := range l.Links {
		have[d.Slug] = d
	}
	added, same, taken := 0, 0, 0
	for _, e := range entries {
		if d, ok := have[e.Slug]; ok {
			if d.Removed == nil && d.Target == e.Target {
				same++
			} else {
				taken++
				fmt.Fprintf(os.Stderr, "shortlinker import: %s is already in use, so it's left as it is\n", e.Slug)
			}
			continue
		}
		body := map[string]string{"slug": e.Slug, "target": e.Target, "phone": e.Phone, "note": notes[e.Slug], "origin": "import"}
		if err := a.do(http.MethodPost, "/api/links", body, nil); err != nil {
			code = maxExit(code, exitFor("import", fmt.Errorf("%s: %w", e.Slug, err)))
			continue
		}
		added++
	}
	fmt.Printf("added %d, already there %d, slug in use %d\n", added, same, taken)
	if taken > 0 {
		code = maxExit(code, exitError)
	}
	return code
}

func runManage(args []string) int {
	a, _, code := commandAPI("manage", 5*time.Second)
	if code != exitOK {
		return code
	}
	var reply struct {
		Code string `json:"code"`
	}
	if err := a.do(http.MethodPost, "/api/manage-code", struct{}{}, &reply); err != nil {
		return exitFor("manage", err)
	}
	page, err := managePageURL(a.base, reply.Code)
	if err != nil {
		return exitFor("manage", err)
	}
	if len(args) > 0 && args[0] == "--print" {
		fmt.Println(page)
		return exitOK
	}
	if err := openURL(page); err != nil {
		return exitFor("manage", fmt.Errorf("%v (shortlinker manage --print prints the address to open yourself)", err))
	}
	return exitOK
}

// managePageURL is the manage page's address with a one-time code, on the
// API's own port. It's built here rather than taken from the reply, so
// nothing else on the port can choose what gets opened.
func managePageURL(apiBase, code string) (string, error) {
	if _, err := hex.DecodeString(code); err != nil || len(code) < 32 || len(code) > 128 {
		return "", fmt.Errorf("the hub sent a code that isn't one")
	}
	u, err := url.Parse(apiBase)
	if err != nil || u.Port() == "" {
		return "", fmt.Errorf("can't tell the API's port from %q", apiBase)
	}
	return "http://localhost:" + u.Port() + "/manage#code=" + code, nil
}

// openURL opens a URL in the default browser. The URL goes to osascript on
// stdin, not as an argument: every account on the Mac can read every
// process's arguments (ps), and the manage page's URL holds a code that's
// as good as a 12-hour session to whoever uses it first. Tests replace it.
var openURL = func(u string) error {
	cmd := openURLCommand(u)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("opening the browser: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func openURLCommand(u string) *exec.Cmd {
	cmd := exec.Command("/usr/bin/osascript", "-")
	cmd.Stdin = strings.NewReader("open location " + appleScriptString(u) + "\n")
	return cmd
}

func appleScriptString(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

const pruneUsage = `usage: shortlinker prune [-n]

Removes links whose file or folder no longer exists, and prints their
slugs. Kept links are never touched. -n only lists them. Removed links stay
as tombstones, so their slugs are never given to another file.
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
