package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	var (
		port  = flag.String("port", "7777", "port to listen on (loopback only)")
		links = flag.String("links", filepath.Join(home, ".config", "shortlinker", "links.txt"), "path to links file (hub role)")
		hub   = flag.String("hub", "", "hub base URL, e.g. https://mac.tailnet.ts.net; setting this runs an opener agent only")
	)
	flag.Parse()

	var store *Store
	if *hub == "" {
		store = &Store{Path: *links}
	}
	srv := NewServer(store, *hub, *port, home)

	addr := "127.0.0.1:" + *port
	if store != nil {
		log.Printf("shortlinker hub on http://%s (links: %s)", addr, *links)
	} else {
		log.Printf("shortlinker agent on http://%s (hub: %s)", addr, *hub)
	}
	log.Fatal(http.ListenAndServe(addr, srv.Handler()))
}
