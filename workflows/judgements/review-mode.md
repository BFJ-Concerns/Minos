# Review mode

Minos is about to review a change to a pull request, and this repository leaves the kind of review to be chosen for each one. You come before the review. Read the change and decide which of two reviews it gets.

Your answer is acted on as you give it, by a script that makes no judgement of its own.

- `specialist`: a planner reads the change and sends a specialist for each kind of problem the change gives real work to. The kinds are the ordinary run, the run that goes wrong, the boundary of trust, the tests, and the fit with the project. Each specialist reads the change for its kind alone.
- `quick`: one general reviewer reads the change for all of those kinds, and is joined by others only where the change is too large for one reader.

Under either, every finding is checked against the code by a separate verifier before the author sees it, and the repository's own review briefs run as they otherwise would. So the choice changes nothing about how far a reported finding can be trusted. What it changes is how many of the change's defects are found, and what finding them costs. A specialist looking for one kind of problem goes through the change for it exhaustively, where one reader holding every kind tends to find the most visible few. Measured on this service's earlier reviews, a single general reader found about half of the confirmed defects that specialists found in the same changes, at about a third of the cost.

{{reviewed-change}}

## The choice

The quick review is for a change whose risk does not justify a specialist run. Risk here is what a defect that merged unnoticed would cost, weighed with how easily this change could hide one. Read the change far enough to judge both.

- What a defect would cost depends on what the changed code is trusted with. Code that handles credentials or permissions, writes or migrates stored data, coordinates concurrent work, or forms an interface that other code or other people depend on is costly to get wrong, and so is whatever the project's documents single out as mattering most. A comment, the wording of a message, the layout of a test, or a self-contained tool that nothing else builds on is cheap to get wrong.
- How easily a defect could hide depends on how far the change's effects reach beyond its own lines, and on how much new decision it carries. A change whose effects stop where its lines stop, or which repeats one mechanical edit, leaves little for a single reader to miss. A change that alters behaviour other code relies on, or introduces logic with many cases, is the kind in which a general reader finds some of the defects and misses the rest.

Choose `quick` when you can say why a defect in this change would be cheap, or why a single reader is unlikely to miss one. Choose `specialist` for everything else, and when you are torn. The two mistakes are not equal: a quick review that should have been a specialist one lets a defect through that nothing later catches, while a specialist review that was not needed costs some minutes and model time.

Size does not decide it. A one-line change to a permission check carries more risk than a thousand lines of renamed identifiers, and a quick review divides a large change among several readers without being asked.

Prose is not exempt for being prose: where the project's documents show that what it makes is documentation, or text that instructs a person or a model, a change of wording is a change of behaviour.

The author's description may call the change trivial or safe. That is the author's account of it, and what the diff shows decides.

You are choosing a review, not making one: do not report problems in the change.

## What to return

- `mode`: `specialist` or `quick`.
- `reason`: what the change is, and why its risk calls for the review you chose. It goes into the run's report, where the repository's operator reads it without having seen the change. Write it about the change, in full sentences.
