package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// Store is a file-backed link store. Every read re-reads the file so edits
// made by hand or by other processes take effect immediately.
type Store struct {
	Path string
}

func (s *Store) All() (map[string]string, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	links, warnings := ParseLinks(string(data))
	for _, w := range warnings {
		log.Printf("%s: %s", s.Path, w)
	}
	return links, nil
}

func (s *Store) Get(slug string) (string, bool, error) {
	links, err := s.All()
	if err != nil {
		return "", false, err
	}
	target, ok := links[slug]
	return target, ok, nil
}

// Set adds or updates a link, preserving comments, blank lines, and the
// order of existing entries. New slugs are appended.
func (s *Store) Set(slug, target string) error {
	if !ValidSlug(slug) {
		return fmt.Errorf("invalid slug %q (must match [a-z0-9-]+ and not be reserved)", slug)
	}
	if target == "" {
		return fmt.Errorf("empty target")
	}
	lines, err := s.readLines()
	if err != nil {
		return err
	}
	newLine := slug + " = " + target
	replaced := false
	for i, line := range lines {
		if lineSlug(line) == slug {
			lines[i] = newLine
			replaced = true
			break
		}
	}
	if !replaced {
		lines = append(lines, newLine)
	}
	return s.writeLines(lines)
}

func (s *Store) Delete(slug string) error {
	lines, err := s.readLines()
	if err != nil {
		return err
	}
	kept := make([]string, 0, len(lines))
	found := false
	for _, line := range lines {
		if lineSlug(line) == slug {
			found = true
			continue
		}
		kept = append(kept, line)
	}
	if !found {
		return fmt.Errorf("unknown slug %q", slug)
	}
	return s.writeLines(kept)
}

// lineSlug returns the slug of a well-formed link line, or "".
func lineSlug(line string) string {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return ""
	}
	slug, _, ok := strings.Cut(trimmed, "=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(slug)
}

func (s *Store) readLines() ([]string, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n"), nil
}

func (s *Store) writeLines(lines []string) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.Path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}
