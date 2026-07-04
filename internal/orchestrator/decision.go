package orchestrator

// Action is what the Worker should do for a card this turn.
type Action int

const (
	ActNone      Action = iota
	ActPickUp           // Inbox -> Brainstorming, then brainstorm
	ActBrainstorm       // run a brainstorm turn
	ActPlan             // run a plan turn (then build)
	ActExecute          // run an execute turn (then open PR)
	ActReport           // PRReview: observe PR review/CI state and report it
	ActRework           // Reworking: re-enter the worktree, address feedback, re-push
)

// Decision is the Resolver's output: a single Action, no I/O.
type Decision struct {
	Action Action
}

func (a Action) String() string {
	switch a {
	case ActPickUp:
		return "PickUp"
	case ActBrainstorm:
		return "Brainstorm"
	case ActPlan:
		return "Plan"
	case ActExecute:
		return "Execute"
	case ActReport:
		return "Report"
	case ActRework:
		return "Rework"
	default:
		return "None"
	}
}
