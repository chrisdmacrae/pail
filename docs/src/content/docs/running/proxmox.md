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

- **versitygw**, as a service, storing in `/var/lib/versitygw` and listening only inside the container. With [a store of your own](/running/storage/), it is left out.
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
| `S3_ENDPOINT`, `S3_ACCESS_KEY`, `S3_SECRET_KEY` | versitygw, in the container | Another S3 store to keep everything in, with `S3_BUCKET`, `S3_REGION` and `S3_ADDRESSING`. See [other storage](/running/storage/). |
| `YES` | unset | `1` skips the question before it starts. |

> **Give builds room.** A build gets 2 cores and 2GB of memory while it runs. The defaults leave space for that; a much smaller container will serve sites well but struggle to build them.

## Looking after it

Everything lives inside the container. From the Proxmox host, with the container’s ID in place of `101`:

**See the log.**

```bash
pct exec 101 -- journalctl -u pail -f
```

**Change a setting.** Pail’s settings are in one file, `/etc/pail/pail.env`. Edit it, then restart Pail. [Pail’s settings file](#pails-settings-file) covers it.

**Update Pail.** This fetches the newest release and restarts Pail. Sites are away for the moment the restart takes.

```bash
pct exec 101 -- pail-update
```

**Back it up.** Back the container up as you would any other in Proxmox. Everything Pail knows is on its disk: `/var/lib/versitygw` holds every pail, and `/etc/pail` its settings. With [a store of your own](/running/storage/), the pails are there instead, and the store is what to back up.

## Pail’s settings file

Pail is configured by environment variables, and on Proxmox they all come from one file inside the container: `/etc/pail/pail.env`. Pail’s service reads it each time it starts. Nothing else sets Pail up, so this file is where every setting in these docs goes.

### What is in it

The installer writes it. With the defaults it looks like this:

```
# Pail's settings, one NAME=value to a line. After changing any:
#   systemctl restart pail
PAIL_TOKEN=...
PAIL_BASE_DOMAIN=pail.lan
PAIL_S3_ENDPOINT=http://127.0.0.1:7070
PAIL_S3_ACCESS_KEY=pail
PAIL_S3_SECRET_KEY=...
PAIL_DATA_DIR=/var/lib/pail
PAIL_FIRECRACKER=/usr/local/bin/firecracker
PAIL_KERNEL=/var/lib/pail/vmlinux
```

What you gave the installer is there too: `PAIL_ACME_DNS_PROVIDER` and `PAIL_ACME_DNS_TOKEN` for Let’s Encrypt, and the other `PAIL_S3_` settings for a store of your own.

The file holds Pail’s token and its storage keys, so only root in the container can read it. Keep it that way.

### Change it

Open the file from the Proxmox host:

```bash
pct exec 101 -- nano /etc/pail/pail.env
```

Each setting is a line of its own, `NAME=value`. To add one, add a line; to go back to a default, remove the line.

- No spaces around the `=`, and no `export` in front.
- A value with a space in it goes in double quotes: `NAME="two words"`.
- A line that starts with `#` is a note, and is ignored.
- Nothing is worked out: `$HOME` in a value stays as those five characters.

To add one line without opening an editor:

```bash
pct exec 101 -- sh -c 'echo "PAIL_MAX_UPLOAD_SIZE=500MB" >> /etc/pail/pail.env'
```

### Restart Pail

Pail reads the file only as it starts, so a change does nothing until you restart it. Sites are away for the moment that takes.

```bash
pct exec 101 -- systemctl restart pail
```

Then check it came up:

```bash
pct exec 101 -- journalctl -u pail -n 20 --no-pager
```

The last lines should say “pail is up”. If a setting is wrong, Pail says which and why, and doesn’t start. Fix the line and restart it again.

### What you’ll change there

| To | Set |
| --- | --- |
| Change the token | `PAIL_TOKEN`. Every client is locked out until it is given the new one. |
| Move to another base domain | `PAIL_BASE_DOMAIN`, and point the new name at Pail. |
| Use Let’s Encrypt | `PAIL_ACME_DNS_PROVIDER`, `PAIL_ACME_DNS_TOKEN` and `PAIL_ACME_EMAIL`. See [custom domains](/custom-domains/). |
| Let people sign in to a git host | `PAIL_OAUTH_<HOST>_CLIENT_ID` and `_CLIENT_SECRET`. See [git providers](/git-providers/). |
| Allow bigger uploads | `PAIL_MAX_UPLOAD_SIZE`, such as `500MB`. |
| Keep storage somewhere else | The `PAIL_S3_` settings. See [other storage](/running/storage/). |

[Every setting](/running/#settings) Pail has can go in this file.

Two things the file doesn’t cover:

- **versitygw’s own keys** are in `/etc/pail/versitygw.env`. They have to match `PAIL_S3_ACCESS_KEY` and `PAIL_S3_SECRET_KEY` for as long as Pail uses it. After changing them, restart both: `systemctl restart versitygw pail`.
- **The container itself**, its cores, memory, disk and address, is Proxmox’s to change, from its web UI or with `pct set`.

Updating Pail with `pail-update` leaves the file as it is.

## When it doesn’t work

**“this host has no /dev/kvm”.** Virtualization is off in the host’s firmware, or Proxmox is itself running inside a virtual machine without nested virtualization. Pail can’t run builds there.

**“no pail-server at …”.** The release the script looked for isn’t there. Check `PAIL_VERSION`.

**“container … has no network”.** The container started but can’t reach the internet. Check `BRIDGE`, and `IP` and `GATEWAY` if you set them.

**Pail is up, but says “static files only”.** The reason is on the same line of the log. Most often the container can’t open `/dev/kvm`. On the host, check the container’s configuration has the two devices:

```bash
pct config 101 | grep -E "dev|lxc"
```

**Names don’t resolve.** That is your home resolver, not Pail. See [After it’s running](/running/#after-its-running).
