---
title: DigitalOcean
lead: "Get Pail its Let’s Encrypt certificates through DigitalOcean: make a personal access token, point your names at Pail, and set three variables."
section: Custom domains
order: 23
provider: digitalocean
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- Add your domain under **Networking**, then **Domains**.
- At your registrar, set the domain’s nameservers to DigitalOcean’s.
- A personal access token from DigitalOcean. It reaches only what you tick. Domain access is enough.

## Make the personal access token

1. In the DigitalOcean control panel, open **API**, then **Tokens**, and choose **Generate New Token**.
2. Give it a name and an expiry you’re comfortable with.
3. Choose **Custom Scopes** and tick every scope under **domain**: create, read, update and delete.
4. Generate the token and copy it. DigitalOcean shows it once.

> **An expired token stops renewals.** If you set an expiry, Pail can’t renew certificates after it. Make a new token and update `PAIL_ACME_DNS_TOKEN` before then.

Dashboards change. If a label here doesn’t match what you see, DigitalOcean’s own guide is the one to trust: [DigitalOcean: create a personal access token](https://docs.digitalocean.com/reference/api/create-personal-access-token/).

## Point your names at Pail

In DigitalOcean’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on.

A private address like `10.0.0.50` is fine if Pail only needs to work on your home network. Away from home, nothing can connect to it. To open Pail from anywhere, see [Reach Pail from outside your network](/custom-domains/#reach-pail-from-outside-your-network).

| Type | Name | Points to |
| --- | --- | --- |
| A | `pail` | Pail’s address |
| A | `*.pail` | Pail’s address |

That makes `pail.example.com` Pail’s base domain, and a pail called blog lives at `blog.pail.example.com`. Use another name in place of `pail` if you like.

## Tell Pail

Set these wherever you set Pail’s environment, then start Pail.

```
PAIL_BASE_DOMAIN=pail.example.com
PAIL_ACME_DNS_PROVIDER=digitalocean
PAIL_ACME_DNS_TOKEN=your-personal-access-token
```

The first start waits a minute or two while Pail proves the domain and collects its certificate. When the log says `pail is up` with `tls=acme`, open `https://pail.example.com`.

While you’re getting it working, [try it against Let’s Encrypt’s staging service first](/custom-domains/#set-it-up) so a mistake doesn’t use up real attempts.

## Custom hostnames

Any pail can now take extra hostnames, as long as each one is in a DNS zone this personal access token can edit.

```bash
pail hosts add blog blog.example.com
```

If something goes wrong, [When it doesn’t work](/custom-domains/#when-it-doesnt-work) lists the usual causes.
