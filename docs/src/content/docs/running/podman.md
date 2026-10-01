---
title: Running on Podman
navLabel: Podman
lead: "The same script that runs Pail on Docker runs it on Podman, with or without root."
section: Running Pail
order: 5
provider: podman
next:
  href: /deploying/
  label: What Pail deploys
---

## What the script does

It starts one container that holds **versitygw** and **pail-server**, on port 8080 of your machine, and gives it Podman’s socket. Pail uses the socket to run builds, functions and containers as containers of their own, beside Pail’s.

Podman answers the same API Docker does, so this is the setup [Running on Docker](/running/docker/) describes, and everything there holds here: what is different from a server, the settings, and looking after it, with `podman` in place of `docker`. This page covers what is Podman’s own.

## Before you start

- **Podman, running.** `podman info` answers when it is.
- **On a Mac, a machine.** Podman runs containers in a small virtual machine there. Make and start one if you haven’t:

  ```bash
  podman machine init --memory 4096
  ```

  ```bash
  podman machine start
  ```

- **Port 8080 free**, or another you choose.

> **Give builds room.** A build may use 2GB of memory. A Podman machine has 2GB in all unless told otherwise, which is tight. For one you already have: `podman machine stop`, `podman machine set --memory 4096`, `podman machine start`.

## Run it

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

The script uses whichever of Docker and Podman is running. With both, say which:

```bash
PAIL_ENGINE=podman bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

It shows what it is about to make and asks before changing anything. When it finishes it prints the address, `http://localhost:8080`, the token, and whether builds are on. Open the address, give it the token, and [deploy something](/deploying/).

[The settings](/running/docker/#choosing-your-own-settings) are the ones on the Docker page.

## Podman’s socket

Pail talks to Podman over its API socket. The script finds it by asking Podman, and you’ll see where in what it prints before it starts.

- **On a Mac**, the socket is inside Podman’s machine and always on.
- **On Linux**, Podman has no socket until it is asked for one. The script turns it on for your user. To do it yourself:

  ```bash
  systemctl --user enable --now podman.socket
  ```

  Your user’s services stop when you log out. To keep Pail up on a machine you aren’t logged in to:

  ```bash
  loginctl enable-linger $USER
  ```

## Without root

Podman runs as you, not as root, and Pail is happy with that. Two things follow on Linux.

- **Ports under 1024 are root’s.** The default, 8080, is fine. For 80 and 443, run the script as root, or lower `net.ipv4.ip_unprivileged_port_start`.
- **Limits need cgroups v2.** A container’s memory and CPU limits come from `pail.json`. On an old system that can’t set them for your user, Pail runs the container without them rather than not at all.

## Dockerfiles

Podman builds a Dockerfile itself. Where Docker takes `FROM python:3.13` to mean Docker Hub’s, Podman may not know which registry you mean, and can’t ask while Pail is building. Write the name in full:

```dockerfile
FROM docker.io/library/python:3.13
```

An `image` in `pail.json` needs no such care; Pail fetches those, and reads a short name as Docker does.

## Looking after it

As on Docker, with `podman` in front: `podman logs -f pail` for the log, `podman exec pail cat /data/token` for the token, and the script again to update. To stop and remove Pail, keeping its data:

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)" pail down
```

## When it doesn’t work

**“Podman is installed but its machine isn’t running”.** `podman machine start`. If it says the machine is already running and `podman info` still fails, stop it and start it again.

**“Podman’s socket isn’t at …”.** On Linux, turn it on as above.

**Pail is up, but says “static files only”.** The reason is on the same line. If Pail can’t reach the socket, say where it is with `PAIL_SOCKET`: the path as Podman’s machine sees it, on a Mac.

**A build exits with status 137.** It ran out of memory. Give the machine more, as above.

**A Dockerfile’s build can’t find its image.** Write the image’s name in full, as above.
