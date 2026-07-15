# skills/ — the agent-logic umbrella

A run is an ordinary agent session equipped with general-purpose skills plus a
thin, service-owned launch instruction. The skills live here, in two halves
split by usage breadth:

- **`foundry/` — committed Foundry consumers.** General skills are authored in
  Skill-Canon and generated as castings there. Minos owns the decision to
  admit a landed casting as a pinned repository dependency. Nothing
  service-authored goes inside this directory.

- **`service/` — home-authored.** Skills carrying knowledge specific to this
  service (the fix run's skill, say): service-side adaptation in skill form,
  authored and maintained here, never Foundry canon.

A skill with life beyond this service lives in Foundry and is mirrored here as
a complete casting — never authored in both places.

## Updating `review-panel`

Foundry Canon owns the skill's authored source, and Foundry Castings are its
generated output. The committed Minos directory owns the selected version used
by Minos. Foundry installation does not target a Minos checkout.

After the required Foundry change has landed, use clean Foundry and Minos
worktrees and mirror the complete Claude Code casting with deletion semantics:

```sh
FOUNDRY=/path/to/Skill-Canon
MINOS=/path/to/fresh/Minos-worktree

(
  cd "$FOUNDRY"
  just check
)
rsync --archive --delete \
  "$FOUNDRY/Castings/Claude Code/skills/review-panel/" \
  "$MINOS/skills/foundry/review-panel/"
diff --recursive --brief \
  "$FOUNDRY/Castings/Claude Code/skills/review-panel/" \
  "$MINOS/skills/foundry/review-panel/"
```

Review and commit the entire resulting Minos directory. The recursive diff must
produce no output; this checks file contents and rejects missing or extra files.
Deployment then copies the committed `skills/foundry/` tree to
`/opt/minos/skills/foundry/` as described in `docs/go-live.md`.
