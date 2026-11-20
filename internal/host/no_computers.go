package host

import (
	"context"
	"encoding/json/jsontext"
	"errors"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// NoComputers is a host that pairs with no other computer: nothing is found, nothing is held, and
// nothing is carried.
var NoComputers ComputerDesktop = noComputers{}

type noComputers struct{}

func (noComputers) DiscoverComputers(context.Context) ([]*domain.DiscoveredComputer, error) {
	return []*domain.DiscoveredComputer{}, nil
}

func (noComputers) PairComputer(context.Context, string, string, string) error {
	return errors.New("This computer cannot pair with others.")
}

func (noComputers) ForgetComputer(context.Context, string) error        { return nil }
func (noComputers) ReconnectComputer(context.Context, *string) error    { return nil }
func (noComputers) RenameComputer(context.Context, string) error        { return nil }
func (noComputers) LabelComputer(context.Context, string, string) error { return nil }

func (noComputers) SendToComputer(context.Context, string, []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
	return contracts.WorkspaceCommandResultFalse{Message: "This computer holds no other computer's threads."}, nil
}

func (noComputers) QueryComputerThreads(context.Context, string, contracts.ComputerThreadQuery) (jsontext.Value, error) {
	return nil, errors.New("This computer holds no other computer's threads.")
}

func (noComputers) OnComputersChanged(func(string, []*domain.ComputerLink)) func()  { return func() {} }
func (noComputers) OnComputerState(func(string, application.WorkspaceState)) func() { return func() {} }
func (noComputers) OnComputerNotice(func(string, contracts.ThreadNotice)) func()    { return func() {} }
