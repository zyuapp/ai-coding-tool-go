package computeruse

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreGraphics -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <CoreGraphics/CoreGraphics.h>
#include <stdbool.h>

static bool accessibilityTrusted(void) { return AXIsProcessTrusted(); }

// Lists the app under Accessibility, which shows macOS's own prompt the first time.
static bool promptAccessibility(void) {
	const void *keys[] = { kAXTrustedCheckOptionPrompt };
	const void *values[] = { kCFBooleanTrue };
	CFDictionaryRef options = CFDictionaryCreate(NULL, keys, values, 1, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
	bool trusted = AXIsProcessTrustedWithOptions(options);
	if (options) CFRelease(options);
	return trusted;
}

static bool screenRecordingGranted(void) { return CGPreflightScreenCaptureAccess(); }
static bool requestScreenRecording(void) { return CGRequestScreenCaptureAccess(); }
*/
import "C"

import (
	"log"

	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

const (
	accessibilitySettings   = "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility"
	screenRecordingSettings = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"
)

// needsGrants: a run cannot start until macOS grants both permissions.
const needsGrants = true

func (h *Host) permissions() *domain.ComputerUsePermissions {
	return &domain.ComputerUsePermissions{
		Accessibility:   bool(C.accessibilityTrusted()),
		ScreenRecording: bool(C.screenRecordingGranted()),
	}
}

// request shows macOS's prompt where it still has one; after that the user is sent to the setting.
func (h *Host) request(permission domain.ComputerUsePermission) *domain.ComputerUsePermissions {
	if permission == domain.ComputerUsePermissionAccessibility {
		if !bool(C.promptAccessibility()) {
			openSettings(accessibilitySettings)
		}
		return h.permissions()
	}
	C.requestScreenRecording()
	next := h.permissions()
	if !next.ScreenRecording {
		openSettings(screenRecordingSettings)
	}
	return next
}

func openSettings(url string) {
	if err := platform.OpenExternal(url); err != nil {
		log.Printf("Could not open System Settings: %v", err)
	}
}
