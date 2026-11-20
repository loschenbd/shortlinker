package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// devices names tailnet addresses ("100.101.102.103" is "iphone") and knows
// this Mac's own tailnet names. It's filled from `tailscale status` in the
// background: the macOS CLI can take seconds, so clicks never wait on it.
type devices struct {
	mu    sync.RWMutex
	names map[string]string // tailnet address → machine name
	self  []string          // this Mac's DNS names, lowercase, no trailing dot
}

// refresh reads `tailscale status --json`.
func (d *devices) refresh() error {
	bin := tailscaleBin()
	out, err := exec.Command(bin, "status", "--json").Output()
	if err != nil {
		return fmt.Errorf("%s status: %v", bin, err)
	}
	if i := bytes.IndexByte(out, '{'); i > 0 { // a warning line before the JSON
		out = out[i:]
	}
	if err := d.parse(out); err != nil {
		return fmt.Errorf("%s status printed something that isn't JSON: %.80q", bin, out)
	}
	return nil
}

type tsNode struct {
	HostName     string
	DNSName      string
	TailscaleIPs []string
}

func (d *devices) parse(statusJSON []byte) error {
	var status struct {
		Self *tsNode
		Peer map[string]*tsNode
	}
	if err := json.Unmarshal(statusJSON, &status); err != nil {
		return err
	}
	names := map[string]string{}
	add := func(n *tsNode) {
		if n == nil {
			return
		}
		for _, ip := range n.TailscaleIPs {
			names[ip] = machineName(n)
		}
	}
	add(status.Self)
	for _, p := range status.Peer {
		add(p)
	}
	var self []string
	if status.Self != nil && status.Self.DNSName != "" {
		self = append(self, strings.ToLower(strings.TrimSuffix(status.Self.DNSName, ".")))
	}
	d.mu.Lock()
	d.names, d.self = names, self
	d.mu.Unlock()
	return nil
}

// machineName is the name the admin console shows: the first label of the
// device's DNS name, else its host name.
func machineName(n *tsNode) string {
	if label, _, _ := strings.Cut(n.DNSName, "."); label != "" {
		return label
	}
	return n.HostName
}

// name is the device at a tailnet address, or "" if it isn't known.
func (d *devices) name(addr string) string {
	if d == nil {
		return ""
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.names[addr]
}

// selfNames are this Mac's tailnet names, if they're known yet.
func (d *devices) selfNames() []string {
	if d == nil {
		return nil
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.self
}

// tailscaleBin finds the tailscale CLI. A launchd job's PATH leaves out
// /usr/local/bin, where the Mac app installs it, and the app's own binary
// only works as a CLI from a login session ("The Tailscale GUI failed to
// start"), so it comes last.
func tailscaleBin() string {
	if bin, err := exec.LookPath("tailscale"); err == nil {
		return bin
	}
	for _, bin := range []string{"/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale"} {
		if fi, err := os.Stat(bin); err == nil && fi.Mode()&0o111 != 0 {
			return bin
		}
	}
	return "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
}
