package worker

import "go.temporal.io/sdk/workflow"

// PersistenceProbe is an operational check, not a novel-production workflow.
func PersistenceProbe(ctx workflow.Context, reference string) (string, error) {
	state := "WAITING"
	if err := workflow.SetQueryHandler(ctx, "state", func() (string, error) { return state, nil }); err != nil {
		return "", err
	}
	signal := workflow.GetSignalChannel(ctx, "continue")
	for {
		var accepted string
		signal.Receive(ctx, &accepted)
		if accepted == reference {
			state = "DONE"
			return reference, nil
		}
	}
}
