---
summary: what the change lets someone or something do that it should not
---
What the change lets someone or something do that it should not.

Look at what crosses a boundary of trust:

- credentials and keys: where they are kept, who can read them, what they are used for, and where they might be written out;
- input that arrives from outside the program's control, such as a person, a model's answer, a network peer, a file or an environment variable, and what that input can reach;
- the permissions on what the change writes, and who else can read or replace it;
- anything the change runs, signals or deletes that could be aimed at the wrong target.

Judge the change against the trust the project's documents actually describe, not against an ideal posture. A party the project trusts is trusted here, and a protection the project has chosen not to have is not a finding.

A change with no such surface has nothing for this concern, and `inapplicable` is then the correct answer.

Your ground is the boundary of trust. A crash or a wrong result that needs no hostile or untrusted party is `logic`'s or `failure`'s, however alarming it sounds. A missing test of a boundary is `tests`'s.
