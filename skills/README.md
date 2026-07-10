# skills/ — the agent-logic umbrella

A run is an ordinary agent session equipped with general-purpose skills plus a
thin, service-owned launch instruction. The skills live here, in two halves
split by usage breadth:

- **`foundry/` — wholly sync-owned.** General skills authored in the
  Skill-Canon (the grown `agent-review`, the general debugging skill, …)
  arrive here by the Foundry's sync and are consumed as plain files. Nothing
  service-authored goes inside: every file in `foundry/` is the sync's to
  place, update, and remove. (The `.gitkeep` merely holds the empty
  destination until the first sync lands.)

- **`service/` — home-authored.** Skills carrying knowledge specific to this
  service (the fix run's skill, say): service-side adaptation in skill form,
  authored and maintained here, never Foundry canon.

A skill with life beyond this service lives in the Foundry and arrives by
sync — never duplicated here, since the sync is what keeps it matched.
