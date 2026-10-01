# Button

Buttons do one thing and say it with a verb. `primary` (pail fill, `on-pail` label) appears **at most once per screen**, for the thing the screen is for: "New pail", "Drop it in the pail", "Deploy". `quiet` is the default for everything else; `ghost` is for icon-only row actions (always pass `aria-label`); `danger` (`failed` fill) only inside the confirm step of removing a pail.

- Provide: `children` (sentence case, 1–3 words), optional `icon` from Icon, `size="sm"` inside rows and cards.
- Don't: two primaries side by side, "Submit"/"OK", icons without a label outside rows.
