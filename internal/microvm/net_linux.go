//go:build linux

package microvm

import (
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
)

// Each microVM gets a tap device of its own on a private /30. Pail's rules
// let a guest out to the internet through NAT, and nowhere else: not the
// home network, not other guests, not this machine's own services.
//
// There are two exceptions. A group, a pail's containers, may reach each
// other and find each other by name. And a guest of a pail that the person
// running Pail has named in PAIL_ALLOW_LAN may reach the home network.

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
	// next is where the search for a free /30 starts. It moves on each
	// time, so an address a guest just gave up isn't handed straight to
	// another while something may still be talking to it.
	next int
	// groups are the sets of guests that may reach each other, by name.
	groups map[string]*group
}

// group is a set of guests that may reach each other: a pail's containers.
// Each has an address kept for it from when the first of them starts until
// the last has stopped, so one that restarts comes back where the others
// expect it.
type group struct {
	// index is each member's /30, by its name.
	index map[string]int
	// up are the members that are running.
	up map[string]*tap
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

	// rules are this guest's exceptions: what lets it reach its group, or
	// the home network.
	rules [][]string
	// The rest is for a guest in a group: the group, the guest's name in
	// it, and every member's address by name.
	group string
	name  string
	Hosts map[string]string
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

// slots is how many /30s guestNet holds.
const slots = 1 << 14

// free takes a /30 nothing is using. The caller holds n.mu.
func (n *network) free() (int, error) {
	if n.used == nil {
		n.used = map[int]bool{}
	}
	if len(n.used) >= slots {
		return 0, fmt.Errorf("too many microVMs at once")
	}
	index := n.next % slots
	for n.used[index] {
		index = (index + 1) % slots
	}
	n.next = index + 1
	n.used[index] = true
	return index, nil
}

// guestIP is the guest's address in a /30: .0 is the network, .1 this
// machine, .2 the guest.
func guestIP(index int) string {
	return fmt.Sprintf("172.30.%d.%d", index/64, index%64*4+2)
}

// acquire makes a tap device for one guest, which nothing but Pail reaches.
func (n *network) acquire() (*tap, error) {
	return n.join("", "", nil, false)
}

// join makes a tap device for one guest. Given a group, the guest may reach
// the group's other members, and they it: name is the guest's own name
// among them, and peers every member's. With lan, it may reach the home
// network too.
func (n *network) join(groupName, name string, peers []string, lan bool) (*tap, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.setup(); err != nil {
		return nil, err
	}

	var g *group
	var index int
	if groupName == "" {
		var err error
		if index, err = n.free(); err != nil {
			return nil, err
		}
	} else {
		var err error
		if g, err = n.groupOf(groupName, name, peers); err != nil {
			return nil, err
		}
		if g.up[name] != nil {
			return nil, fmt.Errorf("%s is already running", name)
		}
		index = g.index[name]
	}
	// undo gives back what was taken, if the tap can't be made.
	undo := func() {
		if g == nil {
			delete(n.used, index)
		} else {
			n.leave(groupName)
		}
	}

	hi, lo := index/64, index%64*4
	t := &tap{
		n: n, index: index,
		Name:     fmt.Sprintf("%s%d", tapPrefix, index),
		HostIP:   fmt.Sprintf("172.30.%d.%d", hi, lo+1),
		GuestIP:  guestIP(index),
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
			undo()
			return nil, err
		}
	}

	// The guest's exceptions go ahead of the rules that keep guests in.
	var specs [][]string
	if lan {
		// Anywhere but other guests, who stay out of reach, and this
		// machine's own services, which PAIL-IN goes on refusing.
		specs = append(specs, []string{"PAIL-FWD", "-i", t.Name, "-s", t.GuestIP, "!", "-d", guestNet, "-j", "ACCEPT"})
	}
	if g != nil {
		t.group, t.name, t.Hosts = groupName, name, map[string]string{}
		for peer, i := range g.index {
			t.Hosts[peer] = guestIP(i)
			if peer == name {
				continue
			}
			// Only what goes to another guest's tap: an address in guestNet
			// whose guest isn't up must not find its way out to a network
			// that uses the same one.
			specs = append(specs, []string{"PAIL-FWD", "-i", t.Name, "-o", tapPrefix + "+", "-s", t.GuestIP, "-d", guestIP(i), "-j", "ACCEPT"})
		}
	}
	for _, spec := range specs {
		if err := run("iptables", append([]string{"-I", spec[0], "1"}, spec[1:]...)...); err != nil {
			t.unrule()
			exec.Command("ip", "link", "del", t.Name).Run()
			undo()
			return nil, err
		}
		t.rules = append(t.rules, spec)
	}
	if g != nil {
		g.up[name] = t
	}
	return t, nil
}

// groupOf finds a group, making it for its first member: every member gets
// an address then, so each can be told where the others will be. The caller
// holds n.mu.
func (n *network) groupOf(groupName, name string, peers []string) (*group, error) {
	if g := n.groups[groupName]; g != nil {
		if _, ok := g.index[name]; !ok {
			return nil, fmt.Errorf("%s isn't one of %s", name, groupName)
		}
		return g, nil
	}
	names := append([]string{name}, peers...)
	sort.Strings(names)
	g := &group{index: map[string]int{}, up: map[string]*tap{}}
	for _, peer := range names {
		if _, ok := g.index[peer]; ok {
			continue
		}
		index, err := n.free()
		if err != nil {
			for _, taken := range g.index {
				delete(n.used, taken)
			}
			return nil, err
		}
		g.index[peer] = index
	}
	if n.groups == nil {
		n.groups = map[string]*group{}
	}
	n.groups[groupName] = g
	return g, nil
}

// leave forgets a group none of whose members is running, and gives back
// their addresses. The caller holds n.mu.
func (n *network) leave(groupName string) {
	g := n.groups[groupName]
	if g == nil || len(g.up) > 0 {
		return
	}
	for _, index := range g.index {
		delete(n.used, index)
	}
	delete(n.groups, groupName)
}

// unrule takes back a guest's exceptions.
func (t *tap) unrule() {
	for _, spec := range t.rules {
		exec.Command("iptables", append([]string{"-D"}, spec...)...).Run()
	}
	t.rules = nil
}

func (t *tap) release() {
	t.unrule()
	exec.Command("ip", "link", "del", t.Name).Run()
	t.n.mu.Lock()
	defer t.n.mu.Unlock()
	if t.group == "" {
		delete(t.n.used, t.index)
		return
	}
	// The address stays the group's while any of it is running.
	if g := t.n.groups[t.group]; g != nil && g.up[t.name] == t {
		delete(g.up, t.name)
		t.n.leave(t.group)
	}
}
