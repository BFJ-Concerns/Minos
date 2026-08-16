# QA Signoff

The doctrine for claiming implemented UI works — after building a feature,
before reporting it done. Its premise: a claim you have not checked in the
state where it matters is a guess, and functional, layout, and aesthetic
quality are three separate passes because each can fail while the others hold.

The doctrine scales with the claim surface, not the diff size. A new feature,
a reworked layout, or any blanket "the UI works" signoff gets the full pass
below. A small fix gets the subset that covers what the fix actually claims —
a copy change needs the affected text inspected in its state, not an inventory
of every control — but the mapping rule is invariant at every scale: each
claim in the report traces to a check that ran.

## The QA Inventory

Before testing, write a short coverage list from three sources: what the user
asked for, the user-visible behaviour actually implemented, and the claims the
final report will make. Everything in any of the three maps to at least one
check — a functional check, the specific state where the visual check happens,
and the evidence to capture. Convert subjective requirements ("feels
responsive", "clean layout") into observable checks rather than leaving them
implicit. Add at least two off-happy-path scenarios that could expose fragile
behaviour. When exploration reveals a new control, state, or claim, add it to
the inventory before signing off.

## Functional Pass

- Sign off on real user input — clicks, typing, keys, touch — driven through
  the page. Reading or staging state with `eval`/`run-code` is fine for
  diagnosis but does not count as signoff evidence, because it bypasses the
  event handling users go through.
- Verify at least one critical flow end to end, confirming the visible result,
  not internal state.
- Cover every visible control at least once; for toggles and reversible
  controls, test the full cycle — initial state, changed state, back to
  initial.
- For realtime or animation-heavy interfaces, verify behaviour under real
  interaction timing — while things move, not only in settled states.
- After the scripted checks, spend a short free-form burst (half a minute or
  two) using the interface like a person rather than following the plan;
  anything new it reveals goes into the inventory and gets covered.

## Visual Pass

Run this as its own pass over screenshots, after — and independently of — the
functional pass.

Two window modes exist, and they answer different questions. The default is a
deterministic pass at explicit sizes — `playwright-cli resize <w> <h>` at the
start makes screenshots reproducible across machines. When the product's
behaviour may depend on the real launched window — OS-level DPI, window-manager
sizing, browser chrome — add a separate headed pass (`open --headed` in a
fresh session) and inspect the as-launched layout before any resize. Treat the
two modes as separate sessions, not a resize of the same one.

- Inspect the initial viewport before scrolling: the primary claims of the
  interface should be perceptible there.
- Inspect every required region and every inventoried state, including at
  least one meaningful post-interaction state; for animated interfaces, at
  least one in-transition state as well as the settled endpoints.
- Reach the densest realistic state — populated lists, long labels, expanded
  panels — not only the empty or freshly-loaded one.
- Distinguish presence from perceptibility: an affordance that exists but is
  illegible, occluded, clipped, or lost in weak contrast is a visual failure
  even though the DOM contains it.
- Where labels, overlays, annotations, or guides are meant to track changing
  content, verify the relationship still holds after the relevant state change
  — tracking that drifts is exactly the defect a single static screenshot
  hides.
- Look for: clipping, overflow, misalignment, inconsistent spacing, illegible
  text, broken layering, awkward motion states — and judge aesthetic
  coherence, not just correctness.
- Screenshots are the primary evidence and overrule metrics: visible clipping
  is a failure no numeric check can excuse. Prefer viewport screenshots for
  signoff; full-page captures are secondary debugging artefacts.
- If the product defines a minimum supported viewport, run a pass there
  (`playwright-cli resize <w> <h>`); otherwise pick a realistic smaller size
  and inspect it explicitly. Add a mobile-sized pass when mobile is in scope —
  `resize` covers layout, but when touch handling or device metrics matter,
  drive a one-shot Playwright script with true device emulation (Playwright's
  device descriptors) instead of trusting a resized desktop window.

## Viewport Fit

Do not assume a screenshot is acceptable because the main widget is visible.
Define the intended initial view (above-the-fold for scrollable pages; the
full interactive surface plus required controls for app-like shells), then
check fit both visually and numerically:

```bash
playwright-cli eval "() => ({
  vw: window.innerWidth, vh: window.innerHeight,
  sw: document.documentElement.scrollWidth,
  sh: document.documentElement.scrollHeight })"
```

Document-level scroll metrics are not sufficient for fixed shells: internal
panes and hidden-overflow containers can clip required UI while page metrics
look clean, so check the bounding boxes of required regions
(`eval "el => el.getBoundingClientRect()" <ref>`, or `snapshot --boxes`)
against the viewport. Scrolling is acceptable where the design scrolls and the
initial view still carries the core experience; it is not a workaround for a
fixed shell whose primary surface or controls do not fit.

For desktop-windowed apps (Electron), also check the as-launched window size
and initial layout before any resize — launch size is part of the product.

## Signoff Gates

Sign off only when all of these hold, and say so against the inventory:

- The functional path passed on real user input, and coverage against the
  inventory is explicit — including any intentional exclusions.
- Every user-visible claim has a matching visual check from the state and
  viewport where the claim matters.
- Viewport-fit checks passed for the intended initial view and any required
  minimum size.
- Functional, fit, and visual quality each passed on their own evidence — one
  does not imply the others.
- Any disagreement between screenshots and numeric checks was investigated,
  with screenshots taking precedence.
- The report names the defect classes checked and not found (clipping,
  overflow, console errors, broken states) — a negative confirmation, not
  silence.
- Sessions are closed and artefacts cleaned up or deliberately kept
  (see Artefact Hygiene in the skill body).

Two questions before declaring done: which visible part of the interface has
not been inspected closely, and which visible defect would be most
embarrassing if the user looked first? Answer both by looking, not by memory.
