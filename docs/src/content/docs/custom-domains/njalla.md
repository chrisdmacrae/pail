---
title: Njalla
lead: "Get Pail its Let’s Encrypt certificates through Njalla: make an API token, point your names at Pail, and set three variables."
section: Custom domains
order: 28
provider: njalla
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- The domain must be registered with Njalla and use Njalla’s DNS.
- An API token from Njalla. It reaches the Njalla account the token belongs to.

## Make the API token

1. Sign in at njal.la and open **Settings**, then **API**.
2. Add a token.
3. Copy it.

Dashboards change. If a label here doesn’t match what you see, Njalla’s own guide is the one to trust: [Njalla: API](https://njal.la/api/).

## Point your names at Pail

In Njalla’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on; a private address like `10.0.0.50` is fine.

| Type | Name | Points to |
| --- | --- | --- |
| A | `pail` | Pail’s address |
| A | `*.pail` | Pail’s address |

That makes `pail.example.com` Pail’s base domain, and a pail called blog lives at `blog.pail.example.com`. Use another name in place of `pail` if you like.

## Tell Pail

Set these wherever you set Pail’s environment, then start Pail.

```
PAIL_BASE_DOMAIN=pail.example.com
PAIL_ACME_DNS_PROVIDER=njalla
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
