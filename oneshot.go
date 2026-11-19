package codex

import (
	"context"
	"os"

	contracts "github.com/Herrscherd/herrscher-contracts"
)

type oneShotResponder struct {
	run func(context.Context, contracts.Prompt) (string, error)

	gatewayHomeRemovedOnClose string
}

func (o *oneShotResponder) Respond(ctx context.Context, prompt contracts.Prompt, _ func(contracts.BackendEvent)) (string, error) {
	if _, asked := contracts.ApprovalAskerFrom(ctx); asked {
		return "", errUngatedSession
	}
	return o.run(ctx, prompt)
}

func (o *oneShotResponder) Close() error {
	if o.gatewayHomeRemovedOnClose == "" {
		return nil
	}
	return os.RemoveAll(o.gatewayHomeRemovedOnClose)
}
