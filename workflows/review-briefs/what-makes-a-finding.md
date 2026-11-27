A finding is something this change gets wrong, which its author would want to fix before merging and which you can point to in the change.

A problem the change did not introduce is not a finding. Judge that by the lines, not by the behaviour: a problem in lines the change neither wrote nor altered, which the change does nothing to bring about, was there before it. A problem in lines the change did write is the change's, even where the software misbehaved in the same way before: the author rewrote that code and the fault is in what they wrote. `git show {{target}}:<path>` shows a file as it stood before the change. When you come across a real defect the change did not introduce, give it one entry in `outOfScopeObservations` and move on.

Leave out matters of taste.
