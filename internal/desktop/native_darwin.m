//go:build darwin

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#import <objc/runtime.h>
#include "native_darwin.h"

extern void actWindowActive(int active);
extern void actWindowChanged(int kind);
extern void actWindowCloseRequested(void);
extern void actReopen(void);
extern void actDockMarkAllRead(void);
extern void actTransparencyChanged(int reduced);

static BOOL dockHasUnread = NO;
static void placeTrafficLights(NSWindow *window);
static NSString *const VisualFramePath = @"/__app/visual-frame";

static NSWindow *mainWindow(void) {
  for (NSWindow *window in [NSApp windows]) {
    if ([NSStringFromClass([window class]) isEqualToString:@"WailsWindow"]) return window;
  }
  return nil;
}

static WKWebView *findWebView(NSView *view) {
  if ([view isKindOfClass:[WKWebView class]]) return (WKWebView *)view;
  for (NSView *child in [view subviews]) {
    WKWebView *found = findWebView(child);
    if (found) return found;
  }
  return nil;
}

static NSVisualEffectView *findEffectView(NSView *view) {
  for (NSView *child in [view subviews]) {
    if ([child isKindOfClass:[NSVisualEffectView class]]) return (NSVisualEffectView *)child;
  }
  return nil;
}

/* Global coordinates put the origin at the top left of the main display, as Electron reports them. */
static CGFloat mainDisplayHeight(void) {
  NSArray<NSScreen *> *screens = [NSScreen screens];
  return screens.count ? NSMaxY([screens[0] frame]) : 0;
}

static NSRect toCocoa(double x, double y, double width, double height) {
  return NSMakeRect(x, mainDisplayHeight() - y - height, width, height);
}

static void fromCocoa(NSRect rect, double *out) {
  out[0] = rect.origin.x;
  out[1] = mainDisplayHeight() - NSMaxY(rect);
  out[2] = rect.size.width;
  out[3] = rect.size.height;
}

static void activateApp(void) {
  if (@available(macOS 14.0, *)) [NSApp activate];
  else [NSApp activateIgnoringOtherApps:YES];
}

static void runOnMain(dispatch_block_t block) {
  if ([NSThread isMainThread]) block();
  else dispatch_sync(dispatch_get_main_queue(), block);
}

int actScreens(double *out, int capacity) {
  __block int count = 0;
  runOnMain(^{
    for (NSScreen *screen in [NSScreen screens]) {
      if (count >= capacity) break;
      fromCocoa([screen visibleFrame], out + count * 4);
      count++;
    }
  });
  return count;
}

int actWindowState(double *frame, int *flags) {
  __block int found = 0;
  runOnMain(^{
    NSWindow *window = mainWindow();
    if (!window) return;
    found = 1;
    BOOL fullScreen = ([window styleMask] & NSWindowStyleMaskFullScreen) != 0;
    fromCocoa([window frame], frame);
    flags[0] = [window isZoomed] && !fullScreen;
    flags[1] = fullScreen;
    flags[2] = [window isVisible];
    flags[3] = [window isMiniaturized];
    flags[4] = [window isKeyWindow];
  });
  return found;
}

void actPlaceAndShow(double x, double y, double width, double height, int hasPosition, int maximized, int fullScreen) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSWindow *window = mainWindow();
    if (!window) return;
    NSRect frame = [window frame];
    frame.size = NSMakeSize(width, height);
    if (hasPosition) frame = toCocoa(x, y, width, height);
    [window setFrame:frame display:NO];
    if (!hasPosition) [window center];
    placeTrafficLights(window);
    [window makeKeyAndOrderFront:nil];
    activateApp();
    placeTrafficLights(window);
    if (fullScreen) [window toggleFullScreen:nil];
    else if (maximized && ![window isZoomed]) [window zoom:nil];
  });
}

void actReveal(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSWindow *window = mainWindow();
    if (!window) return;
    if ([window isMiniaturized]) [window deminiaturize:nil];
    [window makeKeyAndOrderFront:nil];
    activateApp();
  });
}

void actToggleFullScreen(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [mainWindow() toggleFullScreen:nil]; });
}

void actMinimize(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [mainWindow() performMiniaturize:nil]; });
}

void actZoomWindow(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [mainWindow() performZoom:nil]; });
}

void actBringAllToFront(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [NSApp arrangeInFront:nil]; });
}

void actAbout(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    activateApp();
    [NSApp orderFrontStandardAboutPanel:nil];
  });
}

void actHideOthers(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [NSApp hideOtherApplications:nil]; });
}

void actShowAll(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [NSApp unhideAllApplications:nil]; });
}

void actHideApp(void) {
  dispatch_async(dispatch_get_main_queue(), ^{ [NSApp hide:nil]; });
}

void actSetBackground(int r, int g, int b, int a) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSWindow *window = mainWindow();
    if (!window) return;
    [window setBackgroundColor:[NSColor colorWithSRGBRed:r / 255.0 green:g / 255.0 blue:b / 255.0 alpha:a / 255.0]];
  });
}

