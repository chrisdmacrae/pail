---
title: Duck DNS
lead: "Get Pail its Let’s Encrypt certificates through Duck DNS: make a token, point your names at Pail, and set three variables."
section: Custom domains
order: 24
provider: duckdns
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- Sign in at duckdns.org and add a subdomain, such as `garden`. Your name is then `garden.duckdns.org`.
- A token from Duck DNS. It reaches every Duck DNS name on your account.

## Make the token

1. Sign in at duckdns.org.
2. Your token is shown at the top of the page. Copy it.

Dashboards change. If a label here doesn’t match what you see, Duck DNS’s own guide is the one to trust: [Duck DNS: spec](https://www.duckdns.org/spec.jsp).

## Point your names at Pail

Duck DNS has no records to add. Set your subdomain’s **current ip** to the address of the server Pail runs on. A private address like `10.0.0.50` is fine. Duck DNS then sends `garden.duckdns.org` and every name under it to that address.

> **Issuing takes a little longer.** Duck DNS holds one verification record at a time, so Pail proves the base domain and its wildcard one after the other.

## Tell Pail

Set these wherever you set Pail’s environment, then start Pail.

```
PAIL_BASE_DOMAIN=garden.duckdns.org
PAIL_ACME_DNS_PROVIDER=duckdns
PAIL_ACME_DNS_TOKEN=your-token
```

The first start waits a minute or two while Pail proves the domain and collects its certificate. When the log says `pail is up` with `tls=acme`, open `https://garden.duckdns.org`.

While you’re getting it working, [try it against Let’s Encrypt’s staging service first](/custom-domains/#set-it-up) so a mistake doesn’t use up real attempts.

## Custom hostnames

With Duck DNS, a custom hostname has to be another Duck DNS name on the same account, such as `recipes.duckdns.org`. The token can’t speak for a domain hosted anywhere else.

```bash
pail hosts add blog recipes.duckdns.org
```

If something goes wrong, [When it doesn’t work](/custom-domains/#when-it-doesnt-work) lists the usual causes.
