# Field

One text input with a label above, an optional hint or error below, and optional `prefix`/`suffix` blocks for fixed parts of a value (the pail domain). Pail asks for almost nothing: a name, maybe a build command. Don't add fields for things Pail can guess.

- Provide: `label` (sentence case, no colon), `value`/`onChange` or `defaultValue`, `hint` for the one thing people get wrong, `error` (replaces the hint, says how to fix it), `mono` for paths and commands.
