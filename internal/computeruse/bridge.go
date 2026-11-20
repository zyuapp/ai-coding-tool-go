package computeruse

import (
	"context"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
)

// Publisher is the runtime publisher a paired computer is served from; *host.Publisher is one.
type Publisher interface {
	Revision() float64
	Snapshot() application.WorkspaceUpdate
	Subscribe(subscriber host.RuntimeSubscriber) func()
	Request(ctx context.Context, input application.WorkspaceInput) application.WorkspaceResponseResult
}

var _ Publisher = (*host.Publisher)(nil)

// Reads answers a paired computer's reads the way the window reads them (computer-queries).
type Reads interface {
	Answer(ctx context.Context, query contracts.ComputerQuery) (any, error)
}

type BridgeHost struct {
	Publisher Publisher
	Reads     Reads
	// Name is what this computer calls itself to the computers it hands the workspace to.
	Name func() string
}

// WorkspaceHooks is what another computer is handed (mobile-server's WorkspaceHooks).
type WorkspaceHooks struct {
	Name      func() string
	Snapshot  func() application.WorkspaceUpdate
	Subscribe func(listener func(update application.WorkspaceUpdate)) func()
	Input     func(ctx context.Context, inputs []application.WorkspaceInput) application.WorkspaceResponseResult
	Query     func(ctx context.Context, query contracts.ComputerQuery) (any, error)
}

// CreateComputerBridge hands a paired computer this workspace whole, driven in the window's own
// inputs, and read the way the window reads it.
func CreateComputerBridge(bridge BridgeHost) WorkspaceHooks {
	publisher := bridge.Publisher
	return WorkspaceHooks{
		Name:     bridge.Name,
		Snapshot: publisher.Snapshot,
		Subscribe: func(listener func(update application.WorkspaceUpdate)) func() {
			return publisher.Subscribe(host.RuntimeSubscriber(listener))
		},
		Input: func(ctx context.Context, inputs []application.WorkspaceInput) application.WorkspaceResponseResult {
			result := contracts.ComputerServerMessageResultResultTrue{Revision: publisher.Revision()}
			for _, input := range inputs {
				response := publisher.Request(ctx, input)
				answered, ok := response.(contracts.ComputerServerMessageResultResultTrue)
				if !ok {
					return response
				}
				result.Revision = answered.Revision
				if answered.TaskID != nil {
					result.TaskID = answered.TaskID
				}
				if answered.Answer != nil {
					result.Answer = answered.Answer
				}
			}
			return result
		},
		Query: func(ctx context.Context, query contracts.ComputerQuery) (any, error) {
			return bridge.Reads.Answer(ctx, query)
		},
	}
}
