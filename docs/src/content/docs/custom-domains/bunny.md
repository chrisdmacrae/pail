---
title: Bunny
lead: "Get Pail its Let’s Encrypt certificates through Bunny: make an API key, point your names at Pail, and set three variables."
section: Custom domains
order: 20
provider: bunny
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- Add your domain as a DNS zone in the Bunny dashboard, under **DNS**.
- At your registrar, set the domain’s nameservers to the ones Bunny gives you for the zone.
- An API key from Bunny. It reaches the whole Bunny account. Bunny has no key that is limited to DNS.

## Make the API key

1. Sign in to the Bunny dashboard.
2. Open your account settings from the profile menu and find the **API** section.
3. Copy the account **API key**.

> **It’s the account key.** There is one API key per Bunny account, and it can change anything in it. Keep it only in Pail’s environment.

Dashboards change. If a label here doesn’t match what you see, Bunny’s own guide is the one to trust: [Bunny’s API overview](https://docs.bunny.net/reference/bunnynet-api-overview).

## Point your names at Pail

In Bunny’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on.

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
PAIL_ACME_DNS_PROVIDER=bunny
PAIL_ACME_DNS_TOKEN=your-api-key
```

The first start waits a minute or two while Pail proves the domain and collects its certificate. When the log says `pail is up` with `tls=acme`, open `https://pail.example.com`.

While you’re getting it working, [try it against Let’s Encrypt’s staging service first](/custom-domains/#set-it-up) so a mistake doesn’t use up real attempts.

## Custom hostnames

Any pail can now take extra hostnames, as long as each one is in a DNS zone this API key can edit.

```bash
pail hosts add blog blog.example.com
```

If something goes wrong, [When it doesn’t work](/custom-domains/#when-it-doesnt-work) lists the usual causes.

