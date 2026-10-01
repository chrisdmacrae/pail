---
title: Running Pail
lead: "What Pail needs to run on a server of your own, and the ways to set it up."
section: Running Pail
order: 2
providerTiles: true
next:
  href: /git-providers/
  label: Set up a git provider
---

## What Pail is made of

A Pail installation is two programs on one Linux machine.

- **pail-server** is Pail itself: the web UI, the API that `pail-cli` talks to, and every pail’s site, all from one listener on ports 80 and 443.
- **versitygw** is where Pail keeps everything: each deploy’s files, its own records, certificates and git connections. It is a small S3 gateway over an ordinary folder.

For builds, Pail also uses **Firecracker**, which runs each build in a small virtual machine of its own so it can’t touch the server.

## What the machine needs

| | |
| --- | --- |
| Linux, on amd64 or arm64 | A container, a virtual machine or a real machine. |
| Ports 80 and 443 | Pail serves every pail from them. Nothing else on the machine can hold them. |
| An address on your network that doesn’t change | Names point at it. |
| `/dev/kvm` | Only for builds. Without it Pail still serves sites you build yourself. |
| Root | Pail gives each build’s virtual machine a network of its own, which only root may do. |

Pail does not need to be reachable from the internet.

## After it’s running

Whichever way you set Pail up, three things come next.

1. **Point names at it.** Every pail lives at a name under Pail’s base domain, `pail.lan` unless you chose another. On your home resolver, send that domain and everything under it to Pail’s address. For dnsmasq or Pi-hole, that is one line:

   ```
   address=/pail.lan/10.0.0.50
   ```

2. **Open it and give it the token.** Go to `https://pail.lan`. Pail asks once for its token, the secret you set it up with.

3. **Trust its certificates.** Out of the box Pail signs its own, so each device has to trust it once. The page at `https://pail.lan/trust` has the steps for each kind of device. To skip this for good, [put Pail on a domain you own](/custom-domains/) and it uses Let’s Encrypt instead.

## Settings

Pail is configured entirely by environment variables. There is no settings screen and no config file to learn.

| Variable | Default | What it sets |
| --- | --- | --- |
| `PAIL_TOKEN` | none, required | The installation’s secret. Every client sends it; Pail won’t start without it. |
| `PAIL_BASE_DOMAIN` | `pail.lan` | The domain every pail gets a name under. |
| `PAIL_S3_ENDPOINT` | none, required | Where versitygw listens, such as `http://127.0.0.1:7070`. |
| `PAIL_S3_ACCESS_KEY`, `PAIL_S3_SECRET_KEY` | none, required | versitygw’s credentials. |
| `PAIL_MAX_UPLOAD_SIZE` | `100MB` | The largest upload, or repo, Pail accepts. |
| `PAIL_MAX_DEPLOYS` | `10` | How many good deploys each pail keeps to roll back to. |
| `PAIL_DATA_DIR` | `/var/lib/pail` | Local disk for what builds need. |
| `PAIL_FIRECRACKER`, `PAIL_KERNEL` | `firecracker`, `<data dir>/vmlinux` | The Firecracker binary and the kernel its virtual machines boot. |
| `PAIL_TLS` | on | `off` serves plain HTTP only, for running behind a proxy that handles HTTPS itself. |

[Custom domains](/custom-domains/) and [git providers](/git-providers/) have settings of their own, covered in their guides.

To rotate the token, change `PAIL_TOKEN` and restart Pail. Every client is locked out until it is given the new one.
