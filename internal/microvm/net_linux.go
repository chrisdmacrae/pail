//go:build linux

package microvm

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// Each microVM gets a tap device of its own on a private /30. Pail's rules
// let a guest out to the internet through NAT, and nowhere else: not the
// home network, not other guests, not this machine's own services.

const (
	tapPrefix = "pailvm"
	// guestNet holds every guest's /30. It sits inside 172.16.0.0/12, which
	// guests are forbidden to reach, so one guest can't reach another.
	guestNet = "172.30.0.0/16"
)

// private are the ranges a guest may not reach: home networks, link-local,
// and carrier-grade NAT, which mesh VPNs use.
var private = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "169.254.0.0/16", "100.64.0.0/10"}

type network struct {
	mu    sync.Mutex
	ready bool
	used  map[int]bool
}

// tap is one guest's network.
type tap struct {
	n         *network
	index     int
	Name      string
	HostIP    string // the gateway the guest sees
	GuestIP   string
	GuestMAC  string
	bootParam string
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// rule adds an iptables rule unless it is already there.
func rule(table, chain string, spec ...string) error {
	check := append([]string{"-t", table, "-C", chain}, spec...)
	if exec.Command("iptables", check...).Run() == nil {
		return nil
	}
	return run("iptables", append([]string{"-t", table, "-A", chain}, spec...)...)
}

// setup puts Pail's firewall rules in place, once.
func (n *network) setup() error {
	if n.ready {
		return nil
	}
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0o644); err != nil {
		return fmt.Errorf("can't turn on IP forwarding: %w", err)
	}
	any := tapPrefix + "+"
	// Chains of Pail's own, so its rules are easy to see and to remove.
	for _, chain := range []string{"PAIL-FWD", "PAIL-IN"} {
		exec.Command("iptables", "-N", chain).Run() // fails if it exists; fine
		if err := run("iptables", "-F", chain); err != nil {
			return err
		}
	}
	var rules [][]string
	// Replies to what a guest started may come back to it.
	rules = append(rules, []string{"PAIL-FWD", "-o", any, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"})
	for _, cidr := range private {
		rules = append(rules, []string{"PAIL-FWD", "-i", any, "-d", cidr, "-j", "DROP"})
	}
	rules = append(rules,
		[]string{"PAIL-FWD", "-i", any, "-j", "ACCEPT"},
		// Nothing else may open a connection to a guest through this machine.
		[]string{"PAIL-FWD", "-o", any, "-j", "DROP"},
		// A guest may answer this machine, but not call it.
		[]string{"PAIL-IN", "-i", any, "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
		[]string{"PAIL-IN", "-i", any, "-j", "DROP"},
	)
	for _, r := range rules {
		if err := run("iptables", append([]string{"-A", r[0]}, r[1:]...)...); err != nil {
			return err
		}
	}
	for chain, mine := range map[string]string{"FORWARD": "PAIL-FWD", "INPUT": "PAIL-IN"} {
		if exec.Command("iptables", "-C", chain, "-j", mine).Run() != nil {
			if err := run("iptables", "-I", chain, "1", "-j", mine); err != nil {
				return err
			}
		}
	}
	if err := rule("nat", "POSTROUTING", "-s", guestNet, "!", "-o", any, "-j", "MASQUERADE"); err != nil {
		return err
	}
	n.ready = true
	return nil
}

// acquire makes a tap device for one guest.
func (n *network) acquire() (*tap, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.setup(); err != nil {
		return nil, err
	}
	if n.used == nil {
		n.used = map[int]bool{}
	}
	index := 0
	for n.used[index] {
		index++
	}
	if index >= 1<<14 {
		return nil, fmt.Errorf("too many microVMs at once")
	}
	// The index picks a /30: .0 is the network, .1 this machine, .2 the guest.
	hi, lo := index/64, index%64*4
	t := &tap{
		n: n, index: index,
		Name:     fmt.Sprintf("%s%d", tapPrefix, index),
		HostIP:   fmt.Sprintf("172.30.%d.%d", hi, lo+1),
		GuestIP:  fmt.Sprintf("172.30.%d.%d", hi, lo+2),
		GuestMAC: fmt.Sprintf("06:00:ac:1e:%02x:%02x", hi, lo+2),
	}
	// The kernel configures the guest's address itself from this.
	t.bootParam = fmt.Sprintf("ip=%s::%s:255.255.255.252::eth0:off", t.GuestIP, t.HostIP)

	exec.Command("ip", "link", "del", t.Name).Run() // one left over from a crash
	steps := [][]string{
		{"tuntap", "add", "dev", t.Name, "mode", "tap"},
		{"addr", "add", t.HostIP + "/30", "dev", t.Name},
		{"link", "set", t.Name, "up"},
	}
	for _, step := range steps {
		if err := run("ip", step...); err != nil {
			exec.Command("ip", "link", "del", t.Name).Run()
			return nil, err
		}
	}
	n.used[index] = true
	return t, nil
}

func (t *tap) release() {
	exec.Command("ip", "link", "del", t.Name).Run()
	t.n.mu.Lock()
	delete(t.n.used, t.index)
	t.n.mu.Unlock()
}
