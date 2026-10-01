---
title: Forgejo
lead: "Deploy pails from Forgejo: connect with an access token, or set up signing in."
section: Git providers
order: 10
provider: forgejo
next:
  href: /git-providers/#pushes-and-webhooks
  label: Pushes and webhooks
---

## Connect with a token

1. In Forgejo, open **Settings**, then **Applications**.
2. Under **Generate new token**, give the token a name.
3. Under its permissions, set **repository** to **Read and write** and **user** to **Read**.
4. Generate the token and copy it. Forgejo shows it once.

> **Write, for the webhook.** Reading is enough to fetch a repo, but adding the webhook that makes pushes deploy needs write access to it.

> **Codeberg is Forgejo.** To deploy from Codeberg, use `https://codeberg.org` as the server.

Then, in Pail, open **New pail**, pick **Forgejo**, enter your server’s address under **Server**, paste the token and press **Connect**.

Dashboards change. If a label here doesn’t match what you see, Forgejo’s own guide is the one to trust: [Forgejo: API usage](https://forgejo.org/docs/latest/user/api-usage/).

## Set up signing in

This is optional. It puts a **Sign in with Forgejo** button on New pail, so nobody pastes a token.

1. In Forgejo, open **Settings**, then **Applications**, and find the OAuth2 applications.
2. Give the application a name, such as Pail.
3. Set **Redirect URI** to the address you open Pail at, followed by `/oauth/callback/forgejo`.
4. Create it. Copy the **Client ID** and the **Client secret**. Forgejo shows the secret once.

With Pail at `https://pail.example.com`, the callback address is:

```
https://pail.example.com/oauth/callback/forgejo
```

Then set these on the server and restart Pail:

```
PAIL_OAUTH_FORGEJO_CLIENT_ID=your-client-id
PAIL_OAUTH_FORGEJO_CLIENT_SECRET=your-client-secret
PAIL_OAUTH_FORGEJO_SERVER=https://git.home.example
```

`PAIL_OAUTH_FORGEJO_SERVER` is your Forgejo’s address. It is required: the app is registered there.

More from Forgejo: [Forgejo: OAuth2 provider](https://forgejo.org/docs/latest/user/oauth2-provider/).

## Pushes

When you make a pail from a repo, Pail adds a webhook to it so each push to the branch deploys.

- Forgejo refuses to deliver webhooks to private addresses until you allow it. In `app.ini`, under `[webhook]`, set `ALLOWED_HOST_LIST` to include Pail: `private` allows every address on your network, or name Pail’s host. Restart Forgejo after.
- If Pail signs its own certificates, Forgejo won’t trust them. Either add Pail’s root certificate to the system Forgejo runs on, or set `SKIP_TLS_VERIFY = true` under `[webhook]`.

> **Pail lists the repos you own.** Repos that belong to an organisation you’re in aren’t listed yet.

If something goes wrong, [When it doesn’t work](/git-providers/#when-it-doesnt-work) lists the usual causes.
