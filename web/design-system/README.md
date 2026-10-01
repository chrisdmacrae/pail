Pail is where you toss the things you host at home. Each **pail** is one web-accessible property — a static site, a PWA, a small server app — with one name, one URL and one status. You make one in a single step: run a command, pick a repo, or drop a folder. There are no users, no roles, no teams, no settings pages. Every design decision answers to one rule: **dead simple**.

## Principles

1. **One thing per screen.** The home screen is a list of pails and a "New pail" button. A new pail is: pick a source → do the one step that source needs → it's live.
2. **Guess, don't ask.** Pail infers the name from the folder or repo, the output folder from what's in it. A Field appears only for something Pail can't guess.
3. **Nothing to manage.** Never design login screens, account menus, avatars, invite flows, permission toggles or "owner" columns. If a design needs one, the design is wrong.
4. **The CLI and the UI are the same product.** Every action in the UI has a one-line `pail` command, and the UI shows it.
5. **Quiet until something's wrong.** Live pails are calm; colour is spent on Building and Failed.

## Voice

Plain, warm, short. Talk like a neighbour handing you a bucket, not a cloud console.

- Sentence case everywhere: "New porch", "Pick a source".
- Second person, present tense: "Drop a folder or a .zip." "Your porch is live."
- "pail" is a common noun in copy — lowercase, plural "pails". **Pail**, capitalised, is the product. The CLI is always `pail-cli` (the package) or `pail` (the command), in mono.
- One pail metaphor per screen at most ("Drop it in the pail"). Never stack puns — no "bucket list", no "kick the bucket".
- Errors say what happened and what to do, in that order: "No index.html in ./site. Point Pail at the folder that has it."
- No exclamation marks, no emoji, no "Oops", no "Successfully".

| Say | Don't say |
| --- | --- |
| Your pail is live. | Deployment completed successfully! |
| Building… | Provisioning resources |
| The last deploy failed. blog.pail.lan is still serving the previous one. | Error 500: build pipeline failure |
| Remove pail | Delete resource |

## Colour

Day (`light`) is warm paper; Night (`dark`) is the shed after dark. Both are first-class.

- Page on `surface`; rows, cards and inputs on `surface-raised`; things you read but don't click (commands, logs, the idle drop zone) on `surface-sunk`.
- Text is `ink`; secondary text (URLs under names, timestamps, hints) is `ink-muted`. Both hold ≥5.4:1 on every surface and soft tint in both themes.
- **`pail` is the brand** — the sunny paint on a beach pail. It's the fill in the mark, the fill of the one primary button and the Building dot. It is a fill, never text on a light ground — use `pail-ink` for yellow-brown text and `on-pail` for labels on it.
- **`water`** is decorative only (the cover, illustrations). Its working forms are `water-ink` (links via `link`, the focus ring via `focus`) and `water-soft` (the selected source tile).
- Status colours (`live`, `failed`, with their `-soft` tints) mean status and nothing else, and always come with a word. Building borrows pail. Off is `ink-muted` on `surface-sunk`.
- Hairlines are `line`; anything you can type into or drop onto has a `line-strong` edge (≥3:1).

## Type

- **Bricolage Grotesque** (`display`, `title`, `heading`) — pail names and page titles. A pail's name is always set in it; that's how you spot names on a screen.
- **Hanken Grotesk** (`body`, `body-strong`, `small`, `label`) — everything you read.
- **IBM Plex Mono** (`command`, `code`) — anything you could type or paste: commands, URLs, paths, hashes, logs. A URL is always mono.
- One `display` per screen at most. Buttons are `body-strong`.

## Space, shape, depth

- 4px base: `space-4` inside rows and cards, `space-3` between rows, `space-8` between sections and as the desktop gutter.
- Shapes are softly squared, like a pressed-steel rim: `radius-sm` for inputs and code, `radius-md` for buttons, rows and the drop zone, `radius-lg` for dialogs. `radius-full` is reserved for status pills and dots.
- Flat by default. `shadow-sm` is a single 1px bottom edge on rows; `shadow-lg` only for things that float (dialogs, toasts).
- Content column max 880px. The pail list is a single column on every screen size.

## Focus and motion

- Keyboard focus is the `focus-ring` shadow: a 2px gap in the page colour, then 2px of solid water-ink (≥5.8:1 on every surface in both themes). Never remove it.
- The only animation is the Building dot's slow glow, switched off under `prefers-reduced-motion`. Pages don't slide, cards don't bounce.

## Iconography

Twelve line icons in `assets/Icons` and the `Icon` component: 24px grid, 1.75 stroke, round caps and joins, `currentColor`. Icons sit beside words, not instead of them, except ghost row actions (which carry `aria-label`). **Never draw a git host's logo** — GitHub, GitLab, Bitbucket, Gitea and Forgejo appear as their names in plain type with the shared `git` icon.

## Logo

The mark is a pail: a handle, a rim and a tapered body, filled to the line in `pail` yellow. Use `pail-mark.svg` / `pail-wordmark.svg` on Day, the `-reversed` files on Night. The wordmark is lowercase "pail" in Bricolage Grotesque Bold. Clear space: the rim's height on every side; minimum mark size 16px. Don't recolour the fill, outline it, tilt the pail or put the mark on a pail fill.

## The screens, all of them

1. **Pails** — TopBar, a `display` title, a stack of PailRow. Empty: one sentence and a Command (`pail up ./dist`) plus the "New pail" button.
2. **New pail** — SourcePicker, then exactly one of: Command (pail-cli), DropZone (Upload), or a list of repos (git host). Name Field pre-filled.
3. **A pail** — `title` name, mono URL, Status, Redeploy, the last deploy's BuildLog, the `pail` command that does the same, and "Remove pail" at the bottom.

That's the whole app. If a design needs a fourth screen, push back first.
