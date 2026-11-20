package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const TerminalServerName = "aicodingtool-terminal"

// TerminalBridge is the terminal panel, which a run may only read: the shell is the user's, and a
// run has Bash of its own.
type TerminalBridge interface {
	Read(ctx context.Context, read contracts.TerminalRead) (contracts.TerminalReadResult, error)
}

var terminalField = String().Max(contracts.IpcMaxIDLength).Optional().Describe("Which terminal to read. Defaults to the one this thread opened, else the one on screen.")

func terminalSnapshotText(snapshot *domain.TerminalSnapshot) string {
	state := "running"
	if snapshot.Status != domain.TerminalSnapshotStatusRunning {
		if snapshot.Error != nil {
			state = *snapshot.Error
		} else if snapshot.ExitCode == nil {
			state = "exited"
		} else {
			state = fmt.Sprintf("exited (%s)", jsNumber(*snapshot.ExitCode))
		}
	}
	parts := []string{snapshot.Title + " — " + snapshot.Cwd, state}
	if snapshot.Matched != nil {
		parts = append(parts, jsNumber(*snapshot.Matched)+" matching lines")
	}
	if snapshot.Omitted > 0 {
		parts = append(parts, jsNumber(snapshot.Omitted)+" earlier lines not shown")
	}
	body := "(this terminal has printed nothing)"
	if len(snapshot.Lines) > 0 {
		body = strings.Join(snapshot.Lines, "\n")
	}
	return strings.Join([]string{strings.Join(parts, " · "), "", body}, "\n")
}

func terminalReadText(result contracts.TerminalReadResult) string {
	switch result := result.(type) {
	case contracts.TerminalReadResultSnapshot:
		return terminalSnapshotText(&result.Snapshot)
	case contracts.TerminalReadResultTerminals:
		if len(result.Terminals) == 0 {
			return "The terminal panel has no terminal open."
		}
		lines := make([]string, len(result.Terminals))
		for index, terminal := range result.Terminals {
			lines[index] = domain.DescribeTerminal(terminal)
		}
		return strings.Join(lines, "\n")
	}
	return "The terminal panel has no terminal open."
}

func reportAs(prefix string, work func() (string, error)) ToolResult {
	text, err := work()
	if err != nil {
		return errorResult(prefix + errorText(err))
	}
	return textResult(text)
}

func terminalReport(work func() (string, error)) ToolResult {
	return reportAs("Terminal error: ", work)
}

var TerminalTools = []Definition[TerminalBridge]{
	{
		Name:        "terminal_list",
		Description: "List the terminals the AICodingTool terminal panel has open, with the folder each runs in and whether its shell is still alive.",
		Input:       Shape{},
		ReadOnly:    true,
		Run: func(ctx context.Context, bridge TerminalBridge, _ Args) ToolResult {
			return terminalReport(func() (string, error) {
				result, err := bridge.Read(ctx, contracts.TerminalReadTerminals{})
				if err != nil {
					return "", err
				}
				return terminalReadText(result), nil
			})
		},
	},
	{
		Name: "terminal_read",
		Description: strings.Join([]string{
			"Read what a terminal in the AICodingTool terminal panel has printed, as plain text with the escape sequences resolved.",
			"This is the user's own shell, not yours: you can read it but never type into it, so use Bash to run anything yourself.",
			"Prefer `match` over a large `lines` when hunting for an error — it filters before the output is returned.",
		}, " "),
		Input: Shape{
			{"terminalId", terminalField},
			{"lines", Number().Nonnegative().Optional().Describe(fmt.Sprintf("How many of the newest lines to return. Defaults to %d, and never exceeds %d.", domain.DefaultTerminalLines, domain.MaxTerminalLines))},
			{"match", String().Max(contracts.MaxSearchText).Optional().Describe("Keep only lines containing this text, case-insensitively.")},
		},
		ReadOnly: true,
		Run: func(ctx context.Context, bridge TerminalBridge, args Args) ToolResult {
			return terminalReport(func() (string, error) {
				read := contracts.TerminalReadSnapshot{Lines: args.OptNum("lines")}
				if id := args.Str("terminalId"); id != "" {
					read.TerminalID = &id
				}
				if match := args.Str("match"); match != "" {
					read.Match = &match
				}
				result, err := bridge.Read(ctx, read)
				if err != nil {
					return "", err
				}
				return terminalReadText(result), nil
			})
		},
	},
}

func TerminalToolsFor(bridge TerminalBridge) []BoundTool {
	return Bind(bridge, TerminalTools)
}

const terminalInstructions = `Use terminal_list and terminal_read to see what the user's terminals in the AICodingTool terminal panel have printed. Those shells are the user's; run your own commands in your own.`

var TerminalServer = Server{
	Name:        TerminalServerName,
	Definitions: infos(TerminalTools),
	Grants:      GrantsNone,
	Serve: func(run *Run) ([]BoundTool, bool) {
		if run.Terminal == nil {
			return nil, false
		}
		return TerminalToolsFor(run.Terminal), true
	},
	Brief: func(offers Offers, _ BriefDialect, _ BriefRun) []string {
		if offers("") {
			return []string{terminalInstructions}
		}
		return nil
	},
}
