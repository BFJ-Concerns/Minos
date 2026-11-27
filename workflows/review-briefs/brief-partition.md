# Brief partition

A pull request is under review. Its repository keeps review concerns of its own as briefs in a `.review/` directory, and the brief below asks for more than a reading of the change: its owners want a whole part of the repository read for its concern as that part stands, in code the change never touched as much as in code it wrote. The files of that part are listed at the end. Reviewers will read them for the brief's concern and nothing else, and your job is to divide the files among those reviewers. Each unit you return is read by one reviewer, who answers for every file in it.

{{reviewed-change}}

## The brief

<repository-brief path="{{brief-path}}">
{{brief}}
</repository-brief>

## The partition

What you divide is the listed part as it stands, not the change: which files the pull request touched has no bearing on where a boundary falls.

One unit holding every listed file is the ordinary answer. Return it whenever one reviewer could read all of the files with care for this brief's concern. Divide only a part too large for that, and then into as few units as leave each one something a single reviewer can read with care. Every unit costs a reviewer, and a part cut too finely hides the faults that show only when related files are read together.

Put a boundary where this concern lets the part come apart. Files that must be read together to judge the concern belong in one unit, whether that follows a directory, a module or something that cuts across both. Read as much of the repository as you need to see how the files hang together. The directory layout alone is no reason for a boundary.

The partition loses nothing. Every listed path goes in exactly one unit, including a file the concern seems to have little to say about, and no unit names a path that is not listed.

## What to return

`units`, each with:

- `id`: a short slug, unique among the units.
- `concern`: one sentence saying what part of the repository this unit is and what the brief's concern has to read for there. The unit's reviewer is handed that sentence with the unit's files, so make it specific to them.
- `files`: the unit's paths, exactly as listed.

## The files

A JSON array of every path in the part the brief covers.

{{file-inventory}}
