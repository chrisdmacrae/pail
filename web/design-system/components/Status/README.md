# Status

A pail is always in exactly one of four states, shown as a pill with a dot **and** a word — colour never carries it alone.

| state | word | when |
| --- | --- | --- |
| `live` | Live | Serving the latest good deploy. |
| `building` | Building | A deploy is unpacking or building. The dot glows (static under reduced motion). |
| `failed` | Failed | The latest deploy failed. The previous good one keeps serving — say so next to it. |
| `off` | Off | Stopped by you. Hollow dot. |

- Provide: `state`; `children` only to override the word (keep it one word).
