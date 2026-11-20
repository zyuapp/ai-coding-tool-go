package claude

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/jsonx"
)

const transcriptTailBytes = 2 * 1024 * 1024

var transcriptID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,200}$`)

type transcriptRow struct {
	Type      any `json:"type"`
	AgentID   any `json:"agentId"`
	SessionID any `json:"sessionId"`
	Message   any `json:"message"`
	Effort    any `json:"effort"`
	PerTurn   any `json:"perTurnEffort"`
}

// ClaudeTranscriptMetadata reads a child's model and effort from its transcript. The transcript stamps
// the effort sent on the request; per-turn overrides take precedence.
func ClaudeTranscriptMetadata(text, id, sessionID string) domain.SubagentMetadata {
	lines := strings.Split(text, "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		var row transcriptRow
		if jsonx.Unmarshal([]byte(lines[index]), &row) != nil {
			continue
		}
		if row.Type != "assistant" || row.AgentID != id || row.SessionID != sessionID {
			continue
		}
		message, _ := row.Message.(map[string]any)
		model, _ := message["model"].(string)
		if model == "" || model == syntheticModel {
			continue
		}
		metadata := domain.SubagentMetadata{Model: &model}
		effort := row.PerTurn
		if effort == nil {
			effort = row.Effort
		}
		switch value := effort.(type) {
		case string:
			if value != "" {
				metadata.Effort = &value
			}
		case float64:
			if !math.IsInf(value, 0) && !math.IsNaN(value) {
				text := jsNumber(value)
				metadata.Effort = &text
			}
		}
		return metadata
	}
	return domain.SubagentMetadata{}
}

// jsNumber is String(value) for a finite number.
func jsNumber(value float64) string {
	if value == 0 {
		return "0"
	}
	if abs := math.Abs(value); abs >= 1e21 || abs < 1e-6 {
		mantissa, exponent, _ := strings.Cut(strconv.FormatFloat(value, 'e', -1, 64), "e")
		sign, digits := exponent[:1], strings.TrimLeft(exponent[1:], "0")
		return mantissa + "e" + sign + digits
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

type metadataRead struct {
	done   chan struct{}
	result domain.SubagentMetadata
}

var pendingMetadata = struct {
	sync.Mutex
	reads map[string]*metadataRead
}{reads: map[string]*metadataRead{}}

// claudeConfigDir is where Claude Code keeps its sessions.
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

// readClaudeSubagentMetadata reads only the tail of a saved child, without starting Claude or loading
// its full conversation. Reads of the same child at once share one.
func readClaudeSubagentMetadata(id string, sessionID *string, configDir string) domain.SubagentMetadata {
	if !transcriptID.MatchString(id) || sessionID == nil || !transcriptID.MatchString(*sessionID) {
		return domain.SubagentMetadata{}
	}
	key := configDir + "\x00" + *sessionID + "\x00" + id
	pendingMetadata.Lock()
	if read, ok := pendingMetadata.reads[key]; ok {
		pendingMetadata.Unlock()
		<-read.done
		return read.result
	}
	read := &metadataRead{done: make(chan struct{})}
	pendingMetadata.reads[key] = read
	pendingMetadata.Unlock()

	read.result = readSavedChild(configDir, *sessionID, id)
	pendingMetadata.Lock()
	delete(pendingMetadata.reads, key)
	pendingMetadata.Unlock()
	close(read.done)
	return read.result
}

func readSavedChild(configDir, sessionID, id string) domain.SubagentMetadata {
	projects := filepath.Join(configDir, "projects")
	entries, err := os.ReadDir(projects)
	if err != nil {
		return domain.SubagentMetadata{}
	}
	for _, project := range entries {
		if !project.IsDir() {
			continue
		}
		file, err := os.Open(filepath.Join(projects, project.Name(), sessionID, "subagents", "agent-"+id+".jsonl"))
		if err != nil {
			continue
		}
		metadata, err := readTranscriptTail(file, id, sessionID)
		file.Close()
		if err != nil {
			return domain.SubagentMetadata{}
		}
		return metadata
	}
	return domain.SubagentMetadata{}
}

func readTranscriptTail(file *os.File, id, sessionID string) (domain.SubagentMetadata, error) {
	info, err := file.Stat()
	if err != nil {
		return domain.SubagentMetadata{}, err
	}
	size := info.Size()
	start := max(0, size-transcriptTailBytes)
	buffer := make([]byte, min(size, transcriptTailBytes))
	read, err := file.ReadAt(buffer, start)
	if err != nil && err != io.EOF {
		return domain.SubagentMetadata{}, err
	}
	text := string(buffer[:read])
	if start > 0 {
		text = text[strings.IndexByte(text, '\n')+1:]
	}
	return ClaudeTranscriptMetadata(text, id, sessionID), nil
}
