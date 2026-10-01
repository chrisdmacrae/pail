---
title: Set up a git provider
lead: "Connect GitHub, GitLab, Bitbucket, Gitea or Forgejo, so a pail can come straight from a repo and redeploy on every push."
navLabel: Set up a provider
section: Git providers
order: 5
providerTiles: true
next:
  href: /custom-domains/
  label: Set up a custom domain
---

## How it works

Connect a git host once, and New pail lists your repos on it. Pick one and it becomes a pail.

- **One connection per host.** Pail has no users, so there is one GitHub connection, one GitLab connection, and so on, shared by everything Pail does with that host.
- **Pail deploys the branch as it stands.** It fetches the repo’s default branch and serves the files in it.
- **Every push is a deploy.** Pail adds a webhook to the repo. When the branch is pushed to, the host tells Pail and Pail fetches it again.
- **A bad push doesn’t take the site down.** Like any deploy, one that fails leaves the previous one serving.

## Two ways to connect

| | Access token | Sign in |
| --- | --- | --- |
| What you do | Make a token at the host and paste it into Pail. | Press **Sign in with …** and approve Pail at the host. |
| Set-up on the server | None. | An OAuth app, registered with the host once, and two variables. |
| When it runs out | You make a new token and paste it again. | Pail renews it by itself. |

A token is the quickest way to start. Signing in is worth setting up if you’d rather not handle tokens, or your host’s tokens expire often.

## Connect with a token

1. **Make the token.** Follow the guide for your host above. Each one says which permissions the token needs.

2. **Open New pail and pick the host.** For GitLab, Gitea and Forgejo, Pail also asks for the server’s address. GitLab’s is filled in as `https://gitlab.com`; change it if you run your own.

3. **Paste the token and press Connect.** Pail checks it with the host before keeping it. A token the host rejects is not saved.

4. **Pick a repo.** Under each repo, Pail says what it found inside. Pick one it can deploy, check the name, and press **Put it in the pail**.

## Set up signing in

Signing in needs an OAuth app: a registration with the git host that says Pail may ask people to approve it.

1. **Register the app with the host.** Each host’s guide shows where. The one thing to get exactly right is the callback address:

   ```
   https://pail.example.com/oauth/callback/github
   ```

   Use the address you open Pail at in your browser, and end it with the host’s name: `github`, `gitlab`, `bitbucket`, `gitea` or `forgejo`.

2. **Give Pail the app’s ID and secret.** Set these on the server, with the host’s name in capitals:

   ```
   PAIL_OAUTH_GITHUB_CLIENT_ID=your-client-id
   PAIL_OAUTH_GITHUB_CLIENT_SECRET=your-client-secret
   ```

   | Variable | What it sets |
   | --- | --- |
   | `PAIL_OAUTH_<HOST>_CLIENT_ID` | The app’s ID, as the host shows it. |
   | `PAIL_OAUTH_<HOST>_CLIENT_SECRET` | The app’s secret. The two go together: Pail won’t start with only one set. |
   | `PAIL_OAUTH_<HOST>_SERVER` | Where the app is registered, for a host you run yourself. Required for Gitea and Forgejo. For GitLab, leave it out to use gitlab.com. |

3. **Restart Pail.** New pail now shows **Sign in with …** for that host, above the token form.

4. **Sign in.** Pail sends your browser to the host, you approve, and the host sends you back to New pail with your repos listed.

> **Pail doesn’t have to be on the internet for this.** Signing in happens in your browser: it is your browser the host sends back to Pail, so a Pail that only your network can reach works. Webhooks are different; see below.

## Pushes and webhooks

For a push to deploy, the git host has to be able to reach Pail.

- **A host on your own network can.** Gitea, Forgejo or a GitLab you run at home will reach a Pail on the same network. Check their guides: each blocks webhooks to private addresses until you allow it.
- **A hosted service usually can’t.** GitHub, gitlab.com and Bitbucket can’t reach a Pail that only your network can. The webhook is added, but its deliveries never arrive.

Either way, you can always pull the branch by hand. Press **Redeploy** on the pail’s page, or:

```bash
pail redeploy blog
```

If Pail couldn’t add the webhook at all, usually because the token may not, the pail is still made and its page says so.

> **On Pail’s own certificates.** A git host won’t trust a certificate Pail signed itself. For GitHub and GitLab, Pail adds the webhook with certificate checking turned off. Gitea and Forgejo decide this in their own settings; their guides say where.

## What Pail can deploy

Pail serves a repo’s files as they are. It doesn’t run builds yet.

| What’s in the repo | What Pail does |
| --- | --- |
| An `index.html` at the top | Serves the repo. |
| A `pail.json` with `static` set | Serves that folder. |
| A `package.json` with a build script | Declines it for now, and says what kind of project it looks like. |
| A `pail.json` with functions or containers | Declines it for now. |
| None of these | Declines it: there is nothing to serve. |

For a project that needs building, build it yourself and deploy the result with `pail up ./dist`, from your machine or from CI.

## When it doesn’t work

**“… didn’t accept that token.”** The token is mistyped, has expired, or can’t read your repos. Make a new one with the permissions in the host’s guide.

**The repo list is empty.** The token can’t see any repos. Some hosts limit a token to chosen repos; check what it was given.

**A repo says it “needs a build”.** Pail can’t run builds yet. See What Pail can deploy, above.

**“That sign-in didn’t start here, or took too long.”** A sign-in has ten minutes to finish, and each one works once. Press **Sign in with …** again.

**The host shows an error about the redirect or callback address.** The callback address registered with the host doesn’t match the one Pail sent. It has to be the address you open Pail at, followed by `/oauth/callback/` and the host’s name, exactly.

**“The sign-in to … has run out. Sign in again from New pail.”** Pail renews a sign-in by itself, but the host can end one, for instance if you revoke the app. Sign in again.

**Pushes don’t deploy.** The host can’t reach Pail, or blocked the delivery. Look at the webhook’s recent deliveries in the repo’s settings at the host: it shows what happened to each one.

**“… isn’t connected.”** The host was disconnected after the pail was made. Connect it again from New pail; the pail picks up where it left off.