void actSetAppearance(int mode) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSAppearance *appearance = nil;
    if (mode == 1) {
      if (@available(macOS 10.14, *)) appearance = [NSAppearance appearanceNamed:NSAppearanceNameDarkAqua];
    }
    if (mode == 2) appearance = [NSAppearance appearanceNamed:NSAppearanceNameAqua];
    [NSApp setAppearance:appearance];
  });
}

/* Electron's page zoom: each step is 1.2 to the half power, and 0 is the page's own size. */
static double zoomLevel = 0;

void actZoomPage(int step) {
  dispatch_async(dispatch_get_main_queue(), ^{
    WKWebView *webView = findWebView([mainWindow() contentView]);
    if (!webView) return;
    zoomLevel = step == 0 ? 0 : fmax(-8, fmin(9, zoomLevel + step * 0.5));
    if (@available(macOS 11.0, *)) [webView setPageZoom:pow(1.2, zoomLevel)];
  });
}

@interface _WKInspector : NSObject
- (void)show;
- (void)close;
- (BOOL)isVisible;
@end

@interface WKWebView ()
- (_WKInspector *)_inspector;
@end

void actToggleInspector(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    WKWebView *webView = findWebView([mainWindow() contentView]);
    if (!webView) return;
    @try {
      [webView.configuration.preferences setValue:@YES forKey:@"developerExtrasEnabled"];
      _WKInspector *inspector = [webView _inspector];
      if ([inspector isVisible]) [inspector close];
      else [inspector show];
    } @catch (NSException *exception) {
      NSLog(@"Could not toggle the inspector: %@", exception.reason);
    }
  });
}

/* The sidebar material Electron's `vibrancy: "sidebar"` uses, following the window's active state. */
static void applyMaterial(NSWindow *window) {
  NSVisualEffectView *effect = findEffectView([window contentView]);
  if (!effect) return;
  [effect setMaterial:NSVisualEffectMaterialSidebar];
  [effect setState:NSVisualEffectStateFollowsWindowActiveState];
}

@interface ActWindowObserver : NSObject
@end

@implementation ActWindowObserver
- (void)becameKey:(NSNotification *)note {
  placeTrafficLights([note object]);
  actWindowActive(1);
}
- (void)resignedKey:(NSNotification *)note {
  placeTrafficLights([note object]);
  actWindowActive(0);
}
- (void)changed:(NSNotification *)note {
  placeTrafficLights([note object]);
  actWindowChanged(0);
}
- (void)relayout:(NSNotification *)note { placeTrafficLights([note object]); }
- (void)visibility:(NSNotification *)note { actWindowChanged(1); }
@end

static ActWindowObserver *observer = nil;

static BOOL reopenApp(id self, SEL _cmd, NSApplication *sender, BOOL hasVisibleWindows) {
  actReopen();
  return NO;
}

static void markAllRead(id self, SEL _cmd, id sender) { actDockMarkAllRead(); }

static NSMenu *dockMenu(id self, SEL _cmd, NSApplication *sender) {
  NSMenu *menu = [[[NSMenu alloc] initWithTitle:@""] autorelease];
  [menu setAutoenablesItems:NO];
  NSMenuItem *item = [[[NSMenuItem alloc] initWithTitle:@"Mark All as Read" action:@selector(actMarkAllRead:) keyEquivalent:@""] autorelease];
  [item setTarget:self];
  [item setEnabled:dockHasUnread];
  [menu addItem:item];
  return menu;
}

void actSetDockUnread(int unread) { dockHasUnread = unread != 0; }

/* A sandboxed frame may still navigate itself, which is how a visual would reach the network. The
   window has no other frames, so any frame leaving the visual page is stopped. */
static void decideNavigation(id self, SEL _cmd, WKWebView *webView, WKNavigationAction *action, void (^decision)(WKNavigationActionPolicy)) {
  WKFrameInfo *target = action.targetFrame;
  if (target && !target.isMainFrame && ![action.request.URL.path isEqualToString:VisualFramePath]) {
    decision(WKNavigationActionPolicyCancel);
    return;
  }
  decision(WKNavigationActionPolicyAllow);
}

static IMP originalShouldClose = NULL;

static BOOL shouldClose(id self, SEL _cmd, NSWindow *sender) {
  actWindowCloseRequested();
  return ((BOOL(*)(id, SEL, NSWindow *))originalShouldClose)(self, _cmd, sender);
}

