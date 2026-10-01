---
title: Bitbucket
lead: "Deploy pails from Bitbucket: connect with an access token, or set up signing in."
section: Git providers
order: 8
provider: bitbucket
next:
  href: /git-providers/#pushes-and-webhooks
  label: Pushes and webhooks
---

## Connect with a token

1. In Bitbucket, open the repository, or the workspace, that Pail should deploy from, and go to its settings.
2. Under **Security**, open **Access tokens** and create one.
3. Give it **Repositories: Read** and **Webhooks: Read and write**.
4. Create the token and copy it. Bitbucket shows it once.

> **Or an API token with your email.** To use a token tied to your own account instead, paste it into Pail as your Atlassian email, a colon, then the token: `you@example.com:your-token`. Pail sends the two the way Bitbucket expects.

> **If the repo list comes up empty.** An access token belongs to a repository or a workspace, not to a person, and Bitbucket may not list anything for it. Use an API token with your email instead.

Then, in Pail, open **New pail**, pick **Bitbucket**, paste the token and press **Connect**.

Dashboards change. If a label here doesn’t match what you see, Bitbucket’s own guide is the one to trust: [Bitbucket: access tokens](https://support.atlassian.com/bitbucket-cloud/docs/access-tokens/).

## Set up signing in

This is optional. It puts a **Sign in with Bitbucket** button on New pail, so nobody pastes a token.

1. In Bitbucket, open your workspace’s settings and find **OAuth consumers**. Choose **Add consumer**.
2. Give it a name, such as Pail.
3. Set **Callback URL** to the address you open Pail at, followed by `/oauth/callback/bitbucket`.
4. Tick **This is a private consumer**.
5. Under permissions, give it **Repositories: Read** and **Webhooks: Read and write**.
6. Save. Open the consumer and copy its **Key** and **Secret**. The key is the client ID.

> **Permissions live on the consumer.** Bitbucket decides what a sign-in may do from the consumer’s settings, not from what Pail asks for. If repos don’t list or webhooks can’t be added, check the two permissions above.

With Pail at `https://pail.example.com`, the callback address is:

```
https://pail.example.com/oauth/callback/bitbucket
```

Then set these on the server and restart Pail:

```
PAIL_OAUTH_BITBUCKET_CLIENT_ID=your-client-id
PAIL_OAUTH_BITBUCKET_CLIENT_SECRET=your-client-secret
```

More from Bitbucket: [Bitbucket: use OAuth on Bitbucket Cloud](https://support.atlassian.com/bitbucket-cloud/docs/use-oauth-on-bitbucket-cloud/).

## Pushes

When you make a pail from a repo, Pail adds a webhook to it so each push to the branch deploys.

- Bitbucket has to reach Pail to deliver a push. If your Pail is only on your network, deliveries won’t arrive; use Redeploy.

> **Bitbucket Data Center isn’t supported.** Pail connects to Bitbucket Cloud only.

If something goes wrong, [When it doesn’t work](/git-providers/#when-it-doesnt-work) lists the usual causes.
