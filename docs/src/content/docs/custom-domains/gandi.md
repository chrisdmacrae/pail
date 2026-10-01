---
title: Gandi
lead: "Get Pail its Let’s Encrypt certificates through Gandi: make a personal access token, point your names at Pail, and set three variables."
section: Custom domains
order: 25
provider: gandi
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## What you need

- The domain must be at Gandi and use Gandi’s LiveDNS nameservers, which is the default for domains bought there.
- A personal access token from Gandi. It reaches only the domains and permissions you pick.

## Make the personal access token

1. In your Gandi account, open your user settings and find **Personal Access Token (PAT)**.
2. Create a token for the organisation that owns the domain.
3. Limit it to your domain, and give it the permission to **manage domain name technical configurations**.
4. Pick an expiry, create the token and copy it. Gandi shows it once.

> **Gandi’s tokens always expire.** A personal access token lasts a year at most. After that Pail can’t renew certificates. Put a reminder in your calendar to make a new one and update `PAIL_ACME_DNS_TOKEN`.

> **Not the old API key.** Gandi’s older API keys are being retired. Pail uses a personal access token.

Dashboards change. If a label here doesn’t match what you see, Gandi’s own guide is the one to trust: [Gandi: personal access tokens](https://docs.gandi.net/en/managing_an_organization/organizations/personal_access_token.html).

## Point your names at Pail

In Gandi’s DNS for `example.com`, add two records. Both point to the address of the server Pail runs on.

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
PAIL_ACME_DNS_PROVIDER=gandi
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
