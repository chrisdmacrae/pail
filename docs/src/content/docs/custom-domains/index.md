---
title: Set up a custom domain
lead: "Put Pail on a domain you own, with certificates every device already trusts, and give any pail extra hostnames of its own."
navLabel: Set up a domain
section: Custom domains
order: 10
providerTiles: true
---

## How it works

Out of the box, every pail lives under `pail.lan` and Pail signs its own certificates, which each device has to trust once. Give Pail a domain you own and it gets its certificates from Let’s Encrypt instead.

- **Pail proves the domain is yours through your DNS provider.** It asks the provider’s API to publish a short-lived record, and Let’s Encrypt checks for it. That is what the API token is for.
- **Nothing has to be reachable from the internet.** Pail can sit on a private address with no ports open. Only the DNS record is public.
- **One certificate covers every pail.** Pail gets a wildcard for its base domain, so `blog.pail.example.com` and every pail after it are covered from the start.
- **Extra hostnames get their own.** Add `blog.example.com` to a pail and Pail gets that name a certificate too.
- **Renewal is automatic.** Pail replaces each certificate well before it runs out.

## Before you start

You need three things:

- **A domain whose DNS is hosted with one of the providers above.** Where you bought it doesn’t matter; where its DNS records live does.
- **An API token from that provider** that can edit the domain’s DNS records. Each provider’s guide shows how to make one.
- **A name for Pail under that domain.** This is its base domain. With `pail.example.com`, a pail called blog lives at `blog.pail.example.com`.

## Set it up

1. **Make the token.** Follow the guide for your provider, then come back here.

2. **Point the names at Pail.** Add two records, both to the address of the server Pail runs on. A private address like `10.0.0.50` is fine.

   | Type | Name | Points to |
   | --- | --- | --- |
   | A | `pail.example.com` | Pail’s address |
   | A | `*.pail.example.com` | Pail’s address |

   You can add them at your DNS provider or on your home resolver. Either works, because Let’s Encrypt never connects to Pail.

   > **If names won’t resolve at home.** Some routers drop public DNS answers that point at private addresses. If `pail.example.com` resolves elsewhere but not on your network, add the two records on your home resolver, or allow the domain in the router’s DNS rebinding protection.

3. **Tell Pail.** Set these wherever you set Pail’s environment: a compose file, a systemd `EnvironmentFile`, or your shell.

   ```
   PAIL_BASE_DOMAIN=pail.example.com
   PAIL_ACME_DNS_PROVIDER=cloudflare
   PAIL_ACME_DNS_TOKEN=your-token
   PAIL_ACME_EMAIL=you@example.com
   ```

   | Variable | What it sets |
   | --- | --- |
   | `PAIL_BASE_DOMAIN` | The name you chose for Pail. |
   | `PAIL_ACME_DNS_PROVIDER` | Your provider: `bunny`, `cloudflare`, `desec`, `digitalocean`, `duckdns`, `gandi`, `hetzner`, `netlify` or `njalla`. |
   | `PAIL_ACME_DNS_TOKEN` | The token you made. |
   | `PAIL_ACME_EMAIL` | Optional. Where Let’s Encrypt sends a warning if a certificate is about to expire. |

   The provider and the token go together. Pail won’t start with only one of them set.

4. **Try it against staging first.** Let’s Encrypt limits how many certificates it issues, and a wrong token can use up attempts. Its staging service has generous limits, so add this line while you’re getting things working:

   ```
   PAIL_ACME_DIRECTORY=https://acme-staging-v02.api.letsencrypt.org/directory
   ```

   Staging certificates aren’t trusted by browsers, so expect a warning. Once Pail starts cleanly, remove the line and restart. Pail keeps staging and real certificates apart, so it fetches real ones without any clean-up.

5. **Start Pail.** The first start waits while the DNS record spreads, usually under a minute or two. The log says what it’s doing:

   ```
   asking for a certificate; this waits for DNS
   got a certificate
   pail is up  tls=acme
   ```

   Then open `https://pail.example.com`. Later starts reuse the certificate and don’t wait.

## Add a hostname to a pail

Once Pail is on Let’s Encrypt, any pail can answer at other names too.

In the web UI, open the pail and use **Add a hostname** under Addresses. From a terminal:

```bash
pail hosts add blog blog.example.com
```

Either way takes a minute or two, because Pail gets the name its certificate before adding it. Then send the name to Pail with a `CNAME` to the base domain, or an `A` record to Pail’s address. To see whether it has arrived:

```bash
pail hosts blog
```

Each hostname shows **Points here** or **Not pointing here yet**.

> **The token has to cover the name.** Pail gets the certificate through the same token, so the hostname must be in a DNS zone that token can edit. If it isn’t, Pail refuses the hostname and says so.

## When it doesn’t work

**Pail won’t start, and says the provider “isn’t one Pail knows”.** `PAIL_ACME_DNS_PROVIDER` has a typo, or names a provider Pail doesn’t support. The message lists the ones it does.

**Pail won’t start, and says the two variables “go together”.** Only one of `PAIL_ACME_DNS_PROVIDER` and `PAIL_ACME_DNS_TOKEN` is set. Set both.

**Pail won’t start, and says it “can’t get a certificate”.** The rest of the message comes from your provider or Let’s Encrypt. The usual causes:

- The token is wrong, has expired, or can’t edit DNS records for this domain.
- The domain’s DNS isn’t hosted with the provider you named.
- Your home resolver answers for the domain itself, so Pail never sees the public record appear. Tell Pail to check with public resolvers instead:

  ```
  PAIL_ACME_RESOLVERS=1.1.1.1:53,8.8.8.8:53
  ```

**A hostname is refused with “couldn’t get a certificate”.** It isn’t in a zone the token can edit. Widen the token to include that zone, or move the hostname’s DNS to the same provider.

**The browser warns about the certificate.** If `PAIL_ACME_DIRECTORY` still points at staging, remove it and restart.

**A hostname says “Not pointing here yet”.** Its DNS record is missing, or hasn’t spread. Pail checks again by itself; the pail starts answering there as soon as the name resolves to Pail.

## Changing your mind

Take `PAIL_ACME_DNS_PROVIDER` and `PAIL_ACME_DNS_TOKEN` away and Pail goes back to signing its own certificates. Hostnames you added stay on their pails, but browsers will warn on them: Pail’s own authority can only vouch for names under the base domain.
