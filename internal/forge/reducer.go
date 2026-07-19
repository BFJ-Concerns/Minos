package forge

// ReduceRequiredChecks reports whether the forge's required checks pass.
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
			// An unrecognised state is not a passing check.
			return ChecksFail
		}
	}
	return decision
}

// FailedRequiredChecks returns only checks whose newest unambiguous forge
// status is explicitly red. Missing, pending, unknown or ambiguous state still
// blocks a passing decision, but does not pretend there is a failure to fix.
func FailedRequiredChecks(required []CheckIdentity, statuses []Status) []CheckIdentity {
	failed := make([]CheckIdentity, 0)
	for _, identity := range required {
		if identity.Provider == ForgejoProvider && identity.Context == OwnedStatusContext {
			continue
		}
		latest, ambiguous, found := latestStatus(identity, statuses)
		if ambiguous || !found {
			continue
		}
		switch latest.State {
		case StatusFailure, StatusError, StatusCancelled, StatusTimeout:
			failed = append(failed, identity)
		}
	}
	return failed
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
