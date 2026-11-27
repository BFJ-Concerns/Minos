# Publication templates

The text Minos posts on a pull request is laid out by the Markdown files
here. Each is a default nobody has to touch.

A template is plain Markdown with slots, written `{{name}}`. Each slot
names one part of the published text. The module that composes the text
defines the parts, renders each from what the run holds and puts it where
its slot stands. Anything else in the file is posted as written. A part
with nothing to say leaves no gap, and a slot that names no part is
refused.

| File | Composed by | Slots |
|---|---|---|
| `summary-comment.md` | `workflows/summary-comment.mjs` | `state`, `sections`, `outstanding`, `walkthrough` |

The summary comment's first line, the record that binds its state to a
head, is not part of the template: `minos forge summary` writes it above
whatever the template produces.
