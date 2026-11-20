package contracts

import (
	"encoding/json/jsontext"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// WorkspaceCommandDefinition is one command the wire advertises, with the fields its validator accepts.
type WorkspaceCommandDefinition struct {
	Type   string   `json:"type"`
	Fields []string `json:"fields"`
}

// IsAppCommandType: whether an input is a command rather than something that happened, which is what may be
// carried to another computer.
func IsAppCommandType(typ string) bool {
	_, ok := declarations[typ]
	return ok
}

// WorkspaceViewInputCommandPlacement answers "" for a type that is not a command.
func WorkspaceViewInputCommandPlacement(typ string) CommandPlacement {
	if declaration, ok := declarations[typ]; ok {
		return declaration.At
	}
	return ""
}

func CommandLeaves(typ string) bool {
	declaration, ok := declarations[typ]
	return ok && declaration.Leaves
}

// WorkspaceCommandDefinitions: the wire advertises the same actions and fields this validator accepts. New
// optional fields become capabilities automatically; a changed meaning needs a new field or command name.
func WorkspaceCommandDefinitions() []WorkspaceCommandDefinition {
	definitions := make([]WorkspaceCommandDefinition, len(commands))
	for i, declaration := range commands {
		fields := make([]string, len(declaration.Fields))
		for j, field := range declaration.Fields {
			fields[j] = field.Name
		}
		definitions[i] = WorkspaceCommandDefinition{Type: declaration.Type, Fields: fields}
	}
	return definitions
}

// IsWorkspaceViewInput: every input field is checked before a visible view can reach the application reducer.
// value is raw JSON (jsontext.Value or []byte), a map[string]jsontext.Value, or any value that encodes as the input.
func IsWorkspaceViewInput(value any) bool {
	record, typ, ok := viewInputRecord(value)
	return ok && workspaceViewInputFields(record, typ)
}

// IsExternalCommand: the command surface open to callers outside the window: the commands that declare an agent
// rule, passing the window's own checks and then that rule. Everything else is the user's alone.
func IsExternalCommand(value any) bool {
	record, typ, ok := viewInputRecord(value)
	if !ok || !workspaceViewInputFields(record, typ) {
		return false
	}
	declaration, ok := declarations[typ]
	return ok && declaration.Agent != nil && declaration.Agent(record)
}

// IsPhoneCommand: the command surface open to a paired phone: the commands that declare a phone rule, passing that rule.
func IsPhoneCommand(value any) bool {
	record, typ, ok := viewInputRecord(value)
	if !ok {
		return false
	}
	declaration, ok := declarations[typ]
	return ok && declaration.Phone != nil && declaration.Phone(record)
}

func viewInputRecord(value any) (map[string]jsontext.Value, string, bool) {
	record, ok := declRecordOf(value)
	if !ok {
		return nil, "", false
	}
	typ, ok := declString(record["type"])
	return record, typ, ok
}

func workspaceViewInputFields(record map[string]jsontext.Value, typ string) bool {
	if declaration, ok := declarations[typ]; ok {
		return HasFields(record, declaration.Fields)
	}
	shape, ok := events[typ]
	return ok && HasFields(record, shape)
}

// commands is every command, joined from the table each feature declares beside its commands.
var commands = joinDeclarations(
	TaskCommands,
	SideChatCommands,
	AnnotationCommands,
	PasteCommands,
	ImageCommands,
	ImageViewCommands,
	FileCommands,
	CliCommands,
	ComputerUseCommands,
	UsageCommands,
	ProjectCommands,
	PullRequestCommands,
	RunControlCommands,
	ReviewCommands,
	WorktreeCommands,
	AutomationCommands,
	BrowserCommands,
	BrowserImportCommands,
	SimulatorCommands,
	DictationCommands,
	DiffCommands,
	ExternalAppCommands,
	TerminalCommands,
	RemoteCommands,
	ComputerCommands,
	EngineCommands,
	ViewCommands,
)

var declarations = indexDeclarations(commands)

var events = map[string]Shape{
	"action.failed":        {{"message", Text}, {"page", Optional(declGuard(domain.IsSettingsSection))}},
	"find.results":         {{"target", viewCommandsFindTarget}, {"results", Object(Shape{{"matches", Number}, {"index", Optional(Number)}, {"counting", OptionalBoolean}})}},
	"shortcut.captured":    {{"binding", NullableText}},
	"shortcut.unavailable": {{"refusal", Object(Shape{{"reason", Literals("unsupported")}, {"binding", Text}, {"message", Text}})}},
}

// joinDeclarations spreads tables as an object spread does: a later type replaces an earlier one in its place.
func joinDeclarations(tables ...Declarations) Declarations {
	var joined Declarations
	at := map[string]int{}
	for _, table := range tables {
		for _, declaration := range table {
			if i, ok := at[declaration.Type]; ok {
				joined[i] = declaration
				continue
			}
			at[declaration.Type] = len(joined)
			joined = append(joined, declaration)
		}
	}
	return joined
}

func indexDeclarations(table Declarations) map[string]*Declaration {
	index := make(map[string]*Declaration, len(table))
	for i := range table {
		index[table[i].Type] = &table[i]
	}
	return index
}
