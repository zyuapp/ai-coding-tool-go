package domain

import (
	"regexp"
	"unicode/utf8"
)

var NoDictation = DictationState{Model: DictationModelUnsupported}

// DictationTapMs is the longest press on the microphone that counts as a tap.
const DictationTapMs = 250

const MaxDictationSeconds = 120
const MaxDictationAudioBytes = 16_000*2*MaxDictationSeconds + 4_096
const maxDictationBase64Length = (MaxDictationAudioBytes + 2) / 3 * 4

var dictationBase64 = regexp.MustCompile(`^[A-Za-z0-9+/]+={0,2}$`)

func IsDictationAudio(value any) bool {
	audio, ok := value.(string)
	return ok && audio != "" && len(audio) <= maxDictationBase64Length && len(audio)%4 == 0 && dictationBase64.MatchString(audio)
}

const DictationUnsupported = "Dictation needs Linux or a Mac with Apple silicon."
const DictationMissing = "Download the speech model in Settings → General to dictate."
const DictationDownloading = "The speech model is still downloading."
const DictationHeardNothing = "Didn't catch anything."

// DictationRefusal is why a hold cannot start; false when it can. Reads only Model and Message.
func DictationRefusal(state *DictationState) (string, bool) {
	switch state.Model {
	case DictationModelReady:
		return "", false
	case DictationModelMissing:
		return DictationMissing, true
	case DictationModelDownloading:
		return DictationDownloading, true
	case DictationModelFailed:
		if state.Message != nil {
			return *state.Message, true
		}
		return DictationMissing, true
	case DictationModelUnsupported:
		if state.Message != nil {
			return *state.Message, true
		}
		return DictationUnsupported, true
	}
	return "", false
}

func WithSpokenWords(typed, spoken string) string {
	words := jsTrim(spoken)
	if words == "" {
		return typed
	}
	if jsTrim(typed) == "" {
		return words
	}
	if last, _ := utf8.DecodeLastRuneInString(typed); isJSSpace(last) {
		return typed + words
	}
	return typed + " " + words
}
