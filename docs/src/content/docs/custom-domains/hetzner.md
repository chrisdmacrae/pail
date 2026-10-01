---
title: Hetzner
lead: "Get Pail its Let’s Encrypt certificates through Hetzner: make an API token, point your names at Pail, and set three variables."
section: Custom domains
order: 26
provider: hetzner
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- The domain’s DNS zone must be in a project in the Hetzner Console.
- At your registrar, set the domain’s nameservers to Hetzner’s.
- An API token from Hetzner. It reaches the whole Hetzner project the token belongs to.

## Make the API token

1. In the Hetzner Console, open the project that holds your DNS zone.
2. Go to **Security**, then **API tokens**, and generate a token.
3. Give it **Read & Write** permission.
4. Copy the token. Hetzner shows it once.

> **Zones in the old DNS Console need moving first.** Pail talks to the DNS that lives in the Hetzner Console. A zone still managed at dns.hetzner.com has to be migrated into a Console project before this token can edit it.

> **Give DNS its own project.** The token can change everything in its project. Keeping the zone in a project with nothing else in it limits what the token reaches.

Dashboards change. If a label here doesn’t match what you see, Hetzner’s own guide is the one to trust: [Hetzner: generate an API token](https://docs.hetzner.com/cloud/api/getting-started/generating-api-token/).

## Point your names at Pail

In Hetzner’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on.

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
PAIL_ACME_DNS_PROVIDER=hetzner
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
