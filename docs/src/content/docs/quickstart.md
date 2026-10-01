---
title: Quickstart
lead: "From nothing to your first pail: run Pail, get pail-cli, and put a folder in it with one command."
section: Start here
order: 1
next:
  href: /running/
  label: Running Pail
---

## 1. Run Pail

Pick where it runs. Each guide is one script, and ends with Pail’s address and its token.

- **[On Proxmox](/running/proxmox/)**, for a server at home. The script makes a container on your Proxmox host with everything in it, and each build, function and container you deploy gets a small virtual machine of its own.
- **[On Docker](/running/docker/)**, for the machine you’re at. One container, at `http://localhost:8080`, with nothing to set up around it.
- **[On Podman](/running/podman/)**, the same, where Podman is what you have.

To try Pail out, Docker or Podman is the quickest: with either running, this is all of it.

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

## 2. Get pail-cli

On a Mac or on Linux, with Homebrew:

```bash
brew install chrisdmacrae/tap/pail
```

Without Homebrew, or on Windows, download it from the [latest release](https://github.com/chrisdmacrae/pail/releases/latest) and put it somewhere on your `PATH`.

## 3. Point it at your Pail

Once, with the address the script printed. It asks for the token.

```bash
pail login http://localhost:8080
```

On Proxmox the address is `https://pail.lan`, after the three steps in [After it’s running](/running/#after-its-running).

## 4. Deploy a folder

From a folder with an `index.html` in it:

```bash
pail up
```

Pail prints the pail’s address, such as `http://my-site.localhost:8080`. Open it. Run `pail up` again after a change and the address serves the new files.

A folder of files is the start. Pail also builds a project that needs it, runs functions, and runs containers: [What Pail deploys](/deploying/) covers each.
