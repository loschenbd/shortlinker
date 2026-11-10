package main

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

var slugRe = regexp.MustCompile(`^[a-z0-9-]+$`)

var nonSlugRun = regexp.MustCompile(`[^a-z0-9]+`)

// maxAutoSlug keeps picked slugs readable in a link's address.
const maxAutoSlug = 60

// reservedSlugs collide with routes and can never be registered.
var reservedSlugs = map[string]bool{"o": true, "f": true, "api": true}

func ValidSlug(s string) bool {
	return slugRe.MatchString(s) && !reservedSlugs[s]
}

// SlugFromPath names a file or folder's link after its base name, extension
// included: "Q3 Plan (v2).md" gives "q3-plan-v2-md". The result can still be
// taken or reserved; Store.Ensure adds -2, -3, ... until it's free.
func SlugFromPath(p string) string {
	s := strings.Trim(nonSlugRun.ReplaceAllString(strings.ToLower(filepath.Base(p)), "-"), "-")
	if len(s) > maxAutoSlug {
		s = strings.TrimRight(s[:maxAutoSlug], "-")
	}
	if s == "" {
		return "file"
	}
	return s
}

// IsRedirect reports whether target is served as a 302 redirect (anything
// with a URL scheme) rather than opened as a local path.
func IsRedirect(target string) bool {
	return strings.Contains(target, "://")
}

// ExpandPath replaces a leading ~ with home.
func ExpandPath(target, home string) string {
	if target == "~" {
		return home
	}
	if strings.HasPrefix(target, "~/") {
		return filepath.Join(home, target[2:])
	}
	return target
}

// Link is one parsed line of links.txt.
type Link struct {
	Target string // a URL to redirect to, or a file/folder path
	Phone  string // optional page phones get instead of the file (a SilverBullet page)
	Auto   bool   // made by sl-link: below autoMarker
}

// phoneSep separates a target from its phone page:
//
//	notes-md = ~/Documents/notes.md | https://hub.ts.net:3000/notes
const phoneSep = " | "

// splitTarget splits a line's right-hand side into target and phone page.
// It only splits off an http(s) URL, so a path that contains " | " is kept whole.
func splitTarget(raw string) (target, phone string) {
	if i := strings.LastIndex(raw, phoneSep); i >= 0 {
		if p := strings.TrimSpace(raw[i+len(phoneSep):]); isWebURL(p) {
			return strings.TrimSpace(raw[:i]), p
		}
	}
	return raw, ""
}

func isWebURL(s string) bool {
	return (strings.HasPrefix(s, "https://") || strings.HasPrefix(s, "http://")) && !strings.ContainsAny(s, " \t")
}

// formatLine writes a link line.
func formatLine(slug, target, phone string) string {
	if phone != "" {
		return slug + " = " + target + phoneSep + phone
	}
	return slug + " = " + target
}

// ParseLinks parses links.txt content into slug→target. Blank lines and
// #-comments are ignored; malformed lines are skipped with a warning.
func ParseLinks(content string) (map[string]string, []string) {
	entries, warnings := parseEntries(content)
	links := make(map[string]string, len(entries))
	for slug, l := range entries {
		links[slug] = l.Target
	}
	return links, warnings
}

// parseEntries is ParseLinks with each link's phone page and section.
func parseEntries(content string) (map[string]Link, []string) {
	links := map[string]Link{}
	var warnings []string
	auto := false
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if isAutoMarker(trimmed) {
			auto = true
			continue
		}
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		slug, raw, ok := strings.Cut(trimmed, "=")
		slug = strings.TrimSpace(slug)
		target, phone := splitTarget(strings.TrimSpace(raw))
		if !ok || target == "" || !ValidSlug(slug) {
			warnings = append(warnings, fmt.Sprintf("line %d: skipping malformed line: %q", i+1, line))
			continue
		}
		links[slug] = Link{Target: target, Phone: phone, Auto: auto}
	}
	return links, warnings
}
