package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"path/filepath"
)

func main() {
	// `sl-link <path>` (a symlink to this binary) and `shortlinker link
	// <path>` print a link for a file or folder instead of running a server.
	if filepath.Base(os.Args[0]) == "sl-link" {
		os.Exit(runLink(os.Args[1:]))
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "link":
			os.Exit(runLink(os.Args[2:]))
		case "prune":
			os.Exit(runPrune(os.Args[2:]))
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	var (
		port      = flag.String("port", "7777", "public port, the one `tailscale serve` publishes (hub role, loopback only)")
		localPort = flag.String("local-port", defaultLocalPort, "local-only port for opening files and changing links (never publish it)")
		links     = flag.String("links", filepath.Join(home, ".config", "shortlinker", "links.txt"), "path to links file (hub role)")
		hub       = flag.String("hub", "", "hub base URL, e.g. https://mac.tailnet.ts.net; setting this runs an opener agent only")
	)
	flag.Parse()

	var store *Store
	if *hub == "" {
		store = &Store{Path: *links}
	}
	srv := NewServer(store, *hub, *localPort, home)

	local := "127.0.0.1:" + *localPort
	errc := make(chan error, 2)
	go func() { errc <- http.ListenAndServe(local, srv.LocalHandler()) }()
	if store != nil {
		public := "127.0.0.1:" + *port
		go func() { errc <- http.ListenAndServe(public, srv.PublicHandler()) }()
		log.Printf("shortlinker hub on http://%s (public) and http://%s (local only; links: %s)", public, local, *links)
	} else {
		log.Printf("shortlinker agent on http://%s (hub: %s)", local, *hub)
	}
	log.Fatal(<-errc)
}
