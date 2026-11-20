package computeruse

import (
	"context"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
	"github.com/zyuapp/ai-coding-tool-go/internal/mobile"
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

// CreateComputerBridge hands a paired computer this workspace whole, driven in the window's own
// inputs, and read the way the window reads it.
func CreateComputerBridge(bridge BridgeHost) mobile.WorkspaceHooks {
	return computerBridge{bridge}
}

type computerBridge struct{ BridgeHost }

func (b computerBridge) Name() string { return b.BridgeHost.Name() }

func (b computerBridge) Snapshot() application.WorkspaceUpdate { return b.Publisher.Snapshot() }

func (b computerBridge) Subscribe(listener func(update application.WorkspaceUpdate)) func() {
	return b.Publisher.Subscribe(host.RuntimeSubscriber(listener))
}

func (b computerBridge) Input(ctx context.Context, inputs []application.WorkspaceInput) (contracts.ComputerServerMessageResultResult, error) {
	result := contracts.ComputerServerMessageResultResultTrue{Revision: b.Publisher.Revision()}
	for _, input := range inputs {
		response := b.Publisher.Request(ctx, input)
		answered, ok := response.(contracts.ComputerServerMessageResultResultTrue)
		if !ok {
			if refused, ok := response.(contracts.ComputerServerMessageResultResult); ok {
				return refused, nil
			}
			return nil, errors.New("The workspace runtime gave no answer.")
		}
		result.Revision = answered.Revision
		if answered.TaskID != nil {
			result.TaskID = answered.TaskID
		}
		if answered.Answer != nil {
			result.Answer = answered.Answer
		}
	}
	return result, nil
}

func (b computerBridge) Query(ctx context.Context, query contracts.ComputerQuery) (any, error) {
	return b.Reads.Answer(ctx, query)
}
