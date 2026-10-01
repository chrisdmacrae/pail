---
title: GitHub
lead: "Deploy pails from GitHub: connect with a personal access token, or set up signing in."
section: Git providers
order: 6
provider: github
next:
  href: /git-providers/#pushes-and-webhooks
  label: Pushes and webhooks
---

## Connect with a token

1. In GitHub, open **Settings**, then **Developer settings**, then **Personal access tokens**.
2. Choose **Fine-grained tokens** and generate a new one.
3. Under **Repository access**, pick the repos Pail may deploy, or all of them.
4. Under **Repository permissions**, set **Contents** to **Read-only** and **Webhooks** to **Read and write**.
5. Generate the token and copy it. GitHub shows it once.

> **A classic token works too.** Give it the `repo` scope. It reaches every repo you can, so a fine-grained token is the tighter choice.

> **Fine-grained tokens expire.** A pail keeps the token it was made with, and can’t pull once that token has expired. When it does, make another and press **Reconnect** on the pail’s page; see [Reconnect a pail](/git-providers/#reconnect-a-pail). Or set up signing in, which Pail renews by itself.

Then, in Pail, open **New pail**, pick **GitHub**, paste the token and press **Connect**.

Dashboards change. If a label here doesn’t match what you see, GitHub’s own guide is the one to trust: [GitHub: managing your personal access tokens](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/managing-your-personal-access-tokens).

## Set up signing in

This is optional. It puts a **Sign in with GitHub** button on New pail, so nobody pastes a token.

1. In GitHub, open **Settings**, then **Developer settings**, then **OAuth Apps**, and choose **New OAuth App**.
2. Give it a name, such as Pail, and set **Homepage URL** to the address you open Pail at.
3. Set **Authorization callback URL** to that address followed by `/oauth/callback/github`.
4. Register the app, then choose **Generate a new client secret**. Copy the **Client ID** and the secret.

> **An OAuth App, not a GitHub App.** GitHub has two kinds of app. Pail signs in through an OAuth App; a GitHub App’s credentials won’t work here.

With Pail at `https://pail.example.com`, the callback address is:

```
https://pail.example.com/oauth/callback/github
```

Then set these on the server and restart Pail:

```
PAIL_OAUTH_GITHUB_CLIENT_ID=your-client-id
PAIL_OAUTH_GITHUB_CLIENT_SECRET=your-client-secret
```

When you sign in, Pail asks GitHub for the `repo` scope: reading your repos and adding webhooks to them.

> **GitHub keeps ten sign-ins per person for an app.** Each pail signs in for itself, and an eleventh sign-in ends the oldest, whose pail then can’t pull until it is reconnected. With more than ten pails from GitHub, connect some of them with tokens.

More from GitHub: [GitHub: creating an OAuth app](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app).

## Pushes

When you make a pail from a repo, Pail adds a webhook to it so each push to the branch deploys.

- github.com has to reach Pail to deliver a push. If your Pail is only on your network, deliveries won’t arrive; use Redeploy.

> **GitHub Enterprise Server isn’t supported.** Pail connects to github.com only.

If something goes wrong, [When it doesn’t work](/git-providers/#when-it-doesnt-work) lists the usual causes.
