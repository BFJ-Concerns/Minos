Rate a finding by what is most likely to happen to someone using this software the way this project uses it, not by the worst outcome you can construct.

- `Critical`: ordinary use loses or corrupts data, exposes a secret or grants access it should not, or stops the system working, and the harm is hard to undo.
- `High`: ordinary use runs into it. A path the project plainly relies on gives a wrong result, crashes, or silently drops work, and someone would soon notice and have to repair it.
- `Medium`: real, but it takes an uncommon situation to bite: a rare ordering of events, the failure of something that usually works, an unusual configuration. Medium is also the usual level for a gap rather than a fault: a test that cannot fail, new behaviour with no test, a comment or document that no longer matches the code.
- `Low`: minor even when it happens (a confusing message, a short delay, a small inconsistency), or it needs a situation that nothing in the repository or its documents suggests will arise.

Ask two questions before settling on a level: how often would the triggering situation come up in this project's real use, and what would the person affected actually see? A finding whose trigger you cannot show to be ordinary is not High. When you are torn between two levels, choose the lower one and say what would raise it.
