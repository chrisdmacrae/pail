---
title: Running on Docker
navLabel: Docker
lead: "One script runs Pail in a container on the machine you’re at: storage and the server inside it, and builds, functions and containers beside it."
section: Running Pail
order: 4
provider: docker
next:
  href: /deploying/
  label: What Pail deploys
---

## What the script does

It starts one container, from one image, that holds everything Pail is made of:

- **versitygw**, storing in a volume and listening only inside the container.
- **pail-server**, on port 8080 of your machine.

It also gives that container Docker’s socket. Pail uses it to run builds, functions and containers as containers of their own, beside Pail’s, on a network they share.

This is Pail for the machine you work on. There are no virtual machines here: what you deploy is kept apart by Docker alone, shares your machine’s kernel, and can reach your network. That is fine for your own code. For a server that runs other people’s, use [Proxmox](/running/proxmox/), where each build and container gets a virtual machine of its own.

## Before you start

- **Docker, running.** Docker Desktop on a Mac, Docker Engine on Linux. `docker info` answers when it is.
- **bash and curl**, which a Mac and most Linux systems have.
- **Port 8080 free**, or another you choose.

## Run it

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

The script shows what it is about to make and asks before changing anything. Then it fetches the image, starts Pail, and waits for it to come up.

When it finishes it prints three things:

- **The address**, `http://localhost:8080`.
- **The token.** Keep it somewhere safe; it is the only key to this Pail.
- **Whether builds are on.** The line starts “this pail can run containers”. If it says “static files only”, the reason follows; see below.

Open the address, give it the token, and Pail is yours. Every pail gets a name under `localhost`, such as `http://hello.localhost:8080`. Browsers send every name under `localhost` to your own machine, so there is no DNS to set up and no certificate to trust.

Then [deploy something](/deploying/).

## Choosing your own settings

Everything has a default. To change one, set it in front of the command.

```bash
PAIL_PORT=9000 bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

| Setting | Default | What it sets |
| --- | --- | --- |
| `PAIL_ENGINE` | whichever is running | `docker` or `podman`, when you have both. |
| `PAIL_NAME` | `pail` | The container’s name, and its network’s. Its volume is this with `-data` after it. |
| `PAIL_PORT` | `8080` | The port Pail answers on. |
| `PAIL_BIND` | `127.0.0.1` | The address that port is on. `0.0.0.0` lets other devices on your network in. |
| `PAIL_BASE_DOMAIN` | `localhost` | The domain pails get names under. |
| `PAIL_TLS`, `PAIL_HTTPS_PORT` | `off`, `8443` | `on` serves HTTPS as well, on that port. |
| `PAIL_VERSION` | `latest` | A release to run, such as `v0.1.0`. |
| `PAIL_SOCKET` | found by asking Docker | Docker’s socket, where the script can’t find it. |
| `PAIL_S3_ENDPOINT`, `PAIL_S3_ACCESS_KEY`, `PAIL_S3_SECRET_KEY` | versitygw, in the container | Another S3 store to keep everything in. See [other storage](/running/storage/). |
| `YES` | unset | `1` skips the question before it starts. |

Any of [Pail’s own settings](/running/#settings) set the same way, such as `PAIL_MAX_UPLOAD_SIZE`, is handed to Pail as it is.

> **For the rest of your network.** To reach this Pail from other devices, give it a real base domain, the usual ports, and every address: `PAIL_BASE_DOMAIN=pail.lan PAIL_BIND=0.0.0.0 PAIL_PORT=80 PAIL_TLS=on PAIL_HTTPS_PORT=443`. Then [After it’s running](/running/#after-its-running) applies as it does to any Pail.

## Looking after it

**See the log.**

```bash
docker logs -f pail
```

**See the token again.**

```bash
docker exec pail cat /data/token
```

**Update Pail, or change a setting.** Run the script again, with the settings you want. It replaces the container and keeps the data. Sites are away for the moment that takes.

**Stop it.** This removes Pail’s container and every container it started. The data stays, and running the script again carries on from it.

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)" pail down
```

**Back it up.** Everything Pail knows is in the volume `pail-data`. Containers that keep data have a volume each, named `pail-vol.` and then the pail and the container.

## What is different from a server

Pail in a container deploys the same things, from the same `pail.json`. Three things differ.

- **Isolation.** Covered above: Docker’s, not a virtual machine’s.
- **Functions wake differently.** There are no snapshots to wake from. A copy is a container, and starts in a fraction of a second anyway.
- **Deploys don’t move.** What a deploy keeps for a container is not what it keeps for a virtual machine. A Pail’s data moved between the two serves its sites as before; anything that runs has to be deployed again.

## When it doesn’t work

**“… is installed but isn’t running”.** Start Docker Desktop, or on Linux, `sudo systemctl start docker`. If Docker is running and your user can’t reach it, `docker info` says why.

**Pail is up, but says “static files only”.** The reason is on the same line. Most often Pail can’t reach Docker’s socket. On Docker Desktop, check that *Allow the default Docker socket to be used* is on, under Settings, Advanced. Anywhere else, say where the socket is with `PAIL_SOCKET`.

**The port is taken.** Something else has 8080. Pick another with `PAIL_PORT`.

**A name under localhost doesn’t open.** Browsers and `curl` send `hello.localhost` to your own machine by themselves; some other tools ask your resolver, which may not know. Use a browser, or give Pail a base domain your resolver does know.