void actInstallWindowHooks(int material) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSWindow *window = mainWindow();
    if (!window) return;
    /* A window over a material is created clear, so the material samples what lies behind it. */
    if (material != 0) {
      [window setOpaque:NO];
      [window setBackgroundColor:[NSColor clearColor]];
    }
    if (material != 0) applyMaterial(window);
    placeTrafficLights(window);

    Class delegateClass = [[NSApp delegate] class];
    class_addMethod(delegateClass, @selector(applicationShouldHandleReopen:hasVisibleWindows:), (IMP)reopenApp, "c@:@c");
    class_addMethod(delegateClass, @selector(applicationDockMenu:), (IMP)dockMenu, "@@:@");
    class_addMethod(delegateClass, @selector(actMarkAllRead:), (IMP)markAllRead, "v@:@");

    WKWebView *webView = findWebView([window contentView]);
    id navigationDelegate = webView.navigationDelegate;
    if (navigationDelegate) {
      class_addMethod([navigationDelegate class], @selector(webView:decidePolicyForNavigationAction:decisionHandler:), (IMP)decideNavigation, "v@:@@@?");
    }

    id windowDelegate = [window delegate];
    Method close = windowDelegate ? class_getInstanceMethod([windowDelegate class], @selector(windowShouldClose:)) : NULL;
    if (close && !originalShouldClose) originalShouldClose = method_setImplementation(close, (IMP)shouldClose);

    observer = [ActWindowObserver new];
    NSNotificationCenter *center = [NSNotificationCenter defaultCenter];
    [center addObserver:observer selector:@selector(becameKey:) name:NSWindowDidBecomeKeyNotification object:window];
    [center addObserver:observer selector:@selector(resignedKey:) name:NSWindowDidResignKeyNotification object:window];
    for (NSNotificationName name in @[NSWindowDidMoveNotification, NSWindowDidResizeNotification, NSWindowDidEnterFullScreenNotification, NSWindowDidExitFullScreenNotification]) {
      [center addObserver:observer selector:@selector(changed:) name:name object:window];
    }
    for (NSNotificationName name in @[NSWindowDidEndLiveResizeNotification, NSWindowDidUpdateNotification]) {
      [center addObserver:observer selector:@selector(relayout:) name:name object:window];
    }
    for (NSNotificationName name in @[NSWindowDidMiniaturizeNotification, NSWindowDidDeminiaturizeNotification, NSWindowDidChangeOcclusionStateNotification]) {
      [center addObserver:observer selector:@selector(visibility:) name:name object:window];
    }
    actWindowActive([window isKeyWindow] ? 1 : 0);
  });
}

void actSetBadge(int count) {
  dispatch_async(dispatch_get_main_queue(), ^{
    [[NSApp dockTile] setBadgeLabel:count > 0 ? [NSString stringWithFormat:@"%d", count] : nil];
  });
}

/* What the system does when a title bar is double-clicked, as the person set it in System Settings. */
void actTitlebarDoubleClick(void) {
  dispatch_async(dispatch_get_main_queue(), ^{
    NSWindow *window = mainWindow();
    if (!window) return;
    NSString *action = [[NSUserDefaults standardUserDefaults] stringForKey:@"AppleActionOnDoubleClick"];
    if ([action isEqualToString:@"Minimize"]) [window performMiniaturize:nil];
    else if (![action isEqualToString:@"None"]) [window performZoom:nil];
  });
}

/* Returns the installed font families, one per line; the caller frees the result. */
char *actFontFamilies(void) {
  __block char *result = NULL;
  runOnMain(^{
    NSArray<NSString *> *families = [[NSFontManager sharedFontManager] availableFontFamilies];
    result = strdup([[families componentsJoinedByString:@"\n"] UTF8String]);
  });
  return result;
}

/* Where AppKit has Liquid Glass, the page is laid out as one glass sheet. Electron's window shows
   its vibrancy over the glass it attaches, so the sidebar material is what draws behind the page. */
int actGlassAvailable(void) { return NSClassFromString(@"NSGlassEffectView") != nil; }

/* How tinted the person has set the system's glass, from 0 for the clearest to 1. */
double actGlassTint(void) {
  double amount = [[NSUserDefaults standardUserDefaults] doubleForKey:@"NSGlassTintAmount"];
  if (isnan(amount)) return 0;
  return fmin(1, fmax(0, amount));
}

/* Where Electron's inset title bar puts the close button, from the window's top left, in points. */
static const CGFloat TrafficLightX = 12;
static const CGFloat TrafficLightTop = 11;

static void placeTrafficLights(NSWindow *window) {
  if (!window || ([window styleMask] & NSWindowStyleMaskFullScreen)) return;
  NSButton *close = [window standardWindowButton:NSWindowCloseButton];
  NSView *container = [close superview];
  if (!close || !container) return;
  CGFloat dx = TrafficLightX - close.frame.origin.x;
  CGFloat top = container.isFlipped ? TrafficLightTop : container.frame.size.height - TrafficLightTop - close.frame.size.height;
  CGFloat dy = top - close.frame.origin.y;
  if (dx == 0 && dy == 0) return;
  for (NSNumber *kind in @[@(NSWindowCloseButton), @(NSWindowMiniaturizeButton), @(NSWindowZoomButton)]) {
    NSButton *button = [window standardWindowButton:(NSWindowButton)kind.integerValue];
    [button setFrameOrigin:NSMakePoint(button.frame.origin.x + dx, button.frame.origin.y + dy)];
  }
}

int actReducedTransparency(void) {
  return [[NSWorkspace sharedWorkspace] accessibilityDisplayShouldReduceTransparency] ? 1 : 0;
}

void actWatchTransparency(void) {
  [[[NSWorkspace sharedWorkspace] notificationCenter] addObserverForName:NSWorkspaceAccessibilityDisplayOptionsDidChangeNotification object:nil queue:nil usingBlock:^(NSNotification *note) {
    actTransparencyChanged(actReducedTransparency());
  }];
}
