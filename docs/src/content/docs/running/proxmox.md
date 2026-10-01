---
title: Running on Proxmox
navLabel: Proxmox
lead: "One script sets Pail up in a container on your Proxmox host: storage, the server, and everything builds need."
section: Running Pail
order: 3
provider: proxmox
next:
  href: /running/#after-its-running
  label: After it’s running
---

## What the script does

Run on a Proxmox host, it makes one unprivileged LXC container and, inside it, installs and starts everything Pail needs:

- **versitygw**, as a service, storing in `/var/lib/versitygw` and listening only inside the container.
- **pail-server**, as a service, on ports 80 and 443.
- **Firecracker** and a guest kernel, with `/dev/kvm` and `/dev/net/tun` passed through from the host, so Pail can run builds.

A container is the best place for Pail on Proxmox. It shares the host’s KVM directly, so builds run at full speed. In a virtual machine they would need nested virtualization, and run slower.

## Before you start

- **Proxmox VE 8 or later**, and a root shell on the host.
- **Virtualization turned on** in the host’s firmware. If Proxmox runs virtual machines for you already, it is.
- **A free address** for the container, from DHCP with a reservation, or one you choose.

## Run it

In a root shell on the Proxmox host:

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-proxmox.sh)"
```

The script shows what it is about to make and asks before changing anything. Then it downloads a Debian template, makes the container, installs Pail, and waits for it to come up. That takes a few minutes.

When it finishes it prints three things you need:

- **The container’s address**, to point names at.
- **The token.** It is shown once. Keep it somewhere safe; it is the only key to this Pail.
- **Whether builds are on.** The line starts “this pail can run microVMs”. If it says “static files only”, the reason follows; see below.

Then carry on with [After it’s running](/running/#after-its-running).

## Choosing your own settings

Everything has a default. To change one, set it in front of the command.

```bash
IP=10.0.0.50/24 GATEWAY=10.0.0.1 BASE_DOMAIN=pail.home.example \
  bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-proxmox.sh)"
```

| Setting | Default | What it sets |
| --- | --- | --- |
| `CTID` | the next free ID | The container’s ID. |
| `CT_HOSTNAME` | `pail` | The container’s hostname. |
| `STORAGE` | `local-lvm` | The storage its disk goes on. |
| `TEMPLATES` | `local` | The storage that holds container templates. |
| `BRIDGE` | `vmbr0` | The network bridge. |
| `IP`, `GATEWAY` | `dhcp` | A fixed address such as `10.0.0.50/24`, and its gateway. |
| `CORES`, `MEMORY`, `DISK` | `4`, `4096`, `32` | Cores, memory in MB, disk in GB. |
| `BASE_DOMAIN` | `pail.lan` | The domain pails get names under. |
| `PAIL_VERSION` | `latest` | A release to install, such as `v0.1.0`. |
| `ACME_DNS_PROVIDER`, `ACME_DNS_TOKEN`, `ACME_EMAIL` | unset | Let’s Encrypt certificates from the start. See [custom domains](/custom-domains/). |
| `YES` | unset | `1` skips the question before it starts. |

> **Give builds room.** A build gets 2 cores and 2GB of memory while it runs. The defaults leave space for that; a much smaller container will serve sites well but struggle to build them.

## Looking after it

Everything lives inside the container. From the Proxmox host, with the container’s ID in place of `101`:

**See the log.**

```bash
pct exec 101 -- journalctl -u pail -f
```

**Change a setting.** Pail’s settings are in one file. Edit it, then restart Pail.

```bash
pct exec 101 -- nano /etc/pail/pail.env
```

```bash
pct exec 101 -- systemctl restart pail
```

**Update Pail.** This fetches the newest release and restarts Pail. Sites are away for the moment the restart takes.

```bash
pct exec 101 -- pail-update
```

**Back it up.** Back the container up as you would any other in Proxmox. Everything Pail knows is on its disk: `/var/lib/versitygw` holds every pail, and `/etc/pail` its settings.

## When it doesn’t work

**“this host has no /dev/kvm”.** Virtualization is off in the host’s firmware, or Proxmox is itself running inside a virtual machine without nested virtualization. Pail can’t run builds there.

**“no pail-server at …”.** The release the script looked for isn’t there. Check `PAIL_VERSION`.

**“container … has no network”.** The container started but can’t reach the internet. Check `BRIDGE`, and `IP` and `GATEWAY` if you set them.

**Pail is up, but says “static files only”.** The reason is on the same line of the log. Most often the container can’t open `/dev/kvm`. On the host, check the container’s configuration has the two devices:

```bash
pct config 101 | grep -E "dev|lxc"
```

**Names don’t resolve.** That is your home resolver, not Pail. See [After it’s running](/running/#after-its-running).
