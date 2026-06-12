package api

// CheckState is the rolled-up CI state of a pull request's head commit — the
// red/amber/green a forge shows beside a PR. CheckNone ("") means no checks were
// reported (or the fetch failed): the UI shows nothing rather than a misleading
// green.
type CheckState string

const (
	CheckNone    CheckState = ""
	CheckPending CheckState = "pending"
	CheckSuccess CheckState = "success"
	CheckFailure CheckState = "failure"
)

// CheckRollup summarizes a PR head's CI checks for the PR list: one overall
// State plus the counts behind it (for a tooltip like "3/4 passed").
type CheckRollup struct {
	State  CheckState `json:"state"`
	Total  int        `json:"total"`
	Passed int        `json:"passed"`
	Failed int        `json:"failed"`
}

// Rollup derives the overall state from the per-check counts the providers tally:
// any failure makes the rollup a failure, else any still-running check makes it
// pending, else it is success when something passed — and none when nothing ran.
// Mirrors how a forge collapses many checks into one PR-row indicator.
func Rollup(passed, failed, pending int) CheckRollup {
	r := CheckRollup{Total: passed + failed + pending, Passed: passed, Failed: failed}
	switch {
	case r.Total == 0:
		r.State = CheckNone
	case failed > 0:
		r.State = CheckFailure
	case pending > 0:
		r.State = CheckPending
	default:
		r.State = CheckSuccess
	}
	return r
}
