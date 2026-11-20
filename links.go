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

// Link is one link: a line of links.txt or of sl-link's file.
type Link struct {
	Target string // a URL to redirect to, or a file/folder path
	Phone  string // optional page phones get instead of the file (a SilverBullet page)
	Auto   bool   // made by sl-link: it lives in sl-link's file, not links.txt
}

// Entry is a link with its slug, and for sl-link's links, their record.
type Entry struct {
	Slug string
	Link
	Rec *Record // a copy of sl-link's record; nil for your links
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

type lineKind int

const (
	lineOther lineKind = iota // blank or a # comment
	lineLink                  // slug = target
	lineBad                   // anything else: skipped with a warning, never rewritten
)

// parseLine reads one line of a links file. Every read and every edit goes
// through it, so a line is a link to all of them or to none.
func parseLine(line string) (string, Link, lineKind) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", Link{}, lineOther
	}
	slug, raw, ok := strings.Cut(trimmed, "=")
	slug = strings.TrimSpace(slug)
	target, phone := splitTarget(strings.TrimSpace(raw))
	if !ok || target == "" || !ValidSlug(slug) {
		return "", Link{}, lineBad
	}
	return slug, Link{Target: target, Phone: phone}, lineLink
}

// parseEntries parses a links file into its links, in file order. When a
// slug appears twice the first line wins, the same line edits change.
// Malformed lines and repeated slugs come back as warnings.
func parseEntries(content string) ([]Entry, []string) {
	var entries []Entry
	var warnings []string
	seen := map[string]int{}
	for i, line := range strings.Split(content, "\n") {
		slug, l, kind := parseLine(line)
		switch kind {
		case lineBad:
			warnings = append(warnings, fmt.Sprintf("line %d: skipping malformed line: %q", i+1, line))
		case lineLink:
			if first, dup := seen[slug]; dup {
				warnings = append(warnings, fmt.Sprintf("line %d: %q is already defined on line %d; skipping this one", i+1, slug, first))
				continue
			}
			seen[slug] = i + 1
			entries = append(entries, Entry{Slug: slug, Link: l})
		}
	}
	return entries, warnings
}

// isAutoMarker matches the comment that used to start sl-link's section of
// links.txt, before sl-link got a file of its own (see Store.Migrate).
func isAutoMarker(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "# Added by sl-link")
}
