---
title: Keep storage somewhere else
navLabel: Other storage
lead: "Pail keeps everything in versitygw, on its own disk, unless you point it at another S3 store: a NAS, MinIO, or a bucket at a cloud provider."
section: Running Pail
order: 6
next:
  href: /deploying/
  label: What Pail deploys
---

## When to do this

Out of the box, every way of running Pail sets up **versitygw** beside it and keeps everything there: each deploy’s files, Pail’s own records, certificates and git connections. For most installations that is the right place, and there is nothing to do.

Point Pail at another store when you already have one you look after and back up, or when Pail’s own disk is too small for what you deploy.

Two things to know first.

- **Every page comes from the store.** Pail reads a site’s files from storage as they are asked for. A store on your own network is as quick as a disk. One across the internet makes every page as slow as the trip there.
- **Not everything moves.** What builds, functions and containers need while they run, and the data containers keep, stays on Pail’s own disk, in its data folder.

## What the store has to do

Any store that speaks S3 will do. Pail asks it to put, get, copy, list and delete objects in one bucket, and to read part of an object. It signs requests the way S3 does now, with version 4 signatures.

Pail makes the bucket if it isn’t there. Where your keys can’t make buckets, make it yourself first.

## The settings

| Variable | Default | What it sets |
| --- | --- | --- |
| `PAIL_S3_ENDPOINT` | versitygw’s | The store’s address, such as `https://s3.home.example` or `http://10.0.0.20:9000`. |
| `PAIL_S3_ACCESS_KEY`, `PAIL_S3_SECRET_KEY` | versitygw’s | The keys Pail signs its requests with. |
| `PAIL_S3_BUCKET` | `pail` | The bucket Pail keeps everything in. |
| `PAIL_S3_REGION` | `us-east-1` | The region the store expects requests to be signed for. A store with no regions takes any. |
| `PAIL_S3_ADDRESSING` | `auto` | Where the bucket’s name goes in a request: `path` for after the host, `virtual` for in front of it. `auto` uses `virtual` for Amazon’s, Google’s and Alibaba’s stores, and `path` for every other. |

Give Pail a bucket of its own. It keeps nothing outside it, and expects nothing else inside it.

## Set it up

### On Proxmox

For a new Pail, hand the installer the store. It then leaves versitygw out.

```bash
S3_ENDPOINT=https://s3.home.example S3_ACCESS_KEY=pail S3_SECRET_KEY=... \
  bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-proxmox.sh)"
```

`S3_BUCKET`, `S3_REGION` and `S3_ADDRESSING` go the same way.

For a Pail that is already running, change the `PAIL_S3_` lines in [its settings file](/running/proxmox/#pails-settings-file), and restart Pail.

### On Docker or Podman

Set them in front of the script. The container then runs Pail alone, with no versitygw in it.

```bash
PAIL_S3_ENDPOINT=https://s3.home.example PAIL_S3_ACCESS_KEY=pail PAIL_S3_SECRET_KEY=... \
  bash -c "$(curl -fsSL https://github.com/chrisdmacrae/pail/releases/latest/download/pail-container.sh)"
```

The address has to be one Pail’s container can reach. `localhost` there is the container itself, not your machine.

Run the script again without them and Pail goes back to the versitygw in its container, and to whatever it kept there before.

## With a few stores

These are the values each store asks for. Its own documentation says where to make the keys.

| Store | `PAIL_S3_ENDPOINT` | `PAIL_S3_REGION` |
| --- | --- | --- |
| MinIO, Garage, a NAS | Its address on your network, such as `http://10.0.0.20:9000`. | Whatever the store is set to; often anything will do. |
| Amazon S3 | `https://s3.<region>.amazonaws.com` | The bucket’s region, such as `eu-west-1`. |
| Cloudflare R2 | `https://<account id>.r2.cloudflarestorage.com` | `auto` |
| Backblaze B2 | `https://s3.<region>.backblazeb2.com` | The region in that address, such as `us-west-004`. |

## Moving a Pail that has pails

Changing the settings moves nothing. A Pail pointed at an empty bucket starts as a new one, with no pails, and what it had stays where it was.

To keep what you have, copy the old bucket into the new one before you change the settings, with a tool that speaks S3 to both, such as `rclone`. Copy everything in the bucket, under the same names. Stop Pail while you copy, so that nothing changes halfway.

On Proxmox the old store is versitygw, at `http://127.0.0.1:7070` inside the container, with the keys in `/etc/pail/versitygw.env`. Its bucket is `pail`.

## When it doesn’t work

Pail checks the store as it starts, and stops with the reason if it can’t use it. The reason is in Pail’s log.

**“can’t reach the bucket …”.** The address, the keys or the region is wrong, or the store can’t be reached from where Pail runs. The end of the line is what the store itself said.

**“there is no bucket …, and these keys can’t make it”.** Make the bucket at the store, or name one that exists with `PAIL_S3_BUCKET`.

**It starts, and every pail is gone.** The bucket is empty: the settings changed, and the data didn’t move. Put the old settings back and they return.
