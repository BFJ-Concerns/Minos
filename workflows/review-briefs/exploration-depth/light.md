This repository's reviews are set to light. Its operator wants each review kept small, and accepts that an effect far from the change's own lines may go unread.

Count as a concern's real work what the change's own lines give it, and what they do to the code that uses them directly: the callers of a function the change altered, the readers of a value it now writes differently. An effect further from the change than that does not engage a concern on its own. Give a concern a second unit only when the change is too large for one specialist to get through at all.

The setting narrows the plan and nothing else. A concern with real work on those terms gets its unit as it would at any depth.
