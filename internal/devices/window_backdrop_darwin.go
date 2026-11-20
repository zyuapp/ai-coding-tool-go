package devices

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit -framework Foundation
#import <AppKit/AppKit.h>

static NSString *const kActGlassIdentifier = @"aic-window-glass";
static NSString *const kActRegionIdentifier = @"aic-glass-region";

// Looked up by name, so this builds against an SDK older than the class.
static Class actGlassClass(void) {
  return NSClassFromString(@"NSGlassEffectView");
}

static int actGlassClassAvailable(void) { return actGlassClass() != nil; }

static void actOnMain(dispatch_block_t work) {
  if ([NSThread isMainThread]) work();
  else dispatch_sync(dispatch_get_main_queue(), work);
}

// The content view of the window `handle` names: an NSWindow, or any view in one.
static NSView *actRootView(void *handle) {
  if (!handle) return nil;
  id object = (__bridge id)handle;
  if ([object isKindOfClass:[NSWindow class]]) return ((NSWindow *)object).contentView;
  if ([object isKindOfClass:[NSView class]]) return ((NSView *)object).window.contentView;
  return nil;
}

// Lays one sheet of glass under the whole window.
static int actGlassAttach(void *handle) {
  Class glass = actGlassClass();
  if (!glass) return 0;
  __block int attached = 0;
  actOnMain(^{
    NSView *root = actRootView(handle);
    if (!root) return;
    for (NSView *view in [root.subviews copy]) {
      if ([view.identifier isEqualToString:kActGlassIdentifier]) [view removeFromSuperview];
      // The blur would sit behind the glass and frost what the glass is meant to show.
      else if ([view isKindOfClass:[NSVisualEffectView class]]) view.hidden = YES;
    }
    NSView *view = [[glass alloc] initWithFrame:root.bounds];
    [view setValue:@0 forKey:@"cornerRadius"];
    view.identifier = kActGlassIdentifier;
    view.autoresizingMask = NSViewWidthSizable | NSViewHeightSizable;
    [root addSubview:view positioned:NSWindowBelow relativeTo:nil];
    attached = 1;
  });
  return attached;
}

// Lays one pane of clear glass per region over the window's sheet and under its page, so what the
// page draws there sits on a pane of its own. Takes a flat list of x, y from the top, width, height
// and corner radius; panes are reused, so a region that moves is not rebuilt.
static int actGlassRegions(void *handle, const double *flat, int count) {
  Class glass = actGlassClass();
  if (!glass) return 0;
  __block int applied = 0;
  actOnMain(^{
    NSView *root = actRootView(handle);
    if (!root) return;
    NSMutableArray<NSView *> *panes = [NSMutableArray array];
    NSView *sheet = nil;
    for (NSView *view in root.subviews) {
      if ([view.identifier isEqualToString:kActRegionIdentifier]) [panes addObject:view];
      else if ([view.identifier isEqualToString:kActGlassIdentifier]) sheet = view;
    }
    NSUInteger wanted = (NSUInteger)(count / 5);
    while (panes.count > wanted) {
      [panes.lastObject removeFromSuperview];
      [panes removeLastObject];
    }
    for (NSUInteger index = 0; index < wanted; index++) {
      const double *values = flat + index * 5;
      CGFloat y = root.isFlipped ? values[1] : root.bounds.size.height - values[1] - values[3];
      NSRect frame = NSMakeRect(values[0], y, values[2], values[3]);
      NSView *pane = index < panes.count ? panes[index] : nil;
      if (!pane) {
        pane = [[glass alloc] initWithFrame:frame];
        [pane setValue:@1 forKey:@"style"];
        pane.identifier = kActRegionIdentifier;
        if (sheet) [root addSubview:pane positioned:NSWindowAbove relativeTo:sheet];
        else [root addSubview:pane positioned:NSWindowBelow relativeTo:nil];
        [panes addObject:pane];
      }
      pane.frame = frame;
      [pane setValue:@(values[4]) forKey:@"cornerRadius"];
    }
    applied = 1;
  });
  return applied;
}
*/
import "C"

import "unsafe"

func loadWindowGlass() *WindowGlass {
	if C.actGlassClassAvailable() == 0 {
		return nil
	}
	return &WindowGlass{
		attach: func(window unsafe.Pointer) bool { return C.actGlassAttach(window) != 0 },
		regions: func(window unsafe.Pointer, flat []float64) bool {
			var first *C.double
			if len(flat) > 0 {
				first = (*C.double)(unsafe.Pointer(&flat[0]))
			}
			return C.actGlassRegions(window, first, C.int(len(flat))) != 0
		},
	}
}
