---
title: Keep settings and secrets out of the repo
lead: "Give a pail variables of its own, on the server. pail.json uses them by name, so a password never has to sit in your repo."
section: Deploying
order: 6
navLabel: Variables and secrets
next:
  href: /deploying/astro/
  label: Deploy an Astro site
---

## What they’re for

A `pail.json` lives in your repo, and so does everything written in it. A database password doesn’t belong there. Nor does anything that differs from one Pail to the next, like the address of your NAS.

Each pail has variables of its own for those, kept on the server. A variable is a setting anyone with the token can read back, or a **secret**, which is stored sealed and never shown again.

## 1. Set them

From a terminal:

```bash
pail env set notes GREETING=hello
pail env set notes DB_PASSWORD --secret
```

The second asks for the value without showing it, which keeps it out of your shell’s history. In a script, pipe it in: `echo "$DB_PASSWORD" | pail env set notes DB_PASSWORD --secret`.

Or open the pail’s page and use **Variables**.

A pail’s variables can be set before the pail exists, so its first deploy can use them.

## 2. Use them in pail.json

Write `${NAME}` anywhere in a container’s or a function’s `env`:

```json
{
  "containers": {
    "web": {
      "port": 3000,
      "memory": "256MB",
      "env": {
        "DATABASE_URL": "postgres://app:${DB_PASSWORD}@db:5432/app",
        "GREETING": "${GREETING:-hi}"
      }
    }
  }
}
```

| You write | It becomes |
| --- | --- |
| `${NAME}` | The variable’s value. The deploy fails, saying which one, if the pail has no such variable. |
| `${NAME:-fallback}` | The variable’s value, or `fallback` if the pail has none by that name. |
| `$${NAME}` | The text `${NAME}`, left alone. |

Only `env` is filled in. An image’s name, a command and a route are as written.

## 3. Deploy

A variable is read when a container or a function starts. What’s already running keeps what it started with, so after changing one:

```bash
pail redeploy notes
```

A rollback also uses the variables as they are now, not as they were when that deploy was made.

## See and remove them

```bash
pail env notes
pail env rm notes GREETING
```

A secret is listed by name, as `(secret)`. To change one, set it again. Removing a pail removes its variables.

## How a secret is kept

- **Sealed in storage.** A secret is encrypted before Pail stores it, with a key that is never put in the object store. Someone who holds the store alone can’t read it.
- **Never handed back.** Neither the API, the web UI nor `pail-cli` returns a secret’s value.
- **Not kept with a deploy.** A deploy keeps `${DB_PASSWORD}` as written. The value is filled in each time a container starts.
- **The app can still print it.** A secret reaches your code as an environment variable. If the code logs it, it is in the pail’s output.

The key is Pail’s own, made the first time it starts and kept in `secrets.key` in its data folder. **Back that file up:** without it, secrets can’t be read and have to be set again. To choose the key instead, set `PAIL_SECRETS_KEY` on the server to a long random text, such as the output of `openssl rand -base64 32`.

## When a deploy fails

| The log says | What to do |
| --- | --- |
| pail.json uses `${NAME}`, and the pail has no variable called NAME | Set it, or give it a fallback: `${NAME:-value}`. |
| A secret can’t be read: it was kept with another key | Pail’s key changed. Put the old `secrets.key` or `PAIL_SECRETS_KEY` back, or set the secret again. |
| This Pail has no key to seal secrets with | Its data folder can’t be written. Fix that, or set `PAIL_SECRETS_KEY`, and restart Pail. |
