package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"slices"
)

var DesktopInputs = []string{
	"image.open", "image.step", "image.close", "image.download", "file.open", "app.list", "apps.listed", "app.open-folder",
	"app.open-source-licenses", "view.closed", "view.mounted",
}

// desktopChange is the TypeScript class behind DesktopEdit: the desktop around the window as one input leaves it.
type desktopChange = DesktopEdit

func DesktopOf(state *WorkspaceState) DesktopEdit {
	return DesktopEdit{View: OwnFields[DesktopState](state)}
}

// FocusedIn is whether the window is the one in front.
func FocusedIn(edit DesktopEdit, focused bool) DesktopEdit {
	edit.View.Focused = focused
	return edit
}

func ReduceDesktop(state *WorkspaceState, input WorkspaceInput) SliceTransition {
	switch input := input.(type) {
	case contracts.ImageViewCommandImageOpen:
		source := input.Source
		gallery := []string{}
		if slices.Contains(input.Gallery, source) {
			gallery = input.Gallery
		}
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.ViewingImage = &source
			next.ViewingGallery = gallery
		}}
	case contracts.ImageViewCommandImageStep:
		if state.ViewingImage == nil {
			return SliceTransition{}
		}
		at := slices.Index(state.ViewingGallery, *state.ViewingImage) + int(input.Delta)
		if float64(int(input.Delta)) != input.Delta || at < 0 || at >= len(state.ViewingGallery) || state.ViewingGallery[at] == "" {
			return SliceTransition{}
		}
		image := state.ViewingGallery[at]
		return SliceTransition{Patch: func(next *WorkspaceState) { next.ViewingImage = &image }}
	case contracts.ImageViewCommandImageClose:
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.ViewingImage = nil
			next.ViewingGallery = []string{}
		}}
	case contracts.ImageViewCommandImageDownload:
		effects := []WorkspaceEffect{}
		if state.ViewingImage != nil && *state.ViewingImage != "" {
			effects = append(effects, WorkspaceEffectImageDownload{Source: *state.ViewingImage})
		}
		return SliceTransition{ClearsError: true, Effects: effects}
	case contracts.ViewCommandViewMounted:
		toasts := ReleasedToasts(ToastsOf(state))
		effects := []WorkspaceEffect{
			WorkspaceEffectApplyShortcuts{Overrides: state.Shortcuts},
			WorkspaceEffectApplyCaptureOptions{Options: domain.CaptureOptions{Sound: state.CaptureSound, Focus: state.CaptureFocus}},
		}
		effects = append(effects, ShownPageEffects(state)...)
		effects = append(effects, ToastEffects(RescheduledToasts(toasts.View.Toasts, toasts.View.ToastsHeld, Now()))...)
		return SliceTransition{Edits: []OwnerEdit{toasts}, Effects: effects}
	case WorkspaceEventViewClosed:
		return SliceTransition{Patch: func(next *WorkspaceState) {
			next.Focused = false
			next.ViewingImage = nil
			next.ViewingGallery = []string{}
		}, Edits: []OwnerEdit{ClosedWindowDocks(DocksOf(state))}}
	case contracts.FileCommandFileOpen:
		taskID := input.TaskID
		if taskID == nil {
			taskID = state.CurrentID
		}
		roots := ThreadFileRoots(state, findThread(state.Threads, taskID))
		if len(roots) == 0 && !domain.IsAbsoluteFilePath(input.Path) {
			refusal := FileFolderError
			return SliceTransition{Refusal: &refusal}
		}
		return SliceTransition{ClearsError: true, Effects: []WorkspaceEffect{WorkspaceEffectFileOpen{Roots: roots, Path: input.Path, Line: input.Line}}}
	case contracts.ExternalAppCommandAppList:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAppList{}}}
	case DesktopEvent:
		apps := input.Apps
		return SliceTransition{Patch: func(next *WorkspaceState) { next.InstalledApps = apps }}
	case contracts.ExternalAppCommandAppOpenFolder:
		root := CurrentFolder(state)
		if root == nil || *root == "" {
			refusal := AppFolderError
			return SliceTransition{Refusal: &refusal}
		}
		return SliceTransition{ClearsError: true, ClosesMenu: true, Effects: []WorkspaceEffect{WorkspaceEffectAppOpenFolder{Root: *root, AppID: input.AppID}}}
	case contracts.ExternalAppCommandAppOpenSourceLicenses:
		return SliceTransition{Effects: []WorkspaceEffect{WorkspaceEffectAppOpenSourceLicenses{}}}
	}
	return SliceTransition{}
}
