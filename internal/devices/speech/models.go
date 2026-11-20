package speech

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ModelPlace is where a computer keeps the speech model its dictation helper reads, and which files make it whole.
type ModelPlace struct {
	// Folder is what the helper reads unless it is handed another.
	Folder string
	// Files must all be in the folder for the model to load.
	Files []string
}

// Parakeet TDT's ONNX export, as onnx-asr reads it.
const (
	ParakeetPreprocessor = "nemo128.onnx"
	ParakeetEncoder      = "encoder-model.int8.onnx"
	ParakeetDecoder      = "decoder_joint-model.int8.onnx"
	ParakeetVocabulary   = "vocab.txt"
)

// ParakeetSampleRate is the rate the model hears at.
const ParakeetSampleRate = 16_000

// LinuxModelFiles is Parakeet v2 for ONNX Runtime, its encoder and decoder quantized to int8.
var LinuxModelFiles = []string{ParakeetPreprocessor, ParakeetEncoder, ParakeetDecoder, ParakeetVocabulary}

// macModelFiles is Parakeet v2 for Core ML, in the folder FluidAudio shares between apps.
var macModelFiles = []string{"Preprocessor.mlmodelc", "Encoder.mlmodelc", "Decoder.mlmodelc", "JointDecision.mlmodelc", "parakeet_vocab.json"}

func linuxDataHome() string {
	if named := os.Getenv("XDG_DATA_HOME"); named != "" && filepath.IsAbs(named) {
		return named
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// DictationModelPlace is where this computer's model goes, or nil where no helper runs. goos and
// goarch are Go's names (runtime.GOOS, runtime.GOARCH).
func DictationModelPlace(goos, goarch string) *ModelPlace {
	if goos == "darwin" && goarch == "arm64" {
		home, _ := os.UserHomeDir()
		return &ModelPlace{Folder: filepath.Join(home, "Library", "Application Support", "FluidAudio", "Models", "parakeet-tdt-0.6b-v2-coreml"), Files: macModelFiles}
	}
	if goos == "linux" && (goarch == "amd64" || goarch == "arm64") {
		return &ModelPlace{Folder: filepath.Join(linuxDataHome(), "aicodingtool", "models", "parakeet-tdt-0.6b-v2-int8"), Files: LinuxModelFiles}
	}
	return nil
}

// ParakeetVocabularyPieces reads vocab.txt, where each line is a piece and its id. Ids no line names are "".
func ParakeetVocabularyPieces(text string) []string {
	var pieces []string
	for line := range strings.SplitSeq(text, "\n") {
		at := strings.LastIndex(line, " ")
		if at <= 0 {
			continue
		}
		id, ok := vocabularyID(line[at+1:])
		if !ok {
			continue
		}
		for len(pieces) <= id {
			pieces = append(pieces, "")
		}
		pieces[id] = line[:at]
	}
	return pieces
}

// vocabularyID is Number(text) when it is a non-negative integer.
func vocabularyID(text string) (int, bool) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return 0, true
	}
	value, err := strconv.ParseFloat(trimmed, 64)
	if err != nil || value < 0 || value != float64(int(value)) || value > 1<<31 {
		return 0, false
	}
	return int(value), true
}

const (
	// ParakeetBlank is the token that says nothing.
	ParakeetBlank = "<blk>"
	// parakeetSpace is SentencePiece's word boundary.
	parakeetSpace = "▁"
)

// jsSpaces is JavaScript's \s.
const jsSpaces = "\t\n\v\f\r \u00a0\u1680\u2000\u2001\u2002\u2003\u2004\u2005\u2006\u2007\u2008\u2009\u200a\u2028\u2029\u202f\u205f\u3000\ufeff"

var whitespaceRun = regexp.MustCompile("[" + jsSpaces + "]+")

// ParakeetText joins pieces back into words.
func ParakeetText(pieces []string) string {
	return strings.Trim(whitespaceRun.ReplaceAllString(strings.ReplaceAll(strings.Join(pieces, ""), parakeetSpace, " "), " "), jsSpaces)
}
