<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="web/design-system/assets/Brand/pail-wordmark-reversed.svg">
    <img src="web/design-system/assets/Brand/pail-wordmark.svg" alt="Pail" width="188" height="64">
  </picture>
</p>

<p align="center">
  Hosting for static sites, PWAs and small server apps, on a server of your own.
</p>

<p align="center">
  <a href="https://pail.chrisdmacrae.com">Documentation</a> ·
  <a href="https://pail.chrisdmacrae.com/quickstart/">Quickstart</a> ·
  <a href="https://github.com/chrisdmacrae/pail/releases/latest">Latest release</a>
</p>

## What Pail is

Pail puts your sites on your own machine: a homelab server, or the laptop you're at. Each thing it hosts is a **pail**: one name, one URL, one status.

- **Deploy a folder with one command.** `pail up` packs it, sends it, and prints the address. Or drop a folder or a `.zip` on the web UI.
- **Deploy from git.** Connect GitHub, GitLab, Bitbucket, Gitea or Forgejo, pick a repo, and every push is a deploy. One repo can hold several pails.
- **Builds.** A project whose `package.json` has a build script is built first, with npm, pnpm or yarn.
- **Server code.** A `pail.json` beside your site adds functions, which run once per request and sleep when nothing is asking, and containers, built from a Dockerfile or pulled from a registry and kept running.
- **Roll back.** Pail keeps your recent deploys. Serving an older one takes a moment and rebuilds nothing, and a deploy that fails leaves the last good one serving.
- **HTTPS.** Pail signs certificates from an authority of its own, or gets them from Let's Encrypt for a domain you own, which also lets a pail answer at your own hostnames.

## What it's made of

- **pail-server** is Pail itself, one Go binary: the web UI, the REST API, and every pail's site, routed by hostname from one listener.
- **Storage** is S3. Pail comes with [versitygw](https://github.com/versity/versitygw), a small S3 gateway over an ordinary folder, and can use any other S3 store instead.
- **[Firecracker](https://firecracker-microvm.github.io) microVMs** run builds, functions and containers, each in a small virtual machine of its own so that none can touch the server. This needs Linux with KVM.
- **Docker or Podman** runs them as containers instead, on a machine with no KVM.
- **pail-cli** is the `pail` command, for macOS, Linux and Windows.
- **The web UI** is React and TypeScript, built into the server.

Two libraries go with it: [astro-pail](adapters/astro), an Astro adapter that turns on-demand pages and API routes into a function, and [pail-fn](sdk/rust), a Rust crate for writing functions.

## Run it

Each way is one script. It shows what it is about to make, asks before changing anything, and ends by printing Pail's address and its token.

### On Proxmox

For a server at home. The script makes one unprivileged LXC container with everything in it, and passes KVM through so that each build, function and container gets a microVM. In a root shell on the Proxmox host:

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-proxmox.sh)"
```

[Running on Proxmox](https://pail.chrisdmacrae.com/running/proxmox/) covers its settings and looking after the container. [After it's running](https://pail.chrisdmacrae.com/running/#after-its-running) covers pointing names at it and trusting its certificates.

### On Docker

For the machine you're at. One container, at `http://localhost:8080`, with no DNS to set up and no certificate to trust. With Docker running:

```bash
bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

[Running on Docker](https://pail.chrisdmacrae.com/running/docker/) covers its settings, updating it and removing it.

### On Podman

The same script, which uses whichever of Docker and Podman is running. With both, say which:

```bash
PAIL_ENGINE=podman bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

[Running on Podman](https://pail.chrisdmacrae.com/running/podman/) covers Podman's machine on a Mac and its socket on Linux.

## Deploy something

Get pail-cli with Homebrew, on macOS or Linux, or download it from the [latest release](https://github.com/chrisdmacrae/pail/releases/latest):

```bash
brew install chrisdmacrae/tap/pail
```

Point it at your Pail once, with the address the script printed. It asks for the token.

```bash
pail login http://localhost:8080
```

Then, from a folder with an `index.html` in it:

```bash
pail up
```

## Documentation

Everything else is at [pail.chrisdmacrae.com](https://pail.chrisdmacrae.com).

| | |
| --- | --- |
| [Quickstart](https://pail.chrisdmacrae.com/quickstart/) | From nothing to your first pail. |
| [Running Pail](https://pail.chrisdmacrae.com/running/) | What the machine needs, and every setting. |
| [Storage](https://pail.chrisdmacrae.com/running/storage/) | Keeping everything in another S3 store. |
| [What Pail deploys](https://pail.chrisdmacrae.com/deploying/) | Files, builds and `pail.json`. |
| [Functions](https://pail.chrisdmacrae.com/deploying/functions/) · [Containers](https://pail.chrisdmacrae.com/deploying/containers/) | Server code beside your site. |
| [Git providers](https://pail.chrisdmacrae.com/git-providers/) | Deploying a repo on every push. |
| [Custom domains](https://pail.chrisdmacrae.com/custom-domains/) | Your own hostnames, and Let's Encrypt. |

## Contributing

[CONTRIBUTING.md](CONTRIBUTING.md) covers running Pail from a checkout, its tests, and how a release is made.
