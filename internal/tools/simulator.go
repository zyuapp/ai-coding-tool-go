package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const SimulatorServerName = "aicodingtool-simulator"

// SimulatorBridge is the device the thread's own Simulator panel shows: writes are commands, reads are the screen.
type SimulatorBridge interface {
	Command(ctx context.Context, write contracts.SimulatorWrite) error
	Read(ctx context.Context, read contracts.SimulatorRead) (contracts.SimulatorReadResult, error)
}

// A first boot of a fresh device can take minutes; one already up answers at once.
const simulatorBootWaitMs = 240_000

// simulatorSettle is long enough for a tap's animation to land before the screen is read back.
var simulatorSettle = 400 * time.Millisecond

const maxDepthIndent = 8

func simulatorPoint(axis string) Field {
	return Number().Min(0).Max(10_000).Describe(axis + " in points from the screen's top left, as simulator_read measures it.")
}

func simulatorElementLine(element *domain.SimulatorElement) string {
	x, y, width, height := element.Frame[0], element.Frame[1], element.Frame[2], element.Frame[3]
	label, value, identifier := "", "", ""
	if element.Label != nil {
		label = " " + jsonString(*element.Label)
	}
	if element.Value != nil {
		value = " = " + jsonString(*element.Value)
	}
	if element.Identifier != nil {
		identifier = " #" + *element.Identifier
	}
	indent := strings.Repeat("  ", int(min(element.Depth, maxDepthIndent)))
	return fmt.Sprintf("%s[%s] %s%s%s%s (%s,%s %sx%s)", indent, element.Ref, element.Role, label, value, identifier, jsNumber(x), jsNumber(y), jsNumber(width), jsNumber(height))
}

func simulatorSnapshotText(snapshot *domain.SimulatorSnapshot) string {
	application := "Unknown app"
	if snapshot.Application != nil {
		application = *snapshot.Application
	}
	elements := "The app on screen exposes no accessibility elements."
	if len(snapshot.Elements) > 0 {
		lines := make([]string, len(snapshot.Elements))
		for index, element := range snapshot.Elements {
			lines[index] = simulatorElementLine(element)
		}
		elements = strings.Join(lines, "\n")
	}
	return fmt.Sprintf("%s on %s — screen %sx%s points\n%s", application, snapshot.Device, jsNumber(snapshot.Width), jsNumber(snapshot.Height), elements)
}

func simulatorReadText(result contracts.SimulatorReadResult) string {
	switch result := result.(type) {
	case contracts.SimulatorReadResultNoSimulator:
		return result.Reason
	case contracts.SimulatorReadResultStatus:
		label := domain.SimulatorLabel(&result.Device)
		if result.Error != nil && *result.Error != "" {
			return label + " could not start: " + *result.Error
		}
		if result.Booted {
			return label + " is running."
		}
		return label + " is still starting."
	case contracts.SimulatorReadResultSnapshot:
		var parts []string
		if result.Shot != nil {
			parts = append(parts, fmt.Sprintf("Screenshot saved to %s (%sx%s pixels). Open that file to see the screen.", result.Shot.Path, jsNumber(result.Shot.Width), jsNumber(result.Shot.Height)))
		}
		return strings.Join(append(parts, simulatorSnapshotText(&result.Snapshot)), "\n\n")
	}
	return ""
}

func simulatorReport(work func() (string, error)) ToolResult {
	return reportAs("Simulator error: ", work)
}

// simulatorAct answers every gesture with the screen it left behind, read once the animation it started has landed.
func simulatorAct(ctx context.Context, bridge SimulatorBridge, input domain.SimulatorInput) (string, error) {
	if err := bridge.Command(ctx, contracts.SimulatorWriteSimulatorInput{Input: input}); err != nil {
		return "", err
	}
	select {
	case <-time.After(simulatorSettle):
	case <-ctx.Done():
		return "", ctx.Err()
	}
	result, err := bridge.Read(ctx, contracts.SimulatorReadSnapshot{Screenshot: false})
	if err != nil {
		return "", err
	}
	return simulatorReadText(result), nil
}

