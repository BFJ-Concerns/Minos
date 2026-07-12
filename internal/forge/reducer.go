package forge

// ReduceRequiredChecks selects the forge-ordered newest attempt for every
// required identity, then folds those decisions into the lifecycle gate.
func ReduceRequiredChecks(required []CheckIdentity, statuses []Status) CheckDecision {
	decision := ChecksPass
	for _, identity := range required {
		// Minos cannot wait on the status it is itself deciding. Provider is part
		// of the identity, so an unrelated forge may still use the same context.
		if identity.Provider == ForgejoProvider && identity.Context == OwnedStatusContext {
			continue
		}

		latest, ambiguous, found := latestStatus(identity, statuses)
		if ambiguous {
			return ChecksFail
		}
		if !found {
			decision = ChecksPending
			continue
		}

		switch latest.State {
		case StatusSuccess:
			// Already passing.
		case StatusNeutral, StatusSkipped:
			if !latest.ProtectionSatisfied {
				return ChecksFail
			}
		case StatusAbsent, StatusQueued, StatusPending:
			decision = ChecksPending
		case StatusFailure, StatusError, StatusCancelled, StatusTimeout:
			return ChecksFail
		default:
			// An unrecognised state must never become an implicit clearance.
			return ChecksFail
		}
	}
	return decision
}

func latestStatus(identity CheckIdentity, statuses []Status) (Status, bool, bool) {
	var latest Status
	found := false
	ambiguous := false
	for _, status := range statuses {
		if status.Provider != identity.Provider || status.Context != identity.Context {
			continue
		}
		if !found || status.ID > latest.ID {
			latest = status
			found = true
			ambiguous = false
			continue
		}
		if status.ID == latest.ID {
			// Forgejo IDs are monotonic and unique. Seeing the same newest ID
			// twice means ordering or normalisation is ambiguous, even when the
			// duplicated payload happens to agree.
			ambiguous = true
		}
	}
	return latest, ambiguous, found
}
