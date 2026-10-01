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

Connect to a git host on New pail, and it lists your repos there. Pick one and it becomes a pail.

- **One connection per pail.** You connect each time you make a pail, and that connection is the pail’s alone. One pail can use a token that reaches a single repo, and the next a different token, a different account, or a sign-in.
- **Pail deploys the branch as it stands.** It fetches the repo’s default branch and serves the files in it.
- **Every push is a deploy.** Pail adds a webhook to the repo. When the branch is pushed to, the host tells Pail and Pail fetches it again.
- **A bad push doesn’t take the site down.** Like any deploy, one that fails leaves the previous one serving.

## Two ways to connect

| | Access token | Sign in |
| --- | --- | --- |
| What you do | Make a token at the host and paste it into Pail. | Press **Sign in with …** and approve Pail at the host. |
| Set-up on the server | None. | An OAuth app, registered with the host once, and two variables. |
| When it runs out | The pail can’t pull until you reconnect it with a new token. | Pail renews it by itself. |

A token is the quickest way to start. Signing in is worth setting up if you’d rather not handle tokens, or your host’s tokens expire often.

## Connect with a token

1. **Make the token.** Follow the guide for your host above. Each one says which permissions the token needs.

2. **Open New pail and pick the host.** For GitLab, Gitea and Forgejo, Pail also asks for the server’s address. GitLab’s is filled in as `https://gitlab.com`; change it if you run your own.

3. **Paste the token and press Connect.** Pail checks it with the host. A token the host rejects is not kept. One it accepts is held for up to an hour, until you make the pail, and from then on is kept with that pail and used for nothing else.

4. **Pick a repo.** Under each repo, Pail says what it found at the top of it. Pick the repo, check the name, and press **Put it in the pail**. If Pail found nothing to serve at the top, the site is probably in a folder: type it in **Folder**, like `docs`, and Pail looks there instead. See More than one pail in a repo, below.

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

4. **Sign in.** Pail sends your browser to the host, you approve, and the host sends you back to New pail with your repos listed. Like a token, a sign-in is for the pail you make with it: the next pail signs in again.

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

| What’s in the repo | What Pail does |
| --- | --- |
| An `index.html` at the top | Serves the repo as it is. |
| A `pail.json` with `static` set | Serves that folder. |
| A `package.json` with a build script | Builds it on every deploy, then serves the result. |
| A `pail.json` with functions or containers | Declines it for now. |
| None of these | Declines it: there is nothing to serve. |

Builds run in a small virtual machine of their own, which needs a Pail server on Linux with KVM. A Pail without it says so under the repo, and declines it. In that case, build the project yourself and deploy the result with `pail up ./dist`, from your machine or from CI.

## More than one pail in a repo

A repo can hold several pails, each in a folder of its own:

```
acme/
  apps/web/     index.html, …
  apps/docs/    pail.json, public/
```

Make one pail per folder. Pick the repo in New pail, type the folder into **Folder**, like `apps/web`, and Pail looks inside it and suggests a name made of the repo’s and the folder’s: `acme-web`. Then do the same for `apps/docs`.

- **The folder is the pail’s top.** Its `index.html`, `pail.json` and `package.json` are the ones Pail reads, and paths in `pail.json` start from it. Nothing outside the folder is deployed.
- **Each pail is its own.** It has its own name, addresses, deploys and rollback.
- **A build has the whole repo.** A project that needs building is built with the rest of the repo beside it. Dependencies are installed where the lockfile is, in the folder or above it, so a project in an npm, pnpm or yarn workspace builds as it does on your machine. Pail runs the folder’s own build script and no other.

From the command line, `pail up apps/web` deploys one folder. Without `--name` or a `name` in its `pail.json`, the pail is named the same way, `acme-web`. When the project builds from a lockfile above it, `pail up` sends the workspace along and says so.

### Which pushes deploy which pail

A push redeploys a pail when it changed something the pail is made from:

- a file in the pail’s folder,
- a `package.json`, lockfile, `pnpm-workspace.yaml` or `.npmrc` in a folder above it,
- anything its `pail.json` lists under `watch`.

`watch` is for what the pail uses from elsewhere in the repo. Its paths start from the top of the repo, and each is a folder or a file:

```json
{
  "watch": ["packages/ui", "shared/theme.css"]
}
```

A push that changed none of these is skipped: no deploy is made, and the pail goes on serving what it was.

