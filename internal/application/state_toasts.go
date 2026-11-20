package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialToastsState() ToastsState {
	return ToastsState{Toasts: []*domain.Toast{}, ToastSequence: 0, ToastsHeld: false, DismissedToasts: []string{}}
}
