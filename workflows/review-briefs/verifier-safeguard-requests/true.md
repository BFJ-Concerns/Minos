## Requests for a safeguard

An entry may claim no fault and ask instead for a new safeguard (a guard, a check or a test) against a situation that nothing in the repository or its documents suggests will arise. Its title names the safeguard that is missing, and its explanation says which situation the safeguard would guard against and what the code as it stands would do if that situation arose. This project has asked to be sent these requests. One you confirm is filed for the project as a suggestion, apart from the defects. It is never sent to the author as a defect and never holds the change back. One you refute reaches nobody.

An entry is a request by what it claims, and nothing else marks it as one. An entry that says the code goes wrong in a situation the repository shows arising claims a fault, even where the remedy it suggests is a new check. So does one that claims a gap in what the change itself delivers, such as new behaviour with no test. Judge those as faults.

A request claims three things: the safeguard is absent; without it, the code would do what the request says if the situation arose; and nothing in the repository rules the situation out or shows that the project chose to do without the protection. Your verdict is on those three claims, by the same standard of proof as any other.

- `confirmed`: the safeguard is not there, the code would do what the request says, and you looked for whatever would rule the situation out or record the project's choice (the callers, the types, the tests, the project documents) and found neither. Your reason says where the safeguard would sit and where you looked.
- `refuted`: you found one of these, and your reason cites it by file and line: the safeguard itself, in a place the request missed; code that would not do what the request says; or a line or document that rules the situation out, or shows that the project decided to do without the protection. That the entry describes no fault is not a refutation, because it claims none. Neither is the situation being unlikely, which the request says itself.
- `unsettled`: reading the repository cannot decide one of the three claims, as when something outside it may supply the safeguard or rule the situation out. The section above applies as it does to a fault.

You may find instead that the situation does arise: a caller passes the value, or a test or a document expects the state. The entry then understates a fault. Confirm it, cite in your reason where the situation arises, say there that this makes the entry a defect and not a request, and give it the severity of the fault. Your reason is what tells the two apart from here on: without it the entry would be filed as a suggestion, and the change could merge with the fault in it.

*Whether the change introduced it* applies to a request as it does to a fault, the lines at fault being those the safeguard would protect.

Requests reach you from the reviewers alone. Do not raise one yourself: `newFindings` and `outOfScopeObservations`, described below, take defects only, so a request you refute as predating the change is not returned as an observation.
