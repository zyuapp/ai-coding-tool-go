package devices

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework QuartzCore
#import <AppKit/AppKit.h>
#import <QuartzCore/QuartzCore.h>

// A borderless panel that never takes focus or the pointer, above every window, fading out.
static void actFlashWindow(double x, double y, double width, double height, double flashSeconds, double teardownSeconds) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSArray<NSScreen *> *screens = [NSScreen screens];
    if (screens.count == 0) return;
    // CoreGraphics counts down from the main display's top; AppKit counts up from its bottom.
    CGFloat top = screens[0].frame.size.height;
    NSRect rect = NSMakeRect(round(x), round(top - y - height), round(width), round(height));
    NSPanel *panel = [[NSPanel alloc] initWithContentRect:rect
                                                styleMask:NSWindowStyleMaskBorderless | NSWindowStyleMaskNonactivatingPanel
                                                  backing:NSBackingStoreBuffered
                                                    defer:NO];
    panel.releasedWhenClosed = NO;
    panel.animationBehavior = NSWindowAnimationBehaviorNone;
    panel.opaque = NO;
    panel.hasShadow = NO;
    panel.ignoresMouseEvents = YES;
    panel.backgroundColor = [NSColor whiteColor];
    panel.alphaValue = 0.9;
    // Above the window coming forward, so the flash is not drawn under what it announces.
    panel.level = NSScreenSaverWindowLevel;
    panel.collectionBehavior = NSWindowCollectionBehaviorCanJoinAllSpaces | NSWindowCollectionBehaviorFullScreenAuxiliary | NSWindowCollectionBehaviorStationary;
    [panel orderFrontRegardless];
    [NSAnimationContext runAnimationGroup:^(NSAnimationContext *context) {
      context.duration = flashSeconds;
      context.timingFunction = [CAMediaTimingFunction functionWithName:kCAMediaTimingFunctionEaseOut];
      panel.animator.alphaValue = 0.0;
    } completionHandler:nil];
    dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(teardownSeconds * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{
      [panel orderOut:nil];
    });
  });
}
*/
import "C"

import "github.com/zyuapp/ai-coding-tool-go/internal/platform"

// FlashWindow whitens the window that was just captured, the way a camera says it took the picture.
// Drawn after the capture, so the flash is never in the shot, and only where the app is coming
// forward anyway. Safe from any goroutine; the panel is made on the main thread.
func FlashWindow(frame WindowFrame) {
	if !platform.FlashesCapturedWindows {
		return
	}
	if frame.Width < 1 || frame.Height < 1 {
		return
	}
	C.actFlashWindow(C.double(frame.X), C.double(frame.Y), C.double(frame.Width), C.double(frame.Height), C.double(flashDuration.Seconds()), C.double(flashTeardown.Seconds()))
}
