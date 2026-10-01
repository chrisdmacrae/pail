---
title: GitLab
lead: "Deploy pails from GitLab: connect with a personal access token, or set up signing in."
section: Git providers
order: 7
provider: gitlab
next:
  href: /git-providers/#pushes-and-webhooks
  label: Pushes and webhooks
---

## Connect with a token

1. In GitLab, open your avatar menu, then **Edit profile**, then **Access tokens**.
2. Add a new token. Give it a name and an expiry date.
3. Tick the **api** scope.
4. Create the token and copy it. GitLab shows it once.

> **It has to be the api scope.** The read-only scopes can list and fetch a repo, but they can’t add the webhook that makes pushes deploy.

> **You need to be a Maintainer.** GitLab only lets Maintainers and Owners of a project add webhooks to it.

> **GitLab’s tokens always expire.** A year at most. A pail keeps the token it was made with, and can’t pull once that token has expired. When it does, make another and press **Reconnect** on the pail’s page; see [Reconnect a pail](/git-providers/#reconnect-a-pail). Or set up signing in, which Pail renews by itself.

Then, in Pail, open **New pail**, pick **GitLab**, check the **Server** address, paste the token and press **Connect**.

The server is filled in as `https://gitlab.com`. If you run your own GitLab, put its address there instead.

Dashboards change. If a label here doesn’t match what you see, GitLab’s own guide is the one to trust: [GitLab: personal access tokens](https://docs.gitlab.com/user/profile/personal_access_tokens/).

## Set up signing in

This is optional. It puts a **Sign in with GitLab** button on New pail, so nobody pastes a token.

1. In GitLab, open your avatar menu, then **Edit profile**, then **Applications**, and choose **Add new application**.
2. Give it a name, such as Pail.
3. Set **Redirect URI** to the address you open Pail at, followed by `/oauth/callback/gitlab`.
4. Leave **Confidential** ticked, and tick the **api** scope.
5. Save. Copy the **Application ID** and the **Secret**.

With Pail at `https://pail.example.com`, the callback address is:

```
https://pail.example.com/oauth/callback/gitlab
```

Then set these on the server and restart Pail:

```
PAIL_OAUTH_GITLAB_CLIENT_ID=your-client-id
PAIL_OAUTH_GITLAB_CLIENT_SECRET=your-client-secret
PAIL_OAUTH_GITLAB_SERVER=https://gitlab.home.example
```

`PAIL_OAUTH_GITLAB_SERVER` is only for a GitLab you run yourself. Leave it out for gitlab.com.

When you sign in, Pail asks GitLab for the `api` scope.

More from GitLab: [GitLab: configure GitLab as an OAuth 2.0 provider](https://docs.gitlab.com/integration/oauth_provider/).

## Pushes

When you make a pail from a repo, Pail adds a webhook to it so each push to the branch deploys.

- gitlab.com has to reach Pail to deliver a push. If your Pail is only on your network, deliveries won’t arrive; use Redeploy.
- A GitLab you run yourself blocks webhooks to addresses on the local network until an administrator allows them: in the Admin area, under **Settings**, **Network**, **Outbound requests**, allow requests to the local network from webhooks and integrations.

If something goes wrong, [When it doesn’t work](/git-providers/#when-it-doesnt-work) lists the usual causes.
