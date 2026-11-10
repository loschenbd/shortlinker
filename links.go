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

// ParseLinks parses links.txt content into slug→target. Blank lines and
// #-comments are ignored; malformed lines are skipped with a warning.
func ParseLinks(content string) (map[string]string, []string) {
	links := map[string]string{}
	var warnings []string
	for i, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		slug, target, ok := strings.Cut(trimmed, "=")
		slug, target = strings.TrimSpace(slug), strings.TrimSpace(target)
		if !ok || target == "" || !ValidSlug(slug) {
			warnings = append(warnings, fmt.Sprintf("line %d: skipping malformed line: %q", i+1, line))
			continue
		}
		links[slug] = target
	}
	return links, warnings
}