var SimulatorTools = []Definition[SimulatorBridge]{
	{
		Name: "simulator_open",
		Description: strings.Join([]string{
			"Start an iOS simulator in the AICodingTool Simulator panel and wait until it is running. It runs headless, so the user keeps using their computer while you test.",
			"Answers with the device's UDID: build for it with `xcodebuild -destination 'id=<UDID>'`, then `xcrun simctl install <UDID> <path.app>` and `xcrun simctl launch <UDID> <bundle id>`.",
		}, " "),
		Input: Shape{
			{"device", String().Max(200).Optional().Describe("A device name such as \"iPhone 17 Pro\", or a UDID. Defaults to the device the panel already shows, else one that is running, else the newest iPhone.")},
		},
		Run: func(ctx context.Context, bridge SimulatorBridge, args Args) ToolResult {
			return simulatorReport(func() (string, error) {
				open := contracts.SimulatorWriteSimulatorOpen{}
				if device := args.Str("device"); device != "" {
					open.Device = &device
				}
				if err := bridge.Command(ctx, open); err != nil {
					return "", err
				}
				status, err := bridge.Read(ctx, contracts.SimulatorReadStatus{TimeoutMs: simulatorBootWaitMs})
				if err != nil {
					return "", err
				}
				booted, ok := status.(contracts.SimulatorReadResultStatus)
				if !ok || !booted.Booted {
					return "", errors.New(simulatorReadText(status))
				}
				return fmt.Sprintf("%s UDID: %s. Read the screen with simulator_read.", simulatorReadText(status), booted.Device.UDID), nil
			})
		},
	},
	{
		Name:        "simulator_read",
		Description: "Read the simulator's screen: the app on screen as accessibility elements, each with a ref, its role, label, value and frame in points, plus a screenshot file. Refs are only valid until the next read of the screen.",
		Input: Shape{
			{"screenshot", Boolean().Optional().Describe("Also save a screenshot to look at. Defaults to true.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, bridge SimulatorBridge, args Args) ToolResult {
			return simulatorReport(func() (string, error) {
				screenshot := args.OptBool("screenshot")
				result, err := bridge.Read(ctx, contracts.SimulatorReadSnapshot{Screenshot: screenshot == nil || *screenshot})
				if err != nil {
					return "", err
				}
				return simulatorReadText(result), nil
			})
		},
	},
	{
		Name:        "simulator_tap",
		Description: "Tap the simulator's screen, on an element by the ref the latest simulator_read gave it, or at a point. Answers with the elements on screen afterwards.",
		Input: Shape{
			{"ref", String().Max(64).Optional().Describe("The element's ref from the latest simulator_read.")},
			{"x", simulatorPoint("Horizontal position").Optional()},
			{"y", simulatorPoint("Vertical position").Optional()},
		},
		Run: func(ctx context.Context, bridge SimulatorBridge, args Args) ToolResult {
			return simulatorReport(func() (string, error) {
				if ref := args.Str("ref"); ref != "" {
					return simulatorAct(ctx, bridge, domain.SimulatorInputTapElement{Ref: ref})
				}
				if !args.Has("x") || !args.Has("y") {
					return "", errors.New("Give a ref, or both x and y.")
				}
				points := true
				return simulatorAct(ctx, bridge, domain.SimulatorInputTap{X: args.Num("x"), Y: args.Num("y"), Points: &points})
			})
		},
	},
	{
		Name:        "simulator_swipe",
		Description: "Drag a finger across the simulator's screen from one point to another, to scroll, swipe between pages, or pull down. Answers with the elements on screen afterwards.",
		Input: Shape{
			{"x", simulatorPoint("Starting horizontal position")},
			{"y", simulatorPoint("Starting vertical position")},
			{"toX", simulatorPoint("Ending horizontal position")},
			{"toY", simulatorPoint("Ending vertical position")},
			{"durationMs", Number().Min(50).Max(domain.MaxSwipeMs).Optional().Describe("How long the drag takes. Defaults to 300; shorter flings further.")},
		},
		Run: func(ctx context.Context, bridge SimulatorBridge, args Args) ToolResult {
			return simulatorReport(func() (string, error) {
				points := true
				return simulatorAct(ctx, bridge, domain.SimulatorInputSwipe{
					X: args.Num("x"), Y: args.Num("y"), ToX: args.Num("toX"), ToY: args.Num("toY"), Points: &points, DurationMs: args.OptNum("durationMs"),
				})
			})
		},
	},
	{
		Name:        "simulator_type",
		Description: "Type text into whatever field has the keyboard in the simulator; tap the field first. Characters a US keyboard lacks, such as emoji or accents, are pasted through the device's pasteboard. Answers with the elements on screen afterwards.",
		Input: Shape{
			{"text", String().Min(1).Max(domain.MaxSimulatorText - 1).Describe("The text to type.")},
			{"submit", Boolean().Optional().Describe("Press Return afterwards.")},
		},
		Run: func(ctx context.Context, bridge SimulatorBridge, args Args) ToolResult {
			return simulatorReport(func() (string, error) {
				text := args.Str("text")
				if args.Bool("submit") {
					text += "\n"
				}
				return simulatorAct(ctx, bridge, domain.SimulatorInputType{Text: text})
			})
		},
	},
	{
		Name:        "simulator_button",
		Description: "Press a hardware button on the simulator: home goes to the home screen, lock locks or wakes it. Answers with the elements on screen afterwards.",
		Input:       Shape{{"button", Enum("home", "lock")}},
		Run: func(ctx context.Context, bridge SimulatorBridge, args Args) ToolResult {
			return simulatorReport(func() (string, error) {
				return simulatorAct(ctx, bridge, domain.SimulatorInputButton{Button: domain.SimulatorButton(args.Str("button"))})
			})
		},
	},
	{
		Name:        "simulator_rotate",
		Description: "Turn the simulator to portrait or landscape, to check how the app lays out each way. Answers with the elements on screen afterwards, measured the way the app now draws them.",
		Input:       Shape{{"orientation", Enum(domain.SimulatorOrientations...).Describe("How to hold the device. landscape-left has the top of the screen on the left.")}},
		Run: func(ctx context.Context, bridge SimulatorBridge, args Args) ToolResult {
			return simulatorReport(func() (string, error) {
				return simulatorAct(ctx, bridge, domain.SimulatorInputRotate{Orientation: domain.SimulatorOrientation(args.Str("orientation"))})
			})
		},
	},
}

// SimulatorToolsFor binds the simulator tools; readOnly keeps only the read, for a run that may look
// at the device but never drive it.
func SimulatorToolsFor(bridge SimulatorBridge, readOnly bool) []BoundTool {
	if readOnly {
		return Bind(bridge, filterDefinitions(SimulatorTools, func(tool Definition[SimulatorBridge]) bool { return tool.ReadOnly }))
	}
	return Bind(bridge, SimulatorTools)
}

const simulatorInstructions = `To check an iOS app on a simulator, use the AICodingTool simulator tools: simulator_open starts a headless simulator in the Simulator panel and gives its UDID to build, install and launch on with xcodebuild and xcrun simctl; then simulator_read, simulator_tap, simulator_swipe, simulator_type and simulator_rotate drive it. Never open Simulator.app or drive it with computer use: the panel leaves the user's screen, mouse and focus alone.`
const simulatorReadingInstructions = `Read the iOS simulator in the AICodingTool Simulator panel with simulator_read rather than guessing at it. Starting and driving it are work for a thread.`

var SimulatorServer = Server{
	Name:        SimulatorServerName,
	Definitions: infos(SimulatorTools),
	// Reading the simulator's screen changes nothing; booting it and tapping it do.
	Grants: GrantsReadOnly,
	// A coordinator reads the simulator's screen and never taps it.
	Serve: func(run *Run) ([]BoundTool, bool) {
		if run.Simulator == nil {
			return nil, false
		}
		return SimulatorToolsFor(run.Simulator, run.role() == domain.CoordinationRoleCoordinator), true
	},
	Brief: func(offers Offers, _ BriefDialect, _ BriefRun) []string {
		if !offers("") {
			return nil
		}
		if offers("simulator_open") {
			return []string{simulatorInstructions}
		}
		return []string{simulatorReadingInstructions}
	},
}
