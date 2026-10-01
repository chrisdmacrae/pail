---
title: Running Pail
lead: "What Pail needs to run on a server of your own, and the ways to set it up."
section: Running Pail
order: 2
providerTiles: true
next:
  href: /deploying/
  label: What Pail deploys
---

## What Pail is made of

A Pail installation is two programs on one Linux machine.

- **pail-server** is Pail itself: the web UI, the API that `pail-cli` talks to, and every pail’s site, all from one listener on ports 80 and 443.
- **versitygw** is where Pail keeps everything: each deploy’s files, its own records, certificates and git connections. It is a small S3 gateway over an ordinary folder.

For builds, functions and containers, Pail also uses **Firecracker**, which runs each one in a small virtual machine of its own so it can’t touch the server.

On a machine with no KVM, your laptop for one, Pail runs them in **Docker’s or Podman’s containers** instead. That is how the two guides for those set it up. It suits your own code on your own machine; a virtual machine keeps code you don’t trust further away.

## What the machine needs

| | |
| --- | --- |
| Linux, on amd64 or arm64 | A container, a virtual machine or a real machine. |
| Ports 80 and 443 | Pail serves every pail from them. Nothing else on the machine can hold them. |
| An address on your network that doesn’t change | Names point at it. |
| `/dev/kvm` | Only for builds, functions and containers. Without it Pail still serves sites you build yourself, or runs them with Docker or Podman. |
| Root | Pail gives each virtual machine a network of its own, which only root may do. |

On Docker or Podman, none of this is yours to arrange: the script picks a port on your own machine, and pails get names under `localhost`.

Pail does not need to be reachable from the internet.

## After it’s running

On a server, three things come next. Pail on Docker or Podman, at `localhost`, needs none of them.

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
| `PAIL_MAX_CONTAINER_MEMORY` | `2GB` | The most memory one container may ask for. |
| `PAIL_DATA_DIR` | `/var/lib/pail` | Local disk for what builds and containers need, and where containers’ data is kept. |
| `PAIL_RUNTIME` | `auto` | What builds, functions and containers run in: `firecracker` for virtual machines, `container` for Docker or Podman, or `auto` for virtual machines where the machine can run them and containers where it can’t. |
| `PAIL_FIRECRACKER`, `PAIL_KERNEL` | `firecracker`, `<data dir>/vmlinux` | The Firecracker binary and the kernel its virtual machines boot. |
| `PAIL_CONTAINER_SOCKET`, `PAIL_CONTAINER_NETWORK` | `/var/run/docker.sock`, `pail` | Docker’s or Podman’s socket, and the network of theirs that Pail and its containers share. |
| `PAIL_TLS` | on | `off` serves plain HTTP only, for running behind a proxy that handles HTTPS itself, such as a [Cloudflare Tunnel](/custom-domains/cloudflare-tunnel/). Pails can take custom hostnames then, because the proxy holds their certificates. |

[Custom domains](/custom-domains/) and [git providers](/git-providers/) have settings of their own, covered in their guides.

To rotate the token, change `PAIL_TOKEN` and restart Pail. Every client is locked out until it is given the new one.
