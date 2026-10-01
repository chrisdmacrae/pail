---
title: deSEC
lead: "Get Pail its Let’s Encrypt certificates through deSEC: make a token, point your names at Pail, and set three variables."
section: Custom domains
order: 22
provider: desec
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- Either register a free name under `dedyn.io` at deSEC, or add a domain you own and point its nameservers at deSEC.
- A token from deSEC. It reaches every domain in the deSEC account, unless you narrow the token with a policy.

## Make the token

1. Sign in at desec.io and open **Token Management**.
2. Create a new token and give it a name, such as `pail`.
3. Copy the token’s secret. deSEC shows it once.

> **A dedyn.io name works as a base domain.** If your name is `garden.dedyn.io`, set `PAIL_BASE_DOMAIN` to it and your pails live at `blog.garden.dedyn.io`.

Dashboards change. If a label here doesn’t match what you see, deSEC’s own guide is the one to trust: [deSEC: tokens](https://desec.readthedocs.io/en/latest/auth/tokens.html).

## Point your names at Pail

In deSEC’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on.

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
PAIL_ACME_DNS_PROVIDER=desec
PAIL_ACME_DNS_TOKEN=your-token
```

The first start waits a minute or two while Pail proves the domain and collects its certificate. When the log says `pail is up` with `tls=acme`, open `https://pail.example.com`.

While you’re getting it working, [try it against Let’s Encrypt’s staging service first](/custom-domains/#set-it-up) so a mistake doesn’t use up real attempts.

## Custom hostnames

Any pail can now take extra hostnames, as long as each one is in a DNS zone this token can edit.

```bash
pail hosts add blog blog.example.com
```

If something goes wrong, [When it doesn’t work](/custom-domains/#when-it-doesnt-work) lists the usual causes.
