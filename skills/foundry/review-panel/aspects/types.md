---
title: Type Design
relevance: The change introduces or reshapes types — classes, structs, enums, interfaces, schemas, domain models. Clearly irrelevant when the diff defines and modifies none.
---

# Type Design

Review the design of the types the change introduces or reshapes. A
well-designed type carries its own rules: the invariants it promises are
enforced by its structure, so the rest of the codebase cannot quietly break
them. A weak type exports that burden to every caller, and the bugs arrive
later, far from the type that permitted them.

## What to examine

First identify the invariants the type is supposed to hold — data-consistency
requirements, valid state transitions, constraints between fields, rules of the
domain it models, preconditions its methods assume. Then judge the design on
four questions:

- **Encapsulation** — can code outside the type put it into a state that
  violates its invariants? Exposed mutable internals, setters that bypass
  validation, and constructors that accept unchecked values all say yes.
- **Expression** — does the type's structure communicate its rules? Prefer
  designs where illegal states cannot be represented at all (a sum type over a
  boolean-and-nullable-field pair; a non-empty collection type over a comment
  saying "never empty"), and compile-time guarantees over runtime checks where
  the language allows.
- **Usefulness** — do the invariants prevent bugs that could actually happen,
  and make calling code easier to reason about? An invariant nobody could
  violate, or one so strict every caller works around it, is not pulling its
  weight.
- **Enforcement** — are the invariants checked where state is created and
  changed? Construction should validate; every mutation point should preserve
  the invariant; enforcement should be consistent across methods, not present
  in some and absent in others.

Designs that deserve a finding when they appear: a domain model that is only a
bag of fields with its rules enforced elsewhere (or nowhere); invariants stated
in documentation but nothing else; validation at some construction sites and
not others; a type relying on its callers' discipline to stay valid.

## What qualifies as a finding

Point at the concrete violation the design permits — the invalid state that can
be constructed, the mutation that bypasses the check — and suggest an
improvement whose complexity is proportionate. A simpler type with fewer
guarantees is often the right design; recommend stronger typing where it
prevents a real bug, not as a doctrine. Weigh suggested changes against the
churn they impose on existing callers.
