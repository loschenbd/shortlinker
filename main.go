package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	// `sl-link <path>` (a symlink to this binary) and `shortlinker link
	// <path>` print a link for a file or folder instead of running a server.
	if filepath.Base(os.Args[0]) == "sl-link" {
		os.Exit(runLink(os.Args[1:]))
	}
	if code, ok := runCommand(os.Args[1:]); ok {
		os.Exit(code)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	var (
		port      = flag.String("port", "7777", "public port, the one `tailscale serve` publishes (hub role, loopback only)")
		localPort = flag.String("local-port", defaultLocalPort, "local-only port for opening files and the link API (never publish it)")
		links     = flag.String("links", filepath.Join(configDir(home), LinksFile), "path to the links file (hub role); "+ClicksFile+" sits next to it")
		hub       = flag.String("hub", "", "hub base URL, e.g. https://mac.tailnet.ts.net; setting this runs an opener agent only")
	)
	flag.Usage = func() {
		fmt.Fprint(os.Stderr, usage+"\nServer flags:\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	var store *Store
	if *hub == "" {
		store = &Store{Path: *links, FlushDelay: 2 * time.Second}
		m, err := store.Migrate(home)
		if err != nil {
			log.Fatalf("moving older links files into %s: %v", *links, err)
		}
		for _, moved := range []struct {
			n          int
			what, from string
		}{
			{m.FromAuto, "sl-link records", autoFile + " (kept as " + autoFile + ".before-merge)"},
			{m.FromText, "sl-link links", legacyAutoFile + " (kept as " + legacyAutoFile + ".before-records)"},
			{m.FromSplit, "sl-link links", "the end of " + handFile},
			{m.FromHand, "of your links, now kept,", handFile + " (kept as " + handFile + ".before-records)"},
		} {
			if moved.n > 0 {
				log.Printf("moved %d %s into %s from %s", moved.n, moved.what, LinksFile, moved.from)
			}
		}
		store.Tighten()
		stop := make(chan os.Signal, 1)
		signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
		go func() { // launchd stops the hub with SIGTERM: save click counts first
			<-stop
			if err := store.Flush(); err != nil {
				log.Printf("saving click counts: %v", err)
			}
			os.Exit(0)
		}()
	}
	srv := NewServer(store, *hub, *localPort, home)
	if store != nil {
		token, err := loadOrCreateToken(tokenPath(home))
		if err != nil {
			log.Fatalf("hub token: %v", err)
		}
		srv.auth = newAPIAuth(token)
		if savedHubURL(home) == "" {
			go publicHubURL(home) // fills ~/.config/shortlinker/url for the manage page's links
		}
		srv.devices = &devices{}
		go func() { // hourly: device names, and the retention pass
			for {
				if err := srv.devices.refresh(); err != nil {
					log.Printf("can't name tailnet devices yet: %v", err)
				}
				if archived, err := store.Archive(); err != nil {
					log.Printf("archiving unused links: %v", err)
				} else if len(archived) > 0 {
					log.Printf("archived %d links nobody used (they still work, and come back when clicked)", len(archived))
				}
				time.Sleep(time.Hour)
			}
		}()
	}

	serve := func(addr string, h http.Handler) error {
		s := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
		return s.ListenAndServe()
	}
	local := "127.0.0.1:" + *localPort
	errc := make(chan error, 2)
	go func() { errc <- serve(local, srv.LocalHandler()) }()
	if store != nil {
		public := "127.0.0.1:" + *port
		go func() { errc <- serve(public, srv.PublicHandler()) }()
		log.Printf("shortlinker hub on http://%s (public) and http://%s (local only; links: %s)", public, local, *links)
	} else {
		log.Printf("shortlinker agent on http://%s (hub: %s)", local, *hub)
	}
	log.Fatal(<-errc)
}
