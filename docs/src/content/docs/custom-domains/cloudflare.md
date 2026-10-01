---
title: Cloudflare
lead: "Get Pail its Let’s Encrypt certificates through Cloudflare: make an API token, point your names at Pail, and set three variables."
section: Custom domains
order: 21
provider: cloudflare
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- Add your domain to Cloudflare and switch its nameservers to the pair Cloudflare assigns.
- An API token from Cloudflare. It reaches only the zones you pick, and only their DNS records.

## Make the API token

1. In the Cloudflare dashboard, open **My Profile**, then **API Tokens**, and choose **Create Token**.
2. Choose **Create Custom Token**.
3. Add two permissions: **Zone · Zone · Read** and **Zone · DNS · Edit**.
4. Under **Zone Resources**, choose **Include · Specific zone** and pick your domain. Add every zone you want custom hostnames from.
5. Create the token and copy it. Cloudflare shows it once.

> **Use a token, not the Global API Key.** Pail takes an API token. The Global API Key is a different kind of credential and won’t work here.

Dashboards change. If a label here doesn’t match what you see, Cloudflare’s own guide is the one to trust: [Cloudflare: create an API token](https://developers.cloudflare.com/fundamentals/api/get-started/create-token/).

## Point your names at Pail

In Cloudflare’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on.

A private address like `10.0.0.50` is fine if Pail only needs to work on your home network. Away from home, nothing can connect to it. To open Pail from anywhere, see [Reach Pail from outside your network](/custom-domains/#reach-pail-from-outside-your-network).

| Type | Name | Points to |
| --- | --- | --- |
| A | `pail` | Pail’s address |
| A | `*.pail` | Pail’s address |

That makes `pail.example.com` Pail’s base domain, and a pail called blog lives at `blog.pail.example.com`. Use another name in place of `pail` if you like.

> **Keep these records DNS only.** Turn the proxy off for both (the grey cloud). Cloudflare can’t proxy to a private address on your network. To send visitors through Cloudflare instead, with no ports open at home, use a [Cloudflare Tunnel](/custom-domains/cloudflare-tunnel/). That needs no API token: Cloudflare holds the certificates.

## Tell Pail

Set these wherever you set Pail’s environment, then start Pail.

```
PAIL_BASE_DOMAIN=pail.example.com
PAIL_ACME_DNS_PROVIDER=cloudflare
PAIL_ACME_DNS_TOKEN=your-api-token
```

The first start waits a minute or two while Pail proves the domain and collects its certificate. When the log says `pail is up` with `tls=acme`, open `https://pail.example.com`.

While you’re getting it working, [try it against Let’s Encrypt’s staging service first](/custom-domains/#set-it-up) so a mistake doesn’t use up real attempts.

## Custom hostnames

Any pail can now take extra hostnames, as long as each one is in a DNS zone this API token can edit.

```bash
pail hosts add blog blog.example.com
```

If something goes wrong, [When it doesn’t work](/custom-domains/#when-it-doesnt-work) lists the usual causes.
