# PailRow

One pail in the list: its name (display face), its URL (clickable, mono, `link`), its Status, where it came from and when, and two ghost actions — Redeploy and Remove. The home screen is a stack of these and nothing else.

- Provide: `name`, `url`, `status`, `source`, optional `revision` (branch or short hash) and `updated` ("2 min ago"), `onOpen` (makes the name a button that opens the pail's page), `onRedeploy`, `onRemove`.
- Sort by most recently updated. No pagination, filters or owners — there are no owners.
