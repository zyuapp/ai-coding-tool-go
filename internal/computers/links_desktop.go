package computers

import (
	"context"
	"encoding/json/jsontext"
	"slices"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/host"
)

// listeners is a set of subscribers, told in the order they subscribed.
type listeners[T any] struct {
	mu   sync.Mutex
	next int
	held map[int]T
	ids  []int
}

func (l *listeners[T]) add(listener T) func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.held == nil {
		l.held = map[int]T{}
	}
	id := l.next
	l.next++
	l.held[id] = listener
	l.ids = append(l.ids, id)
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if _, ok := l.held[id]; ok {
			delete(l.held, id)
			l.ids = slices.DeleteFunc(l.ids, func(held int) bool { return held == id })
		}
	}
}

func (l *listeners[T]) each(tell func(T)) {
	l.mu.Lock()
	held := make([]T, 0, len(l.held))
	for _, id := range l.ids {
		if listener, ok := l.held[id]; ok {
			held = append(held, listener)
		}
	}
	l.mu.Unlock()
	for _, listener := range held {
		tell(listener)
	}
}

// LinksDesktop is the other computers, held by the links this process keeps open to them, as the
// runtime's desktop reaches them.
type LinksDesktop struct {
	Links *ComputerLinks
	reads func() *ComputerReads
	// announceName tells the paired computers this one's new name.
	announceName func(ctx context.Context) error
	changed      listeners[func(string, []*domain.ComputerLink)]
	states       listeners[func(string, application.WorkspaceState)]
	notices      listeners[func(string, contracts.ThreadNotice)]
}

var _ host.ComputerDesktop = (*LinksDesktop)(nil)

// NewLinksDesktop opens the links described by options, pushing what they report to the desktop's
// subscribers; options' own callbacks are replaced. reads answers a paired computer's thread reads,
// and announceName tells the computers on the bridge this one's new name. Start the links with
// Links.Start once the runtime listens.
func NewLinksDesktop(options LinksOptions, reads func() *ComputerReads, announceName func(ctx context.Context) error) *LinksDesktop {
	desktop := &LinksDesktop{reads: reads, announceName: announceName}
	options.OnChanged = func(name string, links []*domain.ComputerLink) {
		desktop.changed.each(func(listener func(string, []*domain.ComputerLink)) { listener(name, links) })
	}
	options.OnState = func(id string, state *application.WorkspaceState) {
		desktop.states.each(func(listener func(string, application.WorkspaceState)) { listener(id, *state) })
	}
	options.OnNotice = func(id string, notice contracts.ThreadNotice) {
		desktop.notices.each(func(listener func(string, contracts.ThreadNotice)) { listener(id, notice) })
	}
	desktop.Links = NewComputerLinks(options)
	return desktop
}

func (d *LinksDesktop) DiscoverComputers(ctx context.Context) ([]*domain.DiscoveredComputer, error) {
	return d.Links.Discover(ctx)
}

func (d *LinksDesktop) PairComputer(ctx context.Context, address, name, code string) error {
	return d.Links.Pair(ctx, address, name, code)
}

func (d *LinksDesktop) ForgetComputer(_ context.Context, id string) error { return d.Links.Forget(id) }

func (d *LinksDesktop) ReconnectComputer(_ context.Context, id *string) error {
	d.Links.Reconnect(id)
	return nil
}

func (d *LinksDesktop) RenameComputer(ctx context.Context, name string) error {
	if err := d.Links.Rename(name); err != nil {
		return err
	}
	if d.announceName == nil {
		return nil
	}
	return d.announceName(ctx)
}

func (d *LinksDesktop) LabelComputer(_ context.Context, id, name string) error {
	return d.Links.Label(id, name)
}

func (d *LinksDesktop) SendToComputer(ctx context.Context, id string, inputs []application.WorkspaceInput) (contracts.WorkspaceCommandResult, error) {
	return d.Links.Send(ctx, id, inputs)
}

func (d *LinksDesktop) QueryComputerThreads(ctx context.Context, id string, query contracts.ComputerThreadQuery) (jsontext.Value, error) {
	return d.reads().Threads(ctx, &id, query)
}

func (d *LinksDesktop) OnComputersChanged(listener func(name string, links []*domain.ComputerLink)) func() {
	return d.changed.add(listener)
}

func (d *LinksDesktop) OnComputerState(listener func(id string, state application.WorkspaceState)) func() {
	return d.states.add(listener)
}

func (d *LinksDesktop) OnComputerNotice(listener func(id string, notice contracts.ThreadNotice)) func() {
	return d.notices.add(listener)
}
