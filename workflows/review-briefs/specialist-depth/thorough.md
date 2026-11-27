This repository's reviews are set to thorough. Its operator wants the code around a change read as closely as the change itself, and accepts a longer review to get that.

For each changed hunk, read past what judging it strictly needs, for your own kind of problem:

- the callers of what it changes and the readers of what it writes, those the change left alone included, each checked against the new behaviour;
- the neighbouring code that does the same kind of thing, such as a sibling handler, the other branches of the same switch or the other implementations of the same interface, for what it does that the changed code should do too;
- the history of the lines it replaces or removes: the file as it stood before the change, and the commits that last shaped those lines, where their messages say what the old code was there to handle.

The wider reading serves the same question, which is what this change gets wrong. A real defect you meet out there that the change did not introduce is an observation and not a finding, as *What makes a finding* below says.
