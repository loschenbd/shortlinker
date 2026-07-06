package main

import (
	"os"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T, content string) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "links.txt")
	if content != "" {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &Store{Path: path}
}

func TestStoreAllMissingFile(t *testing.T) {
	s := newTestStore(t, "")
	links, err := s.All()
	if err != nil {
		t.Fatalf("All() on missing file: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("links = %v, want empty", links)
	}
}

func TestStoreGet(t *testing.T) {
	s := newTestStore(t, "budget = ~/Documents/budget.numbers\n")
	target, ok, err := s.Get("budget")
	if err != nil || !ok || target != "~/Documents/budget.numbers" {
		t.Errorf("Get(budget) = %q, %v, %v", target, ok, err)
	}
	if _, ok, _ := s.Get("nope"); ok {
		t.Error("Get(nope) reported ok")
	}
}

func TestStoreSetAppendsNewSlug(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old\n")
	if err := s.Set("daily", "obsidian://daily"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# my links\nbudget = /old\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

func TestStoreSetUpdatesInPlacePreservingCommentsAndOrder(t *testing.T) {
	s := newTestStore(t, "# my links\nbudget = /old/path\ndaily = obsidian://daily\n")
	if err := s.Set("budget", "/new/path"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# my links\nbudget = /new/path\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
}

func TestStoreSetCreatesFileAndParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "links.txt")
	s := &Store{Path: path}
	if err := s.Set("budget", "/x"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "budget = /x\n" {
		t.Errorf("file = %q", data)
	}
}

func TestStoreSetRejectsInvalid(t *testing.T) {
	s := newTestStore(t, "")
	if err := s.Set("Bad Slug", "/x"); err == nil {
		t.Error("Set with invalid slug succeeded")
	}
	if err := s.Set("api", "/x"); err == nil {
		t.Error("Set with reserved slug succeeded")
	}
	if err := s.Set("ok", ""); err == nil {
		t.Error("Set with empty target succeeded")
	}
}

func TestStoreDelete(t *testing.T) {
	s := newTestStore(t, "# keep\nbudget = /x\ndaily = obsidian://daily\n")
	if err := s.Delete("budget"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(s.Path)
	want := "# keep\ndaily = obsidian://daily\n"
	if string(data) != want {
		t.Errorf("file = %q, want %q", data, want)
	}
	if err := s.Delete("nope"); err == nil {
		t.Error("Delete(nope) succeeded, want error")
	}
}
