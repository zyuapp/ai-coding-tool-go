package contracts

import (
	"regexp"
	"slices"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

const ComputerProtocolVersion = 2

// MaxComputerMessageBytes is a complete image strip, plus room for the prompt, annotations, and wire envelope:
// domain.MaxAttachments * domain.MaxAttachmentEncodedBytes + 2 MiB.
const MaxComputerMessageBytes = 6*34_952_536 + 2*1024*1024

// ComputerTransferTimeoutMs: transfers share a socket with heartbeats, which wait behind large frames.
const ComputerTransferTimeoutMs = 5 * 60_000

const ComputerSendTooLarge = "This message is too large to send to another computer. Use smaller images or send fewer attachments."

const (
	computersMaxIDLength         = 256
	computersMaxTokenLength      = 512
	computersMaxDeviceNameLength = 128
	computersMaxPathLength       = 4_096
	// How many inputs one request may carry. A send is a few; a stranger's list is refused.
	maxInputs = 16
)

// computersIsString: maxLength defaults to computersMaxIDLength.
func computersIsString(value any, maxLength ...int) bool {
	limit := computersMaxIDLength
	if len(maxLength) > 0 {
		limit = maxLength[0]
	}
	return IpcIsString(value, limit)
}

func computersIsCount(value any) bool {
	return ipcInteger(value) && ipcNumber(value) >= 0
}

func computersIsRecord(value any) (map[string]any, bool) {
	m, ok := value.(map[string]any)
	return m, ok && m != nil
}

// computerQueryField checks one field of a query; an absent field is passed as ipcUndefined.
type computerQueryField struct {
	name  string
	check func(value any) bool
}

// computerQueryShape is one query kind's fields, in declaration order.
type computerQueryShape struct {
	kind   string
	fields []computerQueryField
}

func computersOptional(check func(value any) bool) func(value any) bool {
	return func(value any) bool { return value == ipcUndefined || check(value) }
}

func computersIsBoolean(value any) bool {
	_, ok := value.(bool)
	return ok
}

func computersIsText(value any) bool {
	_, ok := value.(string)
	return ok
}

func computersIsID(value any) bool { return computersIsString(value) }

func computersIsPath(value any) bool { return computersIsString(value, computersMaxPathLength) }

func computersIsLimit(value any) bool { return computersIsCount(value) && ipcNumber(value) <= 200 }

var computersAttachmentName = regexp.MustCompile(`^[A-Za-z0-9-]+\.png$`)

// queryShapes holds every query and field with one validator, also used to advertise what this host understands.
var queryShapes = []computerQueryShape{
	{"thread-read", []computerQueryField{{"threadId", computersIsID}, {"limit", computersOptional(computersIsLimit)}}},
	{"thread-list", []computerQueryField{
		{"project", computersOptional(computersIsPath)},
		{"archived", computersOptional(computersIsBoolean)},
		{"idleForMs", computersOptional(computersIsCount)},
		{"search", computersOptional(func(value any) bool { return computersIsString(value, 1_000) })},
		{"attachments", computersOptional(computersIsBoolean)},
		{"limit", computersOptional(computersIsLimit)},
	}},
	{"terminal-output", []computerQueryField{
		{"terminalId", computersIsID},
		{"after", computersOptional(func(value any) bool { return ipcSafeInteger(value) && ipcNumber(value) >= 0 })},
	}},
	{"directories", []computerQueryField{{"prefix", func(value any) bool {
		prefix, ok := value.(string)
		return ok && ipcLength(prefix) <= computersMaxPathLength && !strings.Contains(prefix, "\x00")
	}}}},
	{"attachment", []computerQueryField{{"name", func(value any) bool {
		return computersIsString(value) && computersAttachmentName.MatchString(value.(string))
	}}}},
	{"message-image", []computerQueryField{
		{"path", computersIsText},
		{"root", computersIsText},
		{"message", computersIsText},
		{"thumbnail", computersOptional(computersIsBoolean)},
	}},
	{"diff-patch", []computerQueryField{
		{"workspaceId", computersIsID},
		{"range", domain.IsDiffRange},
		{"path", computersIsPath},
		{"previousPath", computersOptional(computersIsPath)},
		{"ignoreWhitespace", computersOptional(computersIsBoolean)},
	}},
	{"branches", []computerQueryField{{"workspaceId", computersIsID}}},
	{"commits", []computerQueryField{{"workspaceId", computersIsID}, {"hashes", domain.IsCommitHashes}}},
	{"commands", []computerQueryField{{"workspaceId", computersIsID}, {"engine", domain.IsAgentEngine}}},
}

// ComputerQueryDefinition is one query kind and the fields it takes, as computerQueryDefinitions lists them.
type ComputerQueryDefinition struct {
	Kind   string
	Fields []string
}

func ComputerQueryDefinitions() []ComputerQueryDefinition {
	definitions := make([]ComputerQueryDefinition, len(queryShapes))
	for i, shape := range queryShapes {
		fields := make([]string, len(shape.fields))
		for j, field := range shape.fields {
			fields[j] = field.name
		}
		definitions[i] = ComputerQueryDefinition{Kind: shape.kind, Fields: fields}
	}
	return definitions
}

func IsComputerQuery(value any) bool {
	query, ok := computersIsRecord(value)
	if !ok {
		return false
	}
	kind, _ := query["kind"].(string)
	at := slices.IndexFunc(queryShapes, func(shape computerQueryShape) bool { return shape.kind == kind })
	if at < 0 {
		return false
	}
	for _, field := range queryShapes[at].fields {
		value, present := query[field.name]
		if !present {
			value = ipcUndefined
		}
		if !field.check(value) {
			return false
		}
	}
	return kind != "message-image" || domain.IsMessageImageReference(query["path"], query["root"], query["message"])
}

// IsComputerClientRequest reads another computer's message defensively: it is the security boundary.
// Its inputs are only ever commands a window could send.
func IsComputerClientRequest(value any) bool {
	request, ok := computersIsRecord(value)
	if !ok {
		return false
	}
	switch request["kind"] {
	case "pair":
		return computersIsCount(request["version"]) && computersIsString(request["code"], computersMaxTokenLength) && computersIsString(request["deviceName"], computersMaxDeviceNameLength)
	case "resume":
		return computersIsCount(request["version"]) && computersIsString(request["token"], computersMaxTokenLength) &&
			(!ipcHas(request, "sessionId") || computersIsString(request["sessionId"])) && computersIsCount(request["lastSequence"])
	case "transfer":
		return computersIsString(request["requestId"])
	case "input":
		inputs, ok := request["inputs"].([]any)
		return computersIsString(request["requestId"]) && ok && len(inputs) > 0 && len(inputs) <= maxInputs &&
			!slices.ContainsFunc(inputs, func(input any) bool {
				if !IsWorkspaceViewInput(input) {
					return true
				}
				record, _ := computersIsRecord(input)
				typ, _ := record["type"].(string)
				return !IsAppCommandType(typ)
			})
	case "query":
		return computersIsString(request["requestId"]) && IsComputerQuery(request["query"])
	case "pong":
		return computersIsCount(request["at"])
	}
	return false
}

// IsComputerServerMessage checks only the envelope: the state inside is the other computer's own derivation.
func IsComputerServerMessage(value any) bool {
	message, ok := computersIsRecord(value)
	if !ok || !computersIsCount(message["sequence"]) {
		return false
	}
	switch message["kind"] {
	case "paired":
		return computersIsString(message["deviceId"]) && computersIsString(message["deviceName"], computersMaxDeviceNameLength) && computersIsString(message["token"], computersMaxTokenLength)
	case "workspace":
		update, ok := computersIsRecord(message["update"])
		_, state := computersIsRecord(update["state"])
		_, patches := update["patches"].([]any)
		return (!ipcHas(message, "sessionId") || computersIsString(message["sessionId"])) && ok && computersIsCount(update["revision"]) && (state || patches)
	case "result":
		result, ok := computersIsRecord(message["result"])
		return computersIsString(message["requestId"]) && ok && computersIsBoolean(result["ok"]) && computersIsCount(result["revision"])
	case "answer":
		if !computersIsString(message["requestId"]) {
			return false
		}
		return message["ok"] == true && ipcHas(message, "result") || message["ok"] == false && computersIsText(message["message"])
	case "error":
		return computersIsText(message["code"]) && computersIsText(message["message"])
	case "notice":
		notice, ok := computersIsRecord(message["notice"])
		return ok && computersIsString(notice["taskId"]) && computersIsText(notice["title"]) && computersIsText(notice["headline"])
	case "capabilities":
		capabilities, ok := message["capabilities"].([]any)
		return ok && len(capabilities) <= 4096 && !slices.ContainsFunc(capabilities, func(name any) bool { return !IpcIsString(name) }) &&
			(!ipcHas(message, "instance") || IpcIsString(message["instance"]))
	case "name":
		return computersIsString(message["name"], computersMaxDeviceNameLength)
	case "ping":
		return computersIsCount(message["at"])
	}
	return false
}
