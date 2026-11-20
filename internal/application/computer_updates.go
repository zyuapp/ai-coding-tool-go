package application

import (
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

var upToDate = UpkeepPart{Text: "Up to date", Tone: new(UpkeepPartToneQuiet)}

func threads(count int) string {
	if count == 1 {
		return "1 thread"
	}
	return fmt.Sprintf("%d threads", count)
}

func upkeepToned(text string, tone UpkeepPartTone) UpkeepPart {
	return UpkeepPart{Text: text, Tone: &tone}
}

// The computer whose software a row moves on: a paired one by id and by what it accepts, or this one, which accepts everything.
type upkeepTarget struct {
	ComputerID *string
	Supports   func(command contracts.AppCommand) bool
}

var here = upkeepTarget{Supports: func(contracts.AppCommand) bool { return true }}

func pairedTarget(computer *PairedComputer) upkeepTarget {
	return upkeepTarget{ComputerID: &computer.ID, Supports: func(command contracts.AppCommand) bool {
		return contracts.SupportsComputerCommand(computer.Capabilities, command)
	}}
}

// `command` naming the target's computer, when it is a paired one. Takes the commands that carry a computerId.
func at(target upkeepTarget, command contracts.AppCommand) contracts.AppCommand {
	if target.ComputerID == nil {
		return command
	}
	switch command := command.(type) {
	case contracts.ExternalAppCommandAppUpdate:
		command.ComputerID = target.ComputerID
		return command
	case contracts.ExternalAppCommandAppCancelUpdate:
		command.ComputerID = target.ComputerID
		return command
	case contracts.EngineCommandEngineUpdate:
		command.ComputerID = target.ComputerID
		return command
	}
	return command
}

// The text without one trailing period.
func upkeepTrimPeriod(text string) string {
	return strings.TrimSuffix(text, ".")
}

// Where a newer build of the app stands there: on its way, waiting, failed, or one only that computer can install.
func appItem(target upkeepTarget, remote *WorkspaceState, own *domain.AppBuild) *UpkeepItem {
	item := func(state UpkeepPart, action *UpkeepAction) *UpkeepItem {
		var version *string
		if remote.AppBuild != nil {
			version = &remote.AppBuild.Version
		}
		return &UpkeepItem{Key: "app", Label: "AI Coding Tool", Version: version, State: state, Action: action}
	}
	if !target.Supports(contracts.ExternalAppCommandAppUpdate{}) {
		return item(UpkeepPart{Text: "Update it there once to manage it from here"}, nil)
	}
	request := remote.AppUpdateRequest
	if installing, ok := request.(domain.AppUpdateRequestInstalling); ok {
		return item(upkeepToned("Installing "+installing.Version+"…", UpkeepPartToneProgress), nil)
	}
	if downloading, ok := remote.AppUpdate.(domain.AppUpdateDownloading); ok {
		return item(upkeepToned("Downloading "+downloading.Version+" · "+strconv.FormatFloat(downloading.Percent, 'f', -1, 64)+"%", UpkeepPartToneProgress), nil)
	}
	if failed, ok := request.(domain.AppUpdateRequestFailed); ok {
		return item(upkeepToned(upkeepTrimPeriod(failed.Message), UpkeepPartToneError), &UpkeepAction{Label: "Try again", Command: at(target, contracts.ExternalAppCommandAppUpdate{})})
	}
	var offered *string
	switch update := remote.AppUpdate.(type) {
	case domain.AppUpdateAvailable:
		offered = &update.Version
	case domain.AppUpdateReady:
		offered = &update.Version
	}
	if offered != nil {
		running := RunningThreads(remote)
		if _, waiting := request.(domain.AppUpdateRequestWaiting); waiting {
			return item(upkeepToned("Waiting for "+threads(running), UpkeepPartToneProgress), &UpkeepAction{Label: "Cancel", Command: at(target, contracts.ExternalAppCommandAppCancelUpdate{})})
		}
		// Updating never cuts a run off: the install waits for running threads, which the line says beforehand.
		waits := ""
		if running > 0 {
			waits = " · waits for " + threads(running)
		}
		return item(upkeepToned(*offered+" available"+waits, UpkeepPartToneOffer), &UpkeepAction{Label: "Update", Command: at(target, contracts.ExternalAppCommandAppUpdate{})})
	}
	// A copy that cannot update itself only learns of a newer build from one that runs it.
	if build := remote.AppBuild; build != nil && !build.SelfUpdating && own != nil && domain.CompareVersions(own.Version, build.Version) > 0 {
		return item(upkeepToned(own.Version+" available · update it there", UpkeepPartToneOffer), nil)
	}
	return item(upToDate, nil)
}

// The engines that computer reports or is updating, by its own ids: the ones this build knows in their
// order, then any only a newer build knows. Until it has reported any, this build's own, to be checked.
func reportedEngines(remote *WorkspaceState, status map[domain.EngineID]*domain.EngineReadiness) []domain.EngineID {
	var reported []domain.EngineID
	add := func(engine domain.EngineID) {
		if !slices.Contains(reported, engine) {
			reported = append(reported, engine)
		}
	}
	for _, engine := range domain.AgentEngines {
		if _, ok := status[string(engine)]; ok {
			add(string(engine))
		}
	}
	unknown := make([]string, 0)
	for engine := range status {
		if !domain.IsAgentEngine(engine) {
			unknown = append(unknown, engine)
		}
	}
	sort.Strings(unknown)
	for _, engine := range unknown {
		add(engine)
	}
	if remote.EngineUpdating != nil {
		add(string(*remote.EngineUpdating))
	}
	for _, engine := range remote.EngineUpdateQueue {
		add(string(engine))
	}
	if len(reported) == 0 {
		engines := make([]domain.EngineID, len(domain.AgentEngines))
		for index, engine := range domain.AgentEngines {
			engines[index] = string(engine)
		}
		return engines
	}
	ordered := make([]domain.EngineID, 0, len(reported))
	for _, engine := range domain.AgentEngines {
		if slices.Contains(reported, string(engine)) {
			ordered = append(ordered, string(engine))
		}
	}
	for _, engine := range reported {
		if !domain.IsAgentEngine(engine) {
			ordered = append(ordered, engine)
		}
	}
	return ordered
}

// Where an engine stands there: missing, too old to run, behind, or current, and whether the app can
// move it on. One only a newer build knows is named by its id and updated there.
func engineItem(target upkeepTarget, remote *WorkspaceState, status map[domain.EngineID]*domain.EngineReadiness, engine domain.EngineID) *UpkeepItem {
	readiness := status[engine]
	known := domain.IsAgentEngine(engine)
	item := func(state UpkeepPart, action *UpkeepAction) *UpkeepItem {
		var version *string
		if readiness != nil {
			version = readiness.Version
		}
		return &UpkeepItem{Key: engine, Label: domain.EngineLabel(engine), Version: version, State: state, Action: action}
	}
	if remote.EngineUpdating != nil && string(*remote.EngineUpdating) == engine {
		return item(upkeepToned("Updating…", UpkeepPartToneProgress), nil)
	}
	if slices.ContainsFunc(remote.EngineUpdateQueue, func(queued domain.AgentEngine) bool { return string(queued) == engine }) {
		return item(upkeepToned("Waiting to update", UpkeepPartToneProgress), nil)
	}
	if readiness == nil {
		return item(upkeepToned("Checking…", UpkeepPartToneQuiet), nil)
	}
	switch readiness.Access {
	case domain.EngineAccessMissing:
		return item(upkeepToned("Not installed", UpkeepPartToneQuiet), nil)
	case domain.EngineAccessUnavailable:
		return item(upkeepToned("Won't start", UpkeepPartToneError), nil)
	}
	update := domain.AgentEngineEngineUpdate(readiness)
	if update == nil {
		return item(upToDate, nil)
	}
	command := contracts.EngineCommandEngineUpdate{Engine: domain.AgentEngine(engine)}
	upgradable := known && update.Command != nil && *update.Command != "" && target.Supports(command)
	var action *UpkeepAction
	where := " · update it there"
	if upgradable {
		action = &UpkeepAction{Label: "Update", Command: at(target, command)}
		where = ""
	}
	if readiness.Required != nil && *readiness.Required != "" {
		// An engine too old to speak to stops its runs; one only behind hides newer models.
		if readiness.Access == domain.EngineAccessOutdated {
			return item(upkeepToned("Needs "+*readiness.Required+" to run"+where, UpkeepPartToneError), action)
		}
		return item(upkeepToned("Needs "+*readiness.Required+" for newer models"+where, UpkeepPartToneOffer), action)
	}
	return item(upkeepToned(update.Target+" available"+where, UpkeepPartToneOffer), action)
}

// How the line to that computer stands, which a restart it was asked for explains.
func connection(computer *PairedComputer, remote *WorkspaceState) []*UpkeepPart {
	if computer.Status == domain.ComputerStatusConnected {
		return []*UpkeepPart{new(upkeepToned("Connected", UpkeepPartToneLive))}
	}
	if remote != nil {
		if installing, ok := remote.AppUpdateRequest.(domain.AppUpdateRequestInstalling); ok {
			return []*UpkeepPart{new(upkeepToned("Restarting into "+installing.Version+"…", UpkeepPartToneProgress))}
		}
	}
	away := &UpkeepPart{Text: "Offline"}
	if computer.Status == domain.ComputerStatusConnecting {
		away.Text = "Reconnecting…"
	}
	if computer.Error != nil && *computer.Error != "" {
		return []*UpkeepPart{away, {Text: upkeepTrimPeriod(*computer.Error)}}
	}
	return []*UpkeepPart{away}
}

func ComputerUpdatesComputerUpkeep(paired []*PairedComputer, own *domain.AppBuild) []*ComputerUpkeep {
	rows := make([]*ComputerUpkeep, len(paired))
	for index, computer := range paired {
		remote := computer.State
		status := connection(computer, remote)
		// A computer restarting into an update comes back on its own; one that is away can be dialled now.
		if computer.Status != domain.ComputerStatusConnected {
			row := &ComputerUpkeep{ID: computer.ID, Status: status, Items: []*UpkeepItem{}}
			restarting := false
			if remote != nil {
				_, restarting = remote.AppUpdateRequest.(domain.AppUpdateRequestInstalling)
			}
			if !restarting {
				row.Action = &UpkeepAction{Label: "Reconnect", Command: contracts.ComputerCommandComputersReconnect{ID: &computer.ID}}
			}
			rows[index] = row
			continue
		}
		if remote == nil {
			rows[index] = &ComputerUpkeep{ID: computer.ID, Status: status, Items: []*UpkeepItem{}}
			continue
		}
		rows[index] = &ComputerUpkeep{ID: computer.ID, Status: status, Items: upkeepItems(pairedTarget(computer), remote, own)}
	}
	return rows
}

// The engines a state reports, by the ids it names them by.
func upkeepReportedStatus(status *domain.EngineStatus) map[domain.EngineID]*domain.EngineReadiness {
	engines := map[domain.EngineID]*domain.EngineReadiness{}
	for _, engine := range domain.AgentEngines {
		if readiness := status.Get(engine); readiness != nil {
			engines[string(engine)] = readiness
		}
	}
	return engines
}

func upkeepItems(target upkeepTarget, state *WorkspaceState, own *domain.AppBuild) []*UpkeepItem {
	// Keyed by the engines that computer knows, which may include one this build does not.
	engines := upkeepReportedStatus(state.EngineStatus)
	items := []*UpkeepItem{appItem(target, state, own)}
	for _, engine := range reportedEngines(state, engines) {
		items = append(items, engineItem(target, state, engines, engine))
	}
	return items
}

// This computer's own app and engines, as a paired phone reads them, each moved on by a command naming no computer.
func OwnUpkeep(state *WorkspaceState) []*UpkeepItem {
	return upkeepItems(here, state, nil)
}

func samePart(before, next *UpkeepPart) bool {
	return before.Text == next.Text && upkeepSameTone(before.Tone, next.Tone)
}

func upkeepSameTone(a, b *UpkeepPartTone) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameAction(before, next *UpkeepAction) bool {
	if before == nil || next == nil {
		return before == nil && next == nil
	}
	return before.Label == next.Label && same(before.Command, next.Command)
}

// Streaming state rebuilds the rows on every change there, so an unchanged page keeps its old ones.
func SameComputerUpkeep(before, next []*ComputerUpkeep) bool {
	if len(before) != len(next) {
		return false
	}
	for index, row := range before {
		other := next[index]
		if row.ID != other.ID || !sameAction(row.Action, other.Action) || len(row.Status) != len(other.Status) || len(row.Items) != len(other.Items) {
			return false
		}
		for at, part := range row.Status {
			if !samePart(part, other.Status[at]) {
				return false
			}
		}
		for at, item := range row.Items {
			that := other.Items[at]
			if item.Key != that.Key || !sameOptionalText(item.Version, that.Version) || !samePart(&item.State, &that.State) || !sameAction(item.Action, that.Action) {
				return false
			}
		}
	}
	return true
}
