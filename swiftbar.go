package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The SwiftBar menu. `shortlinker swiftbar` prints it in SwiftBar's plugin
// format; the plugin file `make swiftbar` installs is a two-line wrapper
// around it. `--nested` leaves out the menu bar line and the service rows,
// so another plugin can show the menu as a submenu: prefix every line with
// "--" for each level down.
//
// SwiftBar 2.1.1 greys out a submenu parent whose line changes between
// refreshes unless the parent has an action of its own, so the one parent
// here carries refresh=true. Rows without an action are greyed, which is
// how the summary row looks.

const recentCount = 8

type menuInput struct {
	nested    bool
	bin       string // this binary, which the menu's actions run
	state     string // "up", "down", "not-hub" or "error"
	err       string
	links     []linkDetails
	linksPath string
	logPath   string
	label     string // the LaunchAgent's label
	uid       int
}

func runSwiftbar(args []string, out io.Writer) int {
	m := menuInput{uid: os.Getuid(), state: "up"}
	for _, a := range args {
		if a == "--nested" {
			m.nested = true
		}
	}
	home, _ := os.UserHomeDir()
	m.bin, _ = os.Executable()
	m.linksPath = filepath.Join(configDir(home), "links.txt")
	m.logPath = filepath.Join(home, "Library", "Logs", "shortlinker.log")
	name := "user"
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	m.label = "com." + name + ".shortlinker"

	a, err := localAPI(home, 3*time.Second)
	if err == nil {
		var l listing
		if l, err = fetchListing(a); err == nil {
			m.links = l.Links
		}
	}
	switch {
	case err == nil:
	case errors.Is(err, errUnreachable):
		m.state = "down"
	case errors.Is(err, errNoToken), errors.Is(err, errNoAPI):
		m.state = "not-hub"
	default:
		m.state, m.err = "error", err.Error()
	}
	fmt.Fprint(out, renderMenu(m))
	return exitOK // SwiftBar shows a broken icon for a failing plugin; the menu says what's wrong instead
}

func renderMenu(m menuInput) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }
	if !m.nested {
		icon := "link"
		if m.state != "up" {
			icon = "exclamationmark.triangle"
		}
		line("| sfimage=" + icon)
		line("---")
	}
	switch m.state {
	case "down":
		line("shortlinker isn't running")
	case "not-hub":
		line("This Mac opens links; the hub Mac keeps them")
	case "error":
		line(menuTitle(m.err))
	default:
		renderLinks(m, line)
	}
	if !m.nested {
		line("---")
		line(menuAction("Restart shortlinker", "arrow.clockwise", true, "/bin/launchctl", "kickstart", "-k", "gui/"+strconv.Itoa(m.uid)+"/"+m.label))
		line(menuAction("Open log", "doc.text", false, "/usr/bin/open", "-a", "Console", m.logPath))
	}
	return b.String()
}

func renderLinks(m menuInput, line func(string)) {
	yours, auto, missing := 0, 0, 0
	for _, d := range m.links {
		if d.Source == "sl-link" {
			auto++
			if d.Kind == "missing" {
				missing++
			}
		} else {
			yours++
		}
	}
	line(fmt.Sprintf("%s · %d yours · %d from sl-link", plural(len(m.links), "link"), yours, auto))
	line("---")
	line(menuAction("Link Finder selection", "link.badge.plus", false, m.bin, "menu", "link", "--selection", "--copy", "--notify"))
	line(menuAction("Find links…", "magnifyingglass", false, m.bin, "manage"))

	var recent []linkDetails
	for i := len(m.links) - 1; i >= 0 && len(recent) < recentCount; i-- {
		if d := m.links[i]; d.Source == "sl-link" && d.Kind != "missing" {
			recent = append(recent, d)
		}
	}
	// Every row below is always there, so the menu keeps its shape: SwiftBar 2.1.0 and 2.1.1
	// stop updating a submenu once its number of rows changes, until SwiftBar is relaunched,
	// and with --nested this whole menu is a submenu of another plugin.
	line("Recently linked | sfimage=clock refresh=true")
	if len(recent) == 0 {
		line("--Nothing linked yet")
	} else {
		names := map[string]int{}
		for _, d := range recent {
			names[nameOf(d.Target)]++
		}
		for _, d := range recent {
			name := nameOf(d.Target)
			if names[name] > 1 { // two README.md files: say which folder
				name += " · " + nameOf(filepath.Dir(d.Target))
			}
			icon := "doc"
			if d.Kind == "folder" {
				icon = "folder"
			}
			line("--" + menuAction(name, icon, false, m.bin, "copy", "--notify", d.Slug))
			line("--" + menuAction(name+" as Markdown", "text.quote", false, m.bin, "copy", "--markdown", "--notify", d.Slug) + " alternate=true")
		}
	}
	if missing > 0 {
		line(menuAction("Prune "+plural(missing, "dead link"), "trash", true, m.bin, "prune", "--notify"))
	} else {
		line("No dead links | sfimage=trash")
	}
	line(menuAction("Edit links.txt", "square.and.pencil", false, "/usr/bin/open", "-t", m.linksPath))
}

// menuAction is a row that runs argv when clicked.
func menuAction(title, sfimage string, refresh bool, argv ...string) string {
	var b strings.Builder
	b.WriteString(menuTitle(title) + " | bash=" + menuQuote(argv[0]))
	for i, a := range argv[1:] {
		fmt.Fprintf(&b, " param%d=%s", i+1, menuQuote(a))
	}
	b.WriteString(" terminal=false")
	if refresh {
		b.WriteString(" refresh=true")
	}
	b.WriteString(" sfimage=" + sfimage)
	return b.String()
}

// menuTitle keeps a title from breaking the line format: "|" starts the
// parameters and leading dashes would make it a submenu row.
func menuTitle(s string) string {
	s = strings.NewReplacer("|", "¦", "\n", " ", "\r", " ").Replace(s)
	return strings.TrimLeft(s, "-")
}

func menuQuote(v string) string {
	v = strings.ReplaceAll(v, `"`, `'`)
	if v == "" || strings.ContainsAny(v, " \t") {
		return `"` + v + `"`
	}
	return v
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}
