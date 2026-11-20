package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialDictationState() DictationSliceState {
	return DictationSliceState{Dictation: domain.NoDictation}
}
