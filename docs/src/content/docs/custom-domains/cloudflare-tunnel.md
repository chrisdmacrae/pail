---
title: Cloudflare Tunnel
lead: "Open a Pail on your home network to the internet through Cloudflare, with no ports forwarded, no public address of your own, and no certificates for Pail to get."
section: Custom domains
order: 40
next:
  href: /custom-domains/#add-a-hostname-to-a-pail
  label: Add a hostname to a pail
---

## How it works

A Pail on a private address answers on your home network and nowhere else. A tunnel opens it to everyone without touching your router.

- **The connection goes out, not in.** A small program, `cloudflared`, runs on your network and connects to Cloudflare. Visitors connect to Cloudflare, and Cloudflare sends them down that connection.
- **It works behind a shared address.** Nothing is forwarded, so it doesn’t matter if your internet provider gives you no public address.
- **Cloudflare holds the certificates.** It answers HTTPS for your names, and passes each request to Pail over plain HTTP inside the tunnel. Pail runs with `PAIL_TLS=off`: no Let’s Encrypt, no DNS token.
- **Cloudflare reads the traffic.** It decrypts each request to pass it on. If that isn’t acceptable, [forward ports](/custom-domains/#forward-ports-from-your-router) or use a VPN instead.

> **This puts Pail on the internet.** The sign-in page and every pail can be reached by anyone. Pail’s token is the only key, so keep it long and keep it to yourself.

## What you need

- **A domain on Cloudflare,** with its nameservers switched to the pair Cloudflare assigns.
- **A machine at home that stays on,** to run `cloudflared`. The server Pail runs on is a fine choice.
- **A base domain Cloudflare’s certificate covers.** See the next section before going further.

## Check the base domain

Cloudflare’s free certificate covers your domain and one level under it: `example.com` and `*.example.com`. It doesn’t cover two levels, and that is where pails live when the base domain is `pail.example.com`.

| Base domain | A pail lives at | Cloudflare’s certificate |
| --- | --- | --- |
| `example.com` | `blog.example.com` | The free one covers it. |
| `pail.example.com` | `blog.pail.example.com` | Needs a paid one. |

Choose one:

- **Give Pail the whole domain.** Use `example.com` as the base domain. This suits a domain you keep for Pail alone, because every name under it goes to Pail.
- **Pay for the certificate.** Buy Advanced Certificate Manager for the domain in Cloudflare, then order a certificate for `pail.example.com` and `*.pail.example.com`.

The steps below use `pail.example.com`. With the whole domain, use `example.com` and `*.example.com` where they say `pail.example.com` and `*.pail.example.com`.

## Tell Pail

Set these wherever you set Pail’s environment, then restart Pail.

```
PAIL_BASE_DOMAIN=pail.example.com
PAIL_TLS=off
```

If `PAIL_ACME_DNS_PROVIDER` and `PAIL_ACME_DNS_TOKEN` are set, take them out. Pail gets no certificates with `PAIL_TLS=off`.

When the log says `pail is up` with `tls=off`, Pail is serving plain HTTP on port 80.

## Make the tunnel

1. In the Cloudflare dashboard, open **Networking**, then **Tunnels**, and choose **Create a tunnel**.
2. Give it a name, such as `home`, and create it.
3. Cloudflare shows a command with the tunnel’s token in it. Run it on the machine at home. It installs `cloudflared` and starts it as a service.
4. Wait for the dashboard to show the tunnel as connected.

## Send your names through it

1. **Remove any `A` records** for `pail` and `*.pail` in Cloudflare’s DNS. Cloudflare won’t add a tunnel’s record where another already has the name.

2. **Add two routes.** Open the tunnel, and on its **Routes** tab choose **Add route**, then **Published application**. Add one for each row. The service URL is Pail’s private address, over plain HTTP.

   | Subdomain | Domain | Service URL |
   | --- | --- | --- |
   | `pail` | `example.com` | `http://10.0.0.50` |
   | `*.pail` | `example.com` | `http://10.0.0.50` |

   If `cloudflared` runs on the same server as Pail, `http://localhost` works too.

3. **Check the records.** In Cloudflare’s DNS, `pail` and `*.pail` should each be a `CNAME` to `<tunnel id>.cfargotunnel.com`, with the proxy on (the orange cloud). If the wildcard is missing, add it yourself with that same target.

4. **Turn on Always Use HTTPS.** It is under **SSL/TLS**, then **Edge Certificates**, for the domain. Pail no longer sends plain `http://` visitors to HTTPS, so Cloudflare has to.

Then turn off Wi-Fi on a phone and open `https://pail.example.com` over mobile data.

Dashboards change. If a label here doesn’t match what you see, Cloudflare’s own guide is the one to trust: [Cloudflare: create a tunnel](https://developers.cloudflare.com/cloudflare-one/networks/connectors/cloudflare-tunnel/get-started/create-remote-tunnel/).

## Custom hostnames

With `PAIL_TLS=off`, any pail can take extra hostnames. There is no certificate for Pail to get, so a hostname is added at once, and it can be in any domain: Cloudflare has to answer for it, not Pail.

1. Add it to the pail, in the web UI or from a terminal:

   ```bash
   pail hosts add blog blog.example.com
   ```

2. Add a **Published application** route for it on the tunnel, with the same service URL. That makes its DNS record too.

Cloudflare’s free certificate covers `blog.example.com`. A name two levels down, such as `blog.team.example.com`, needs the paid one.

## What changes

- **Uploads stop at 100MB.** Cloudflare’s Free and Pro plans refuse a larger request, so `pail up` with a bigger archive fails through the tunnel. Raising `PAIL_MAX_UPLOAD_SIZE` doesn’t change Cloudflare’s limit. Deploys from a git host aren’t affected, because Pail fetches those itself.
- **Git hosts can reach Pail.** GitHub, GitLab and the rest can now deliver their webhooks, so a push redeploys.
- **Home devices go through Cloudflare too.** Pail has no certificate of its own any more, so there is no HTTPS straight to it. At home you can still reach it over plain HTTP at its address, which is one way past the upload limit: `pail login http://10.0.0.50`. The token then crosses your network unencrypted.

## Keep Pail’s own HTTPS as well

If devices at home should connect straight to Pail over HTTPS, leave Let’s Encrypt on and point the tunnel at Pail’s HTTPS instead.

1. Set Pail up with the [Cloudflare guide](/custom-domains/cloudflare/), and leave `PAIL_TLS` unset.
2. Follow the steps above, with `https://10.0.0.50` as each route’s service URL.
3. For each route, turn on **Match SNI to host**. It is under **Additional application settings**, then **TLS**. Without it, `cloudflared` asks Pail for a certificate for `10.0.0.50`, gets the one for `pail.example.com`, and refuses it.
4. On your home resolver, add `pail.example.com` and `*.pail.example.com`, pointing at Pail’s private address. Devices at home then skip Cloudflare.

Custom hostnames work as they do with Let’s Encrypt: each one has to be in a DNS zone Pail’s token can edit.

## When it doesn’t work

**The browser says the connection isn’t private, or shows a TLS error.** Cloudflare has no certificate for the name. This is the two-level limit: see [Check the base domain](#check-the-base-domain).

**Cloudflare shows a 502 Bad Gateway page.** `cloudflared` couldn’t get an answer from Pail. Check the service URL is Pail’s address, with `http://` when `PAIL_TLS` is `off`. A route to `https://` fails then, because nothing is listening there.

**Cloudflare shows error 1033.** The tunnel isn’t connected. Check `cloudflared` is running on the machine at home.

**One pail’s name doesn’t resolve.** The wildcard record is missing. Add the `*.pail` `CNAME` described above.

**The page says “Nothing is hosted at” a custom hostname.** The tunnel passed it on, but no pail has it. Add it with `pail hosts add`.

**A hostname is refused, and the message mentions `PAIL_TLS=off`.** Pail is still on its own certificates. Set `PAIL_TLS=off` and restart.
