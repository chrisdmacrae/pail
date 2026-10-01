---
title: Gitea
lead: "Deploy pails from Gitea: connect with an access token, or set up signing in."
section: Git providers
order: 9
provider: gitea
next:
  href: /git-providers/#pushes-and-webhooks
  label: Pushes and webhooks
---

## Connect with a token

1. In Gitea, open **Settings**, then **Applications**.
2. Under **Generate New Token**, give the token a name.
3. Under its permissions, set **repository** to **Read and Write** and **user** to **Read**.
4. Generate the token and copy it. Gitea shows it once.

> **Write, for the webhook.** Reading is enough to fetch a repo, but adding the webhook that makes pushes deploy needs write access to it.

Then, in Pail, open **New pail**, pick **Gitea**, enter your server’s address under **Server**, paste the token and press **Connect**.

Dashboards change. If a label here doesn’t match what you see, Gitea’s own guide is the one to trust: [Gitea: API usage](https://docs.gitea.com/development/api-usage).

## Set up signing in

This is optional. It puts a **Sign in with Gitea** button on New pail, so nobody pastes a token.

1. In Gitea, open **Settings**, then **Applications**, and find **Manage OAuth2 Applications**.
2. Give the application a name, such as Pail.
3. Set **Redirect URI** to the address you open Pail at, followed by `/oauth/callback/gitea`.
4. Create it. Copy the **Client ID** and the **Client Secret**. Gitea shows the secret once.

With Pail at `https://pail.example.com`, the callback address is:

```
https://pail.example.com/oauth/callback/gitea
```

Then set these on the server and restart Pail:

```
PAIL_OAUTH_GITEA_CLIENT_ID=your-client-id
PAIL_OAUTH_GITEA_CLIENT_SECRET=your-client-secret
PAIL_OAUTH_GITEA_SERVER=https://git.home.example
```

`PAIL_OAUTH_GITEA_SERVER` is your Gitea’s address. It is required: the app is registered there.

More from Gitea: [Gitea: OAuth2 provider](https://docs.gitea.com/development/oauth2-provider).

## Pushes

When you make a pail from a repo, Pail adds a webhook to it so each push to the branch deploys.

- Gitea refuses to deliver webhooks to private addresses until you allow it. In `app.ini`, under `[webhook]`, set `ALLOWED_HOST_LIST` to include Pail: `private` allows every address on your network, or name Pail’s host. Restart Gitea after.
- If Pail signs its own certificates, Gitea won’t trust them. Either add Pail’s root certificate to the system Gitea runs on, or set `SKIP_TLS_VERIFY = true` under `[webhook]`.

> **Pail lists the repos you own.** Repos that belong to an organisation you’re in aren’t listed yet.

If something goes wrong, [When it doesn’t work](/git-providers/#when-it-doesnt-work) lists the usual causes.