Pail only skips a push when the host lists every file it changed. It deploys to be safe when the host doesn’t:

| When | Why |
| --- | --- |
| Any push on Bitbucket | Bitbucket doesn’t list files. |
| A forced push, or a new branch | What changed isn’t the commits that were pushed. |
| A push of twenty commits or more | Hosts list only the first of a long push. |

Only GitHub says when a push was forced. On the others, press **Redeploy** after rewriting a branch.

## Give a pail you already have a repo

A pail made with `pail up` or an upload can deploy from a repo instead, without being made again.

1. **Open the pail’s page and press Deploy from a git repo.** It is in the column on the right, under the pail’s source.

2. **Pick the host and connect,** by token or by signing in, as on New pail. Signing in brings you back to the same place.

3. **Pick the repo,** and type the folder if the site isn’t at the top of it.

4. **Press Deploy … from this repo.** Pail deploys the branch as it stands and adds the webhook, so pushes deploy from then on.

The pail keeps its name, its addresses, its variables and its deploys, so you can still serve an older one. If the first deploy from the repo fails, the pail goes on serving what it was.

A pail that already deploys from a repo has **Change repo** in the same place. It works the same way, and takes Pail’s webhook off the repo the pail is leaving.

## Go back to deploying by hand

A pail that deploys from a repo can stop, and be deployed with `pail up` or an upload again.

1. **Open the pail’s page and press Disconnect.** It is beside **Reconnect** and **Change repo**.

2. **Confirm.** Pail takes its webhook off the repo and forgets the pail’s connection to the host.

The pail keeps serving what it was, and keeps its name, addresses, variables and deploys. Pushes no longer deploy it. To deploy it:

```bash
pail up ./dist --name blog
```

or press **Upload a deploy** on its page. **Redeploy** now deploys the pail’s latest files again, since there is no branch to pull.

If Pail can’t reach the host to take the webhook off, the pail is disconnected all the same. The webhook stays at the host until you delete it there, and its deliveries are turned away.

To deploy from a repo again, press **Deploy from a git repo**.

## Reconnect a pail

A pail keeps the connection it was made with. When that token expires or is revoked, or the host ends its sign-in, the pail can’t pull: pushes stop deploying, and **Redeploy** says the host didn’t accept the token. The site keeps serving its last deploy.

To give the pail a new connection:

1. **Open the pail’s page and press Reconnect to …** It is under the repo and branch. If Redeploy has just been refused, the form is already open.

2. **Connect as you would on New pail.** Paste a new token and press **Connect**, or press **Sign in with …**. Signing in brings you back to the pail’s page.

3. **Press Redeploy** to pull the branch with the new connection.

The new connection takes the place of the old one, and has to be able to see the pail’s repo. Nothing else changes: the pail keeps its name, its deploys, its hostnames and its webhook, so pushes deploy again without anything being added at the host.

## When it doesn’t work

**“… didn’t accept that token.”** The token is mistyped, has expired, or can’t read your repos. Make a new one with the permissions in the host’s guide.

**The repo list is empty.** The token can’t see any repos. Some hosts limit a token to chosen repos; check what it was given.

**A repo says it “needs a build, which this Pail can’t run”.** This Pail’s server can’t run the virtual machines builds happen in. See What Pail can deploy, above.

**“That sign-in didn’t start here, or took too long.”** A sign-in has ten minutes to finish, and each one works once. Press **Sign in with …** again.

**The host shows an error about the redirect or callback address.** The callback address registered with the host doesn’t match the one Pail sent. It has to be the address you open Pail at, followed by `/oauth/callback/` and the host’s name, exactly.

**A pail that used to deploy says “… didn’t accept that token.”** The pail’s token has expired or been revoked, or the host ended its sign-in, for instance because you revoked the app. Reconnect the pail; see Reconnect a pail, above.

**“That connection can’t see …”** When reconnecting, the new token or sign-in doesn’t reach the pail’s repo. Use a token that was given that repo, or sign in as someone who can see it.

**Pushes don’t deploy.** The host can’t reach Pail, or blocked the delivery. Look at the webhook’s recent deliveries in the repo’s settings at the host: it shows what happened to each one. For a pail that is a folder of its repo, a delivery answered with “The push changed nothing in …” was skipped on purpose; see Which pushes deploy which pail, above.

**“… isn’t connected.”** On New pail, the connection waited more than an hour for its pail, or Pail restarted in between. Connect again.
