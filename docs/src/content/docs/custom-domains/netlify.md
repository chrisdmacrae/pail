---
title: Netlify
lead: "Get Pail its Let’s Encrypt certificates through Netlify: make a personal access token, point your names at Pail, and set three variables."
section: Custom domains
order: 27
provider: netlify
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- Add the domain to Netlify DNS for your team.
- At your registrar, set the domain’s nameservers to the ones Netlify lists for it.
- A personal access token from Netlify. It reaches the whole Netlify account. Netlify has no token that is limited to DNS.

## Make the personal access token

1. In Netlify, open **User settings**, then **Applications**.
2. Under **Personal access tokens**, choose **New access token**.
3. Name it, pick an expiry, generate it and copy it. Netlify shows it once.

> **An expired token stops renewals.** If you set an expiry, make a new token and update `PAIL_ACME_DNS_TOKEN` before it runs out.

Dashboards change. If a label here doesn’t match what you see, Netlify’s own guide is the one to trust: [Netlify: API authentication](https://docs.netlify.com/api/get-started/#authentication).

## Point your names at Pail

In Netlify’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on; a private address like `10.0.0.50` is fine.

| Type | Name | Points to |
| --- | --- | --- |
| A | `pail` | Pail’s address |
| A | `*.pail` | Pail’s address |

That makes `pail.example.com` Pail’s base domain, and a pail called blog lives at `blog.pail.example.com`. Use another name in place of `pail` if you like.

## Tell Pail

Set these wherever you set Pail’s environment, then start Pail.

```
PAIL_BASE_DOMAIN=pail.example.com
PAIL_ACME_DNS_PROVIDER=netlify
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
