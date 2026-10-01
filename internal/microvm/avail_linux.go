//go:build linux

package microvm

import (
	"os"
	"os/exec"
)

func (r *Runner) available() (bool, string) {
	if os.Geteuid() != 0 {
		return false, "microVMs need Pail to run as root, to give each one its own network"
	}
	kvm, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return false, "this machine has no /dev/kvm that Pail can open, so it can't run microVMs"
	}
	kvm.Close()
	if _, err := os.Stat("/dev/net/tun"); err != nil {
		return false, "this machine has no /dev/net/tun, which microVM networking needs"
	}
	if _, err := exec.LookPath(r.cfg.Firecracker); err != nil {
		return false, "the firecracker binary isn't at " + r.cfg.Firecracker + " (set PAIL_FIRECRACKER)"
	}
	if _, err := os.Stat(r.cfg.Kernel); err != nil {
		return false, "there is no guest kernel at " + r.cfg.Kernel + " (set PAIL_KERNEL)"
	}
	for _, tool := range []string{"mkfs.ext4", "debugfs", "ip", "iptables"} {
		if _, err := exec.LookPath(tool); err != nil {
			return false, "this machine is missing " + tool + ", which Pail uses to prepare microVMs"
		}
	}
	return true, ""
}
