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
- **A private address works at home only.** The name resolves everywhere, but only devices on your network can connect to the address behind it. To open Pail from anywhere, see [Reach Pail from outside your network](#reach-pail-from-outside-your-network).
- **One certificate covers every pail.** Pail gets a wildcard for its base domain, so `blog.pail.example.com` and every pail after it are covered from the start.
- **Extra hostnames get their own.** Add `blog.example.com` to a pail and Pail gets that name a certificate too.
- **Renewal is automatic.** Pail replaces each certificate well before it runs out.

> **Behind a proxy that handles HTTPS,** none of this is needed. With `PAIL_TLS=off`, Pail serves plain HTTP, the proxy holds the certificates, and pails can take extra hostnames without a DNS token. A [Cloudflare Tunnel](/custom-domains/cloudflare-tunnel/) is the usual case.

## Before you start

You need three things:

- **A domain whose DNS is hosted with one of the providers above.** Where you bought it doesn’t matter; where its DNS records live does.
- **An API token from that provider** that can edit the domain’s DNS records. Each provider’s guide shows how to make one.
- **A name for Pail under that domain.** This is its base domain. With `pail.example.com`, a pail called blog lives at `blog.pail.example.com`.

## Set it up

1. **Make the token.** Follow the guide for your provider, then come back here.

2. **Point the names at Pail.** Add two records, both to the address of the server Pail runs on. A private address like `10.0.0.50` is fine if Pail only needs to work on your home network. For anywhere else, see [Reach Pail from outside your network](#reach-pail-from-outside-your-network).

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

## Reach Pail from outside your network

A private address only means something on your own network. Away from home, `pail.example.com` still resolves, but nothing answers at the address it gives. There are three ways to change that. The first two keep Pail’s certificates as they are. The third hands them to Cloudflare.

| Way | Who can reach Pail | What it needs |
| --- | --- | --- |
| A VPN | Your own devices | A VPN app on each device |
| Port forwarding | Anyone | A public address from your internet provider |
| A Cloudflare Tunnel | Anyone | Your domain on Cloudflare. No Let’s Encrypt. |

### Keep it private with a VPN

Leave the records on the private address and join your devices to your home network instead. A VPN such as Tailscale or WireGuard, set up to carry your home network’s addresses, lets a phone or laptop reach Pail from anywhere while nothing is open to the internet. Only devices on the VPN get in.

### Forward ports from your router

Your router has the one public address your home gets. Forwarding tells it to hand connections on two ports to Pail.

1. **Give Pail’s server a fixed address.** Reserve its address in your router’s DHCP settings, or set a fixed one on the server. A forward points at an address, and stops working if the server gets another.

2. **Forward the ports.** In your router’s settings, look for **Port forwarding**, **Virtual servers** or **NAT**. Forward TCP ports `443` and `80` to Pail’s address, each to the same port. Pail serves everything on `443`; `80` is there so a plain `http://` link gets sent to HTTPS.

3. **Point the records at your public address.** Your router’s status page shows it, usually as the WAN or internet address. Change both records from the private address to that one.

   | Type | Name | Points to |
   | --- | --- | --- |
   | A | `pail.example.com` | Your public address |
   | A | `*.pail.example.com` | Your public address |

4. **Try it from outside.** Turn off Wi-Fi on a phone and open `https://pail.example.com` over mobile data. Testing from inside your network doesn’t prove anything.

> **This puts Pail on the internet.** The sign-in page and every pail can be reached by anyone. Pail’s token is the only key, so keep it long and keep it to yourself.

**If your public address changes.** Most home connections get a new address now and then, and the records go stale when it does. Many routers can keep a record up to date by themselves: look for **Dynamic DNS** in the router’s settings. If your name is with [Duck DNS](/custom-domains/duckdns/), this is what it is made for.

**If your provider shares one address between customers.** Some providers, most mobile and rural ones among them, put many homes behind one public address. Port forwarding can’t work there. Compare the WAN address your router shows with the one a site such as `ifconfig.me` reports. If they differ, or the router’s begins with `10.`, `192.168.`, or `100.64.` to `100.127.`, you are behind one. Use a [Cloudflare Tunnel](/custom-domains/cloudflare-tunnel/) or a VPN instead.

**If names stop opening at home.** Some routers can’t send a connection out to their own public address and back in. If Pail opens on mobile data but not on your Wi-Fi, add the two records on your home resolver, pointing at Pail’s private address. Devices at home then connect straight to it.

### Use a Cloudflare Tunnel

A small program on your network connects out to Cloudflare, and Cloudflare sends visitors down that connection. No ports are opened, and it works behind a shared address. Cloudflare holds the certificates, so Pail needs no Let’s Encrypt and no DNS token. Uploads through it are limited in size. [Cloudflare Tunnel](/custom-domains/cloudflare-tunnel/) has the steps.

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

**Pail opens at home but nowhere else.** The records point at a private address, which only your network can reach. See [Reach Pail from outside your network](#reach-pail-from-outside-your-network).

**The browser warns about the certificate.** If `PAIL_ACME_DIRECTORY` still points at staging, remove it and restart.

**A hostname says “Not pointing here yet”.** Its DNS record is missing, or hasn’t spread. Pail checks again by itself; the pail starts answering there as soon as the name resolves to Pail.

## Changing your mind

Take `PAIL_ACME_DNS_PROVIDER` and `PAIL_ACME_DNS_TOKEN` away and Pail goes back to signing its own certificates. Hostnames you added stay on their pails, but browsers will warn on them: Pail’s own authority can only vouch for names under the base domain.
