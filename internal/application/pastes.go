package application

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// A paste past either of these fills the composer rather than reading as a sentence, so it rides as a pill.
const (
	PastePillLines = 12
	PastePillChars = 900
)

func PasteRidesAsPill(text string) bool {
	return threadsTrim(text) != "" && (LineCount(text) > PastePillLines || threadsUTF16Len(text) > PastePillChars)
}

func LineCount(text string) int {
	return strings.Count(text, "\n") + 1
}

// What a pill says it holds: lines for anything with a shape, characters for one long run of text.
func PasteSummary(text string) string {
	if lines := LineCount(text); lines > 1 {
		return threadsGrouped(lines) + " lines"
	}
	return threadsGrouped(threadsUTF16Len(text)) + " characters"
}

// threadsGrouped is a count as en-US toLocaleString writes it.
func threadsGrouped(count int) string {
	digits := strconv.Itoa(count)
	var grouped strings.Builder
	for index, digit := range digits {
		if index > 0 && (len(digits)-index)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	return grouped.String()
}

// What a thread pasted into with nothing typed is called: the first line that says something.
func PasteTitle(pastes []*domain.PastedText) string {
	if len(pastes) == 0 || pastes[0].Text == "" {
		return ""
	}
	for line := range strings.SplitSeq(pastes[0].Text, "\n") {
		if line := threadsTrim(line); line != "" {
			return line
		}
	}
	return ""
}

const pasteHeading = "Text I pasted into my message, in the order I pasted it:"

// How pasted blocks reach the agent. The stored message keeps them apart; only the prompt is flat.
func PromptWithPastes(text string, pastes []*domain.PastedText) string {
	if len(pastes) == 0 {
		return text
	}
	blocks := make([]string, len(pastes))
	for index, paste := range pastes {
		blocks[index] = fmt.Sprintf("Pasted text #%d:\n%s", index+1, paste.Text)
	}
	return threadsJoinNonEmpty([]string{text, pasteHeading + "\n\n" + strings.Join(blocks, "\n\n")}, "\n\n")
}
