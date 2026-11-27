You have the whole change: every hunk of `git diff {{target}}...{{head}}`.

Before you read it closely, see how much of it there is and decide whether it is one reader's work. Most changes are. A change is divided only when it is too large for one reader to give every changed hunk the reading this review asks for, so that the later hunks would get less than the earlier ones did. Length alone does not make it so: a rename carried through many files, generated output, or one coherent piece that happens to be long is still read as one.

When you do not divide it, leave `shares` empty and review the change.

When you divide it, return the division in `shares`, two or more of them, and leave the reading to the reviewers who are then sent, one for each share, each reading its share as you would have read the whole. None of them can divide again, so make every share one reader's work. Each share carries:

- `id`: a short unique slug.
- `focus`: a sentence saying what that part of the change does. Its reviewer is handed the sentence, so make it specific to this change.
- `paths`: the changed paths that reviewer answers for. Keep together the paths that have to be read together to be understood, and put every changed path in exactly one share.

Anything you had already found by then goes in `findings` all the same, and is checked like any other finding.
