//go:build darwin && cgo

#import <Cocoa/Cocoa.h>
#import <WebKit/WebKit.h>
#include <stdlib.h>
#include <string.h>
#include "native_darwin.h"

extern int actBrowserDecide(uint64_t view, char *url, char *method, int target);
extern void actBrowserStarted(uint64_t view);
extern int actBrowserResponse(uint64_t view, char *url, int status, int mainFrame);
extern void actBrowserCommitted(uint64_t view);
extern void actBrowserFinished(uint64_t view);
extern void actBrowserFailed(uint64_t view, int provisional, char *description, char *message);
extern void actBrowserTerminated(uint64_t view);
extern void actBrowserChanged(uint64_t view, char *key, char *url, char *title, int loading, int back, int forward);
extern void actBrowserMessage(uint64_t view, char *origin, int fromURL, char *body);
extern void actBrowserPressed(uint64_t view);
extern int actBrowserKey(uint64_t view, char *type, char *key, char *code, int meta, int control, int alt, int shift, int repeat);
extern uint64_t actBrowserPopup(uint64_t opener, char *url);
extern void actBrowserPopupClosed(uint64_t view);
extern void actBrowserEvaluated(uint64_t call, char *json, char *error);
extern void actBrowserDrawn(uint64_t call, void *bytes, int length, char *error);
extern void actBrowserFound(uint64_t call, int found);
extern void actBrowserCookieList(uint64_t call, uint64_t snapshot, char *json);
extern void actBrowserDone(uint64_t call, int ok);

/* The panel's one website data store. Every tab opens it, so they share one session. */
static const unsigned char DataStoreID[16] = {'a', 'i', 'c', 'o', 'd', 'i', 'n', 'g', 't', 'o', 'o', 'l', '-', 'b', 'r', 'w'};
static const NSSize ParkedSize = {1200, 800};
static const NSSize PopupSize = {560, 720};

static void runOnMain(dispatch_block_t block) {
  if ([NSThread isMainThread]) block();
  else dispatch_sync(dispatch_get_main_queue(), block);
}

static char *copied(NSString *text) { return strdup(text ? [text UTF8String] : ""); }

@class ACTBrowserPage;

static NSWindow *hostWindow;
static WKWebView *hostView;
static NSWindow *parking;
static NSMutableDictionary<NSNumber *, ACTBrowserPage *> *pages;
static WKWebsiteDataStore *store;
static NSString *userAgent;
static WKUserScript *diagnosticsScript;
static WKUserScript *mouseScript;
static NSString *handlerName;
static id inputMonitor;
static uint64_t cookieSnapshotID;
static NSArray<NSHTTPCookie *> *cookieSnapshot;

static NSString *const ObservedKeys[] = {@"loading", @"title", @"URL", @"canGoBack", @"canGoForward"};

static NSString *urlText(NSURL *url) { return url ? (url.absoluteString ?: @"") : @""; }

/* A page's view and what WebKit tells it. A popup is a page with a window of its own. */
@interface ACTBrowserPage : NSObject <WKNavigationDelegate, WKUIDelegate, WKScriptMessageHandler, NSWindowDelegate>
@property(nonatomic) uint64_t handle;
@property(nonatomic, strong) WKWebView *view;
@property(nonatomic, strong) NSWindow *window;
@property(nonatomic) BOOL observing;
@property(nonatomic) BOOL closed;
@end

static ACTBrowserPage *pageFor(uint64_t handle) { return pages[@(handle)]; }

static void giveKeysBack(void) {
  if (hostWindow && hostView) [hostWindow makeFirstResponder:hostView];
}

static BOOL holdsKeyboard(WKWebView *view) {
  NSWindow *window = view.window;
  if (!window) return NO;
  NSResponder *responder = window.firstResponder;
  return [responder isKindOfClass:[NSView class]] && [(NSView *)responder isDescendantOf:view];
}

static NSString *failureMessage(NSError *error) {
  /* A navigation cut short by another, or refused by the panel itself, is not a failure to show. */
  if ([error.domain isEqualToString:NSURLErrorDomain] && error.code == NSURLErrorCancelled) return nil;
  if ([error.domain isEqualToString:@"WebKitErrorDomain"] && (error.code == 102 || error.code == 204)) return nil;
  NSURL *url = error.userInfo[NSURLErrorFailingURLErrorKey];
  NSString *failing = [url isKindOfClass:[NSURL class]] ? url.absoluteString : nil;
  if (failing.length == 0) return error.localizedDescription;
  return [NSString stringWithFormat:@"%@ (%@)", error.localizedDescription, failing];
}

@implementation ACTBrowserPage

- (void)observe {
  for (int i = 0; i < 5; i++) [self.view addObserver:self forKeyPath:ObservedKeys[i] options:NSKeyValueObservingOptionNew context:NULL];
  self.observing = YES;
}

- (void)detach {
  self.closed = YES;
  if (self.observing) {
    for (int i = 0; i < 5; i++) [self.view removeObserver:self forKeyPath:ObservedKeys[i]];
    self.observing = NO;
  }
  /* A popup shares its opener's content controller, whose handler stays the opener's. */
  if (!self.window) [self.view.configuration.userContentController removeScriptMessageHandlerForName:handlerName];
  self.view.navigationDelegate = nil;
  self.view.UIDelegate = nil;
}

- (void)observeValueForKeyPath:(NSString *)keyPath ofObject:(id)object change:(NSDictionary *)change context:(void *)context {
  if (self.closed) return;
  WKWebView *view = self.view;
  actBrowserChanged(self.handle, (char *)[keyPath UTF8String], (char *)[urlText(view.URL) UTF8String], (char *)[(view.title ?: @"") UTF8String],
                    view.isLoading, view.canGoBack, view.canGoForward);
}

- (void)webView:(WKWebView *)webView decidePolicyForNavigationAction:(WKNavigationAction *)action decisionHandler:(void (^)(WKNavigationActionPolicy))decisionHandler {
  if (self.closed) { decisionHandler(WKNavigationActionPolicyCancel); return; }
  NSString *url = urlText(action.request.URL);
  /* A navigation with no target frame opens a window, which the UI delegate answers. */
  if (!action.targetFrame) { decisionHandler(WKNavigationActionPolicyAllow); return; }
  BOOL main = action.targetFrame.isMainFrame;
  if (main && ([url isEqualToString:@"about:blank"] || [url isEqualToString:@"about:srcdoc"])) {
    decisionHandler(WKNavigationActionPolicyAllow);
    return;
  }
  /* The target is a frameTarget: 0 the page, 1 one of its frames. */
  int allowed = actBrowserDecide(self.handle, (char *)[url UTF8String], (char *)[(action.request.HTTPMethod ?: @"GET") UTF8String], main ? 0 : 1);
  decisionHandler(allowed ? WKNavigationActionPolicyAllow : WKNavigationActionPolicyCancel);
}

- (void)webView:(WKWebView *)webView decidePolicyForNavigationResponse:(WKNavigationResponse *)response decisionHandler:(void (^)(WKNavigationResponsePolicy))decisionHandler {
  int status = [response.response isKindOfClass:[NSHTTPURLResponse class]] ? (int)((NSHTTPURLResponse *)response.response).statusCode : 0;
  BOOL allowed = !self.closed && actBrowserResponse(self.handle, (char *)[urlText(response.response.URL) UTF8String], status, response.isForMainFrame);
  /* The panel shows pages; a file it cannot show is not downloaded behind the user's back. */
  decisionHandler(allowed && response.canShowMIMEType ? WKNavigationResponsePolicyAllow : WKNavigationResponsePolicyCancel);
}

- (void)webView:(WKWebView *)webView didStartProvisionalNavigation:(WKNavigation *)navigation {
  if (!self.closed) actBrowserStarted(self.handle);
}

- (void)webView:(WKWebView *)webView didCommitNavigation:(WKNavigation *)navigation {
  [webView setValue:@YES forKey:@"drawsBackground"];
  if (!self.closed) actBrowserCommitted(self.handle);
}

- (void)webView:(WKWebView *)webView didFinishNavigation:(WKNavigation *)navigation {
  if (!self.closed) actBrowserFinished(self.handle);
}

- (void)failed:(NSError *)error provisional:(BOOL)provisional {
  if (self.closed) return;
  NSString *message = failureMessage(error);
  actBrowserFailed(self.handle, provisional, (char *)[error.localizedDescription UTF8String], (char *)[(message ?: @"") UTF8String]);
}

- (void)webView:(WKWebView *)webView didFailProvisionalNavigation:(WKNavigation *)navigation withError:(NSError *)error {
  [self failed:error provisional:YES];
}

- (void)webView:(WKWebView *)webView didFailNavigation:(WKNavigation *)navigation withError:(NSError *)error {
  [self failed:error provisional:NO];
}

- (void)webViewWebContentProcessDidTerminate:(WKWebView *)webView {
  if (!self.closed) actBrowserTerminated(self.handle);
}

- (void)userContentController:(WKUserContentController *)controller didReceiveScriptMessage:(WKScriptMessage *)message {
  if (self.closed || message.webView != self.view || !message.frameInfo.isMainFrame) return;
  if (![message.body isKindOfClass:[NSString class]]) return;
  WKSecurityOrigin *origin = message.frameInfo.securityOrigin;
  NSString *text = @"";
  if (origin.protocol.length && origin.host.length) {
    BOOL standard = origin.port == 0 || ([origin.protocol isEqualToString:@"https"] && origin.port == 443) || ([origin.protocol isEqualToString:@"http"] && origin.port == 80);
    text = standard ? [NSString stringWithFormat:@"%@://%@", origin.protocol, origin.host]
                    : [NSString stringWithFormat:@"%@://%@:%ld", origin.protocol, origin.host, (long)origin.port];
  }
  actBrowserMessage(self.handle, (char *)[text UTF8String], 0, (char *)[(NSString *)message.body UTF8String]);
}

- (WKWebView *)webView:(WKWebView *)webView createWebViewWithConfiguration:(WKWebViewConfiguration *)configuration forNavigationAction:(WKNavigationAction *)action windowFeatures:(WKWindowFeatures *)features {
  if (self.closed) return nil;
  uint64_t popup = actBrowserPopup(self.handle, (char *)[urlText(action.request.URL) UTF8String]);
  if (!popup) return nil;
  NSWindow *window = [[NSWindow alloc] initWithContentRect:NSMakeRect(0, 0, PopupSize.width, PopupSize.height)
                                                 styleMask:NSWindowStyleMaskTitled | NSWindowStyleMaskClosable | NSWindowStyleMaskMiniaturizable | NSWindowStyleMaskResizable
                                                   backing:NSBackingStoreBuffered
                                                     defer:NO];
  window.releasedWhenClosed = NO;
  WKWebView *view = [[WKWebView alloc] initWithFrame:NSMakeRect(0, 0, PopupSize.width, PopupSize.height) configuration:configuration];
  view.customUserAgent = userAgent;
  ACTBrowserPage *page = [ACTBrowserPage new];
  page.handle = popup;
  page.view = view;
  page.window = window;
  view.navigationDelegate = page;
  view.UIDelegate = page;
  window.delegate = page;
  window.contentView = view;
  pages[@(popup)] = page;
  [window center];
  [window makeKeyAndOrderFront:nil];
  return view;
}

- (void)webViewDidClose:(WKWebView *)webView {
  if (self.window) [self.window close];
}

- (void)windowWillClose:(NSNotification *)notification {
  if (self.closed) return;
  self.closed = YES;
  self.view.navigationDelegate = nil;
  self.view.UIDelegate = nil;
  self.window.delegate = nil;
  uint64_t handle = self.handle;
  [pages removeObjectForKey:@(handle)];
  actBrowserPopupClosed(handle);
}

- (void)webView:(WKWebView *)webView runOpenPanelWithParameters:(WKOpenPanelParameters *)parameters initiatedByFrame:(WKFrameInfo *)frame completionHandler:(void (^)(NSArray<NSURL *> *))completionHandler {
  NSOpenPanel *panel = [NSOpenPanel openPanel];
  panel.allowsMultipleSelection = parameters.allowsMultipleSelection;
  panel.canChooseDirectories = parameters.allowsDirectories;
  panel.canChooseFiles = YES;
  NSWindow *window = webView.window && webView.window.isVisible ? webView.window : hostWindow;
  if (!window) { completionHandler(nil); return; }
  [panel beginSheetModalForWindow:window completionHandler:^(NSModalResponse result) {
    completionHandler(result == NSModalResponseOK ? panel.URLs : nil);
  }];
}

/* Nothing in the panel would ask the user, so a page never gets the camera or microphone. */
- (void)webView:(WKWebView *)webView requestMediaCapturePermissionForOrigin:(WKSecurityOrigin *)origin initiatedByFrame:(WKFrameInfo *)frame type:(WKMediaCaptureType)type decisionHandler:(void (^)(WKPermissionDecision))decisionHandler API_AVAILABLE(macos(12.0)) {
  decisionHandler(WKPermissionDecisionDeny);
}

@end

/* DOM codes by macOS virtual key code, and the key names that differ from the character typed. */
typedef struct {
  unsigned short keyCode;
  const char *code;
  const char *key;
} KeyName;

static const KeyName KeyNames[] = {
    {0x00, "KeyA", NULL}, {0x01, "KeyS", NULL}, {0x02, "KeyD", NULL}, {0x03, "KeyF", NULL}, {0x04, "KeyH", NULL}, {0x05, "KeyG", NULL},
    {0x06, "KeyZ", NULL}, {0x07, "KeyX", NULL}, {0x08, "KeyC", NULL}, {0x09, "KeyV", NULL}, {0x0A, "IntlBackslash", NULL}, {0x0B, "KeyB", NULL},
    {0x0C, "KeyQ", NULL}, {0x0D, "KeyW", NULL}, {0x0E, "KeyE", NULL}, {0x0F, "KeyR", NULL}, {0x10, "KeyY", NULL}, {0x11, "KeyT", NULL},
    {0x12, "Digit1", NULL}, {0x13, "Digit2", NULL}, {0x14, "Digit3", NULL}, {0x15, "Digit4", NULL}, {0x16, "Digit6", NULL}, {0x17, "Digit5", NULL},
    {0x18, "Equal", NULL}, {0x19, "Digit9", NULL}, {0x1A, "Digit7", NULL}, {0x1B, "Minus", NULL}, {0x1C, "Digit8", NULL}, {0x1D, "Digit0", NULL},
    {0x1E, "BracketRight", NULL}, {0x1F, "KeyO", NULL}, {0x20, "KeyU", NULL}, {0x21, "BracketLeft", NULL}, {0x22, "KeyI", NULL}, {0x23, "KeyP", NULL},
    {0x24, "Enter", "Enter"}, {0x25, "KeyL", NULL}, {0x26, "KeyJ", NULL}, {0x27, "Quote", NULL}, {0x28, "KeyK", NULL}, {0x29, "Semicolon", NULL},
    {0x2A, "Backslash", NULL}, {0x2B, "Comma", NULL}, {0x2C, "Slash", NULL}, {0x2D, "KeyN", NULL}, {0x2E, "KeyM", NULL}, {0x2F, "Period", NULL},
    {0x30, "Tab", "Tab"}, {0x31, "Space", " "}, {0x32, "Backquote", NULL}, {0x33, "Backspace", "Backspace"}, {0x35, "Escape", "Escape"},
    {0x36, "MetaRight", "Meta"}, {0x37, "MetaLeft", "Meta"}, {0x38, "ShiftLeft", "Shift"}, {0x39, "CapsLock", "CapsLock"}, {0x3A, "AltLeft", "Alt"},
    {0x3B, "ControlLeft", "Control"}, {0x3C, "ShiftRight", "Shift"}, {0x3D, "AltRight", "Alt"}, {0x3E, "ControlRight", "Control"}, {0x3F, "Fn", "Fn"},
    {0x40, "F17", "F17"}, {0x41, "NumpadDecimal", NULL}, {0x43, "NumpadMultiply", NULL}, {0x45, "NumpadAdd", NULL}, {0x47, "NumLock", "Clear"},
    {0x4B, "NumpadDivide", NULL}, {0x4C, "NumpadEnter", "Enter"}, {0x4E, "NumpadSubtract", NULL}, {0x4F, "F18", "F18"}, {0x50, "F19", "F19"},
    {0x51, "NumpadEqual", NULL}, {0x52, "Numpad0", NULL}, {0x53, "Numpad1", NULL}, {0x54, "Numpad2", NULL}, {0x55, "Numpad3", NULL},
    {0x56, "Numpad4", NULL}, {0x57, "Numpad5", NULL}, {0x58, "Numpad6", NULL}, {0x59, "Numpad7", NULL}, {0x5A, "F20", "F20"}, {0x5B, "Numpad8", NULL},
    {0x5C, "Numpad9", NULL}, {0x5D, "IntlYen", NULL}, {0x5E, "IntlRo", NULL}, {0x5F, "NumpadComma", NULL}, {0x60, "F5", "F5"}, {0x61, "F6", "F6"},
    {0x62, "F7", "F7"}, {0x63, "F3", "F3"}, {0x64, "F8", "F8"}, {0x65, "F9", "F9"}, {0x67, "F11", "F11"}, {0x69, "F13", "F13"}, {0x6A, "F16", "F16"},
    {0x6B, "F14", "F14"}, {0x6D, "F10", "F10"}, {0x6E, "ContextMenu", "ContextMenu"}, {0x6F, "F12", "F12"}, {0x71, "F15", "F15"}, {0x72, "Help", "Help"},
    {0x73, "Home", "Home"}, {0x74, "PageUp", "PageUp"}, {0x75, "Delete", "Delete"}, {0x76, "F4", "F4"}, {0x77, "End", "End"}, {0x78, "F2", "F2"},
    {0x79, "PageDown", "PageDown"}, {0x7A, "F1", "F1"}, {0x7B, "ArrowLeft", "ArrowLeft"}, {0x7C, "ArrowRight", "ArrowRight"},
    {0x7D, "ArrowDown", "ArrowDown"}, {0x7E, "ArrowUp", "ArrowUp"},
};

static const KeyName *keyNameOf(unsigned short keyCode) {
  for (size_t i = 0; i < sizeof(KeyNames) / sizeof(KeyNames[0]); i++) {
    if (KeyNames[i].keyCode == keyCode) return &KeyNames[i];
  }
  return NULL;
}

static NSEventModifierFlags modifierFlagOf(unsigned short keyCode) {
  switch (keyCode) {
  case 0x36: case 0x37: return NSEventModifierFlagCommand;
  case 0x38: case 0x3C: return NSEventModifierFlagShift;
  case 0x3A: case 0x3D: return NSEventModifierFlagOption;
  case 0x3B: case 0x3E: return NSEventModifierFlagControl;
  case 0x39: return NSEventModifierFlagCapsLock;
  default: return 0;
  }
}

/* Hands a key in a page to the app first, as Electron's before-input-event does. YES when the app took it. */
static BOOL offerKey(uint64_t handle, NSEvent *event) {
  const KeyName *name = keyNameOf(event.keyCode);
  NSString *type;
  NSString *key;
  if (event.type == NSEventTypeFlagsChanged) {
    NSEventModifierFlags flag = modifierFlagOf(event.keyCode);
    if (!flag || !name) return NO;
    type = (event.modifierFlags & flag) ? @"keyDown" : @"keyUp";
    key = @(name->key);
  } else {
    type = event.type == NSEventTypeKeyDown ? @"keyDown" : @"keyUp";
    if (name && name->key) key = @(name->key);
    else key = event.charactersIgnoringModifiers ?: @"";
  }
  NSEventModifierFlags flags = event.modifierFlags;
  BOOL repeat = event.type == NSEventTypeFlagsChanged ? NO : event.isARepeat;
  return actBrowserKey(handle, (char *)[type UTF8String], (char *)[key UTF8String], (char *)(name ? name->code : ""),
                       (flags & NSEventModifierFlagCommand) != 0, (flags & NSEventModifierFlagControl) != 0, (flags & NSEventModifierFlagOption) != 0,
                       (flags & NSEventModifierFlagShift) != 0, repeat) != 0;
}

/* A press or key in a page is the user taking it over, so a run driving it stops being its owner. */
static NSEvent *sawInput(NSEvent *event) {
  NSWindow *window = event.window;
  if (!window || !pages) return event;
  NSEventType type = event.type;
  NSArray<ACTBrowserPage *> *all = pages.allValues;
  if (type == NSEventTypeKeyDown || type == NSEventTypeKeyUp || type == NSEventTypeFlagsChanged) {
    NSResponder *responder = window.firstResponder;
    if (![responder isKindOfClass:[NSView class]]) return event;
    for (ACTBrowserPage *page in all) {
      if (!page.closed && [(NSView *)responder isDescendantOf:page.view]) return offerKey(page.handle, event) ? nil : event;
    }
    return event;
  }
  for (ACTBrowserPage *page in all) {
    if (page.closed) continue;
    if (page.window) {
      if (page.window == window) actBrowserPressed(page.handle);
      continue;
    }
    if (page.view.window != window || page.view.isHidden) continue;
    NSPoint point = [page.view convertPoint:event.locationInWindow fromView:nil];
    if (NSPointInRect(point, page.view.bounds)) actBrowserPressed(page.handle);
  }
  return event;
}

static WKWebView *findWebView(NSView *view) {
  if ([view isKindOfClass:[WKWebView class]]) return (WKWebView *)view;
  for (NSView *child in view.subviews) {
    WKWebView *found = findWebView(child);
    if (found) return found;
  }
  return nil;
}

static WKWebsiteDataStore *panelStore(void) {
  if (@available(macOS 14.0, *)) {
    NSUUID *identifier = [[NSUUID alloc] initWithUUIDBytes:DataStoreID];
    return [WKWebsiteDataStore dataStoreForIdentifier:identifier];
  }
  return [WKWebsiteDataStore defaultDataStore];
}

char *actBrowserStart(const char *agent, const char *diagnostics, const char *mouseNavigation, const char *handler) {
  __block char *failure = NULL;
  NSString *agentText = @(agent), *diagnosticsText = @(diagnostics), *mouseText = @(mouseNavigation), *handlerText = @(handler);
  runOnMain(^{
    NSWindow *found = nil;
    for (NSWindow *window in [NSApp windows]) {
      if ([NSStringFromClass([window class]) isEqualToString:@"WailsWindow"]) { found = window; break; }
    }
    WKWebView *view = found ? findWebView(found.contentView) : nil;
    if (!view) { failure = strdup("The browser panel has no window to draw pages over."); return; }
    hostWindow = found;
    hostView = view;
    if (!pages) pages = [NSMutableDictionary new];
    store = panelStore();
    userAgent = agentText;
    handlerName = handlerText;
    diagnosticsScript = [[WKUserScript alloc] initWithSource:diagnosticsText injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:YES];
    mouseScript = [[WKUserScript alloc] initWithSource:mouseText injectionTime:WKUserScriptInjectionTimeAtDocumentStart forMainFrameOnly:YES];
    if (!inputMonitor) {
      NSEventMask mask = NSEventMaskLeftMouseDown | NSEventMaskRightMouseDown | NSEventMaskOtherMouseDown | NSEventMaskKeyDown | NSEventMaskKeyUp | NSEventMaskFlagsChanged;
      inputMonitor = [NSEvent addLocalMonitorForEventsMatchingMask:mask handler:^NSEvent *(NSEvent *event) { return sawInput(event); }];
    }
  });
  return failure;
}

void actBrowserStop(void) {
  runOnMain(^{
    if (inputMonitor) [NSEvent removeMonitor:inputMonitor];
    inputMonitor = nil;
    for (ACTBrowserPage *page in pages.allValues) {
      [page detach];
      if (page.window) [page.window close];
      else [page.view removeFromSuperview];
    }
    [pages removeAllObjects];
    [parking close];
    parking = nil;
    hostWindow = nil;
    hostView = nil;
    cookieSnapshot = nil;
  });
}

/* Where a page waits while the panel is not showing it: a window nobody sees, which gives it a
   viewport to lay out in without putting anything on screen or taking the keyboard from the app. */
static NSWindow *parkingWindow(void) {
  if (parking) return parking;
  parking = [[NSWindow alloc] initWithContentRect:NSMakeRect(-20000, -20000, ParkedSize.width, ParkedSize.height)
                                        styleMask:NSWindowStyleMaskBorderless
                                          backing:NSBackingStoreBuffered
                                            defer:NO];
  parking.releasedWhenClosed = NO;
  return parking;
}

char *actBrowserOpen(uint64_t handle, double width, double height) {
  __block char *failure = NULL;
  runOnMain(^{
    if (!hostView) { failure = strdup("The browser panel has no window to draw pages over."); return; }
    WKWebViewConfiguration *configuration = [WKWebViewConfiguration new];
    configuration.websiteDataStore = store;
    configuration.preferences.javaScriptCanOpenWindowsAutomatically = YES;
    if (@available(macOS 12.3, *)) configuration.preferences.elementFullscreenEnabled = YES;
    WKUserContentController *content = [WKUserContentController new];
    [content addUserScript:diagnosticsScript];
    [content addUserScript:mouseScript];
    configuration.userContentController = content;
    NSView *home = parkingWindow().contentView;
    NSRect frame = NSMakeRect(0, home.isFlipped ? 0 : home.frame.size.height - height, width, height);
    WKWebView *view = [[WKWebView alloc] initWithFrame:frame configuration:configuration];
    view.customUserAgent = userAgent;
    view.allowsBackForwardNavigationGestures = YES;
    if (@available(macOS 13.3, *)) view.inspectable = YES;
    view.hidden = YES;
    /* A tab that has loaded nothing draws nothing, so the panel's empty state shows through it. */
    [view setValue:@NO forKey:@"drawsBackground"];
    ACTBrowserPage *page = [ACTBrowserPage new];
    page.handle = handle;
    page.view = view;
    [content addScriptMessageHandler:page name:handlerName];
    view.navigationDelegate = page;
    view.UIDelegate = page;
    [page observe];
    pages[@(handle)] = page;
    [home addSubview:view];
  });
  return failure;
}

char *actBrowserLoad(uint64_t handle, const char *url) {
  __block char *failure = NULL;
  NSString *text = @(url);
  runOnMain(^{
    ACTBrowserPage *page = pageFor(handle);
    if (!page) return;
    NSURL *address = [NSURL URLWithString:text];
    if (!address) { failure = copied([NSString stringWithFormat:@"Cannot load %@", text]); return; }
    [page.view loadRequest:[NSURLRequest requestWithURL:address]];
  });
  return failure;
}

void actBrowserBack(uint64_t handle) {
  runOnMain(^{ [pageFor(handle).view goBack]; });
}

void actBrowserForward(uint64_t handle) {
  runOnMain(^{ [pageFor(handle).view goForward]; });
}

void actBrowserReload(uint64_t handle) {
  runOnMain(^{ [pageFor(handle).view reload]; });
}

void actBrowserClose(uint64_t handle) {
  runOnMain(^{
    ACTBrowserPage *page = pageFor(handle);
    if (!page) return;
    [pages removeObjectForKey:@(handle)];
    BOOL held = holdsKeyboard(page.view);
    [page detach];
    if (page.window) {
      page.window.delegate = nil;
      [page.window close];
    } else {
      [page.view stopLoading];
      [page.view removeFromSuperview];
    }
    if (held) giveKeysBack();
  });
}

void actBrowserPlace(uint64_t handle, int shown, double x, double y, double width, double height) {
  runOnMain(^{
    ACTBrowserPage *page = pageFor(handle);
    if (!page || page.window || !hostView) return;
    WKWebView *view = page.view;
    NSView *home = shown ? hostView.superview : parkingWindow().contentView;
    if (!home) return;
    if (view.superview != home) {
      BOOL held = holdsKeyboard(view);
      [view removeFromSuperview];
      if (shown) [home addSubview:view positioned:NSWindowAbove relativeTo:hostView];
      else [home addSubview:view];
      if (held) giveKeysBack();
    }
    NSRect frame;
    if (shown) {
      NSRect host = hostView.frame;
      double top = home.isFlipped ? host.origin.y + y : NSMaxY(host) - y - height;
      frame = NSMakeRect(host.origin.x + x, top, width, height);
    } else {
      double parent = home.frame.size.height;
      frame = NSMakeRect(0, home.isFlipped ? 0 : parent - height, width, height);
    }
    view.frame = frame;
    view.hidden = !shown;
  });
}

void actBrowserWake(uint64_t handle) {
  runOnMain(^{ pageFor(handle).view.hidden = NO; });
}

double actBrowserZoom(void) {
  __block double zoom = 1;
  runOnMain(^{
    if (@available(macOS 11.0, *)) {
      if (hostView && hostView.pageZoom > 0) zoom = hostView.pageZoom;
    }
  });
  return zoom;
}

void actBrowserState(uint64_t handle, ACTBrowserState *out) {
  runOnMain(^{
    ACTBrowserPage *page = pageFor(handle);
    WKWebView *view = page.view;
    out->url = copied(urlText(view.URL));
    out->title = copied(view.title);
    out->loading = view.isLoading;
    out->canGoBack = view.canGoBack;
    out->canGoForward = view.canGoForward;
  });
}

void actBrowserFocus(uint64_t handle) {
  runOnMain(^{
    WKWebView *view = pageFor(handle).view;
    if (view.window) [view.window makeFirstResponder:view];
  });
}

/* What a script threw, as the page wrote it, without the `Error: ` WebKit puts before it. */
static NSString *scriptError(NSError *error) {
  NSString *message = error.userInfo[@"WKJavaScriptExceptionMessage"];
  if (![message isKindOfClass:[NSString class]]) message = error.localizedDescription;
  if ([message hasPrefix:@"Error: "]) message = [message substringFromIndex:7];
  return message;
}

void actBrowserEvaluate(uint64_t handle, const char *script, uint64_t call) {
  NSString *body = [NSString stringWithFormat:@"const value = await (%@);\nreturn value === undefined ? null : JSON.stringify(value);", @(script)];
  dispatch_async(dispatch_get_main_queue(), ^{
    ACTBrowserPage *page = pageFor(handle);
    if (!page) { actBrowserEvaluated(call, NULL, "The page went away while it was being read."); return; }
    [page.view callAsyncJavaScript:body arguments:nil inFrame:nil inContentWorld:WKContentWorld.defaultClientWorld completionHandler:^(id result, NSError *error) {
      if (error) { actBrowserEvaluated(call, NULL, (char *)[scriptError(error) UTF8String]); return; }
      NSString *json = [result isKindOfClass:[NSString class]] ? result : @"null";
      actBrowserEvaluated(call, (char *)[json UTF8String], NULL);
    }];
  });
}

/* Draws an image into a bitmap of points at scale and encodes it as PNG. */
static NSData *rasterize(NSImage *image, NSSize points, double scale) {
  NSInteger width = MAX(1, (NSInteger)llround(points.width * scale)), height = MAX(1, (NSInteger)llround(points.height * scale));
  NSBitmapImageRep *rep = [[NSBitmapImageRep alloc] initWithBitmapDataPlanes:NULL pixelsWide:width pixelsHigh:height bitsPerSample:8 samplesPerPixel:4
                                                                    hasAlpha:YES isPlanar:NO colorSpaceName:NSCalibratedRGBColorSpace bytesPerRow:0 bitsPerPixel:0];
  if (!rep) return nil;
  rep.size = points;
  NSGraphicsContext *context = [NSGraphicsContext graphicsContextWithBitmapImageRep:rep];
  if (!context) return nil;
  [NSGraphicsContext saveGraphicsState];
  [NSGraphicsContext setCurrentContext:context];
  [image drawInRect:NSMakeRect(0, 0, points.width, points.height)];
  [context flushGraphics];
  [NSGraphicsContext restoreGraphicsState];
  return [rep representationUsingType:NSBitmapImageFileTypePNG properties:@{}];
}

static void drawn(uint64_t call, NSData *data, NSString *error) {
  if (!data) {
    actBrowserDrawn(call, NULL, 0, (char *)[(error ?: @"Could not encode the picture.") UTF8String]);
    return;
  }
  actBrowserDrawn(call, (void *)data.bytes, (int)data.length, NULL);
}

void actBrowserDraw(uint64_t handle, int fullPage, double width, double height, uint64_t call) {
  dispatch_async(dispatch_get_main_queue(), ^{
    ACTBrowserPage *page = pageFor(handle);
    if (!page) { drawn(call, nil, @"The page went away while it was being drawn."); return; }
    WKWebView *view = page.view;
    double scale = view.window ? view.window.backingScaleFactor : (NSScreen.mainScreen ? NSScreen.mainScreen.backingScaleFactor : 2);
    if (!fullPage) {
      WKSnapshotConfiguration *configuration = [WKSnapshotConfiguration new];
      configuration.afterScreenUpdates = NO;
      [view takeSnapshotWithConfiguration:configuration completionHandler:^(NSImage *image, NSError *error) {
        if (!image) { drawn(call, nil, error ? error.localizedDescription : @"The page drew nothing."); return; }
        drawn(call, rasterize(image, image.size, scale), nil);
      }];
      return;
    }
    /* The whole page, drawn through a PDF of it so nothing on screen moves. */
    NSSize size = NSMakeSize(width, height);
    WKPDFConfiguration *configuration = [WKPDFConfiguration new];
    configuration.rect = NSMakeRect(0, 0, width, height);
    [view createPDFWithConfiguration:configuration completionHandler:^(NSData *data, NSError *error) {
      if (!data) { drawn(call, nil, error ? error.localizedDescription : @"The page drew nothing."); return; }
      NSImage *image = [[NSImage alloc] initWithData:data];
      if (!image) { drawn(call, nil, @"Could not read the drawn page."); return; }
      drawn(call, rasterize(image, size, scale), nil);
    }];
  });
}

void actBrowserFind(uint64_t handle, const char *query, int backwards, uint64_t call) {
  NSString *text = @(query);
  dispatch_async(dispatch_get_main_queue(), ^{
    ACTBrowserPage *page = pageFor(handle);
    if (!page) { actBrowserFound(call, 0); return; }
    WKFindConfiguration *configuration = [WKFindConfiguration new];
    configuration.backwards = backwards != 0;
    configuration.wraps = YES;
    configuration.caseSensitive = NO;
    [page.view findString:text withConfiguration:configuration completionHandler:^(WKFindResult *result) {
      actBrowserFound(call, result.matchFound);
    }];
  });
}

void actBrowserCookies(uint64_t call) {
  dispatch_async(dispatch_get_main_queue(), ^{
    if (!store) { actBrowserCookieList(call, 0, "[]"); return; }
    [store.httpCookieStore getAllCookies:^(NSArray<NSHTTPCookie *> *cookies) {
      cookieSnapshot = cookies;
      uint64_t snapshot = ++cookieSnapshotID;
      NSMutableArray *list = [NSMutableArray arrayWithCapacity:cookies.count];
      for (NSHTTPCookie *cookie in cookies) {
        [list addObject:@{@"domain" : cookie.domain ?: @"", @"name" : cookie.name ?: @"", @"path" : cookie.path ?: @"", @"secure" : @(cookie.isSecure)}];
      }
      NSData *json = [NSJSONSerialization dataWithJSONObject:list options:0 error:nil];
      NSString *text = json ? [[NSString alloc] initWithData:json encoding:NSUTF8StringEncoding] : @"[]";
      actBrowserCookieList(call, snapshot, (char *)[text UTF8String]);
    }];
  });
}

void actBrowserDeleteCookie(uint64_t snapshot, int index, uint64_t call) {
  dispatch_async(dispatch_get_main_queue(), ^{
    if (!store || snapshot != cookieSnapshotID || index < 0 || (NSUInteger)index >= cookieSnapshot.count) { actBrowserDone(call, 0); return; }
    [store.httpCookieStore deleteCookie:cookieSnapshot[index] completionHandler:^{ actBrowserDone(call, 1); }];
  });
}

void actBrowserSetCookie(const char *details, uint64_t call) {
  NSData *data = [NSData dataWithBytes:details length:strlen(details)];
  dispatch_async(dispatch_get_main_queue(), ^{
    NSDictionary *cookie = [NSJSONSerialization JSONObjectWithData:data options:0 error:nil];
    if (!store || ![cookie isKindOfClass:[NSDictionary class]]) { actBrowserDone(call, 0); return; }
    NSMutableDictionary *properties = [NSMutableDictionary dictionary];
    properties[NSHTTPCookieName] = cookie[@"name"];
    properties[NSHTTPCookieValue] = cookie[@"value"];
    properties[NSHTTPCookiePath] = cookie[@"path"];
    if ([cookie[@"domain"] isKindOfClass:[NSString class]]) properties[NSHTTPCookieDomain] = cookie[@"domain"];
    else {
      NSURL *origin = [NSURL URLWithString:cookie[@"url"]];
      if (!origin) { actBrowserDone(call, 0); return; }
      properties[NSHTTPCookieOriginURL] = origin;
    }
    if ([cookie[@"secure"] boolValue]) properties[NSHTTPCookieSecure] = @"TRUE";
    if ([cookie[@"httpOnly"] boolValue]) properties[@"HttpOnly"] = @"TRUE";
    NSString *sameSite = cookie[@"sameSite"];
    if ([sameSite isEqualToString:@"lax"]) properties[@"SameSite"] = @"lax";
    else if ([sameSite isEqualToString:@"strict"]) properties[@"SameSite"] = @"strict";
    else if ([sameSite isEqualToString:@"no_restriction"]) properties[@"SameSite"] = @"none";
    if ([cookie[@"expirationDate"] isKindOfClass:[NSNumber class]]) {
      properties[NSHTTPCookieExpires] = [NSDate dateWithTimeIntervalSince1970:[cookie[@"expirationDate"] doubleValue]];
    }
    NSHTTPCookie *made = [NSHTTPCookie cookieWithProperties:properties];
    if (!made) { actBrowserDone(call, 0); return; }
    [store.httpCookieStore setCookie:made completionHandler:^{ actBrowserDone(call, 1); }];
  });
}

void actBrowserClearData(uint64_t call) {
  dispatch_async(dispatch_get_main_queue(), ^{
    if (!store) { actBrowserDone(call, 1); return; }
    NSSet *types = [WKWebsiteDataStore allWebsiteDataTypes];
    if (@available(macOS 14.0, *)) {
      [store removeDataOfTypes:types modifiedSince:[NSDate distantPast] completionHandler:^{ actBrowserDone(call, 1); }];
      return;
    }
    /* Before macOS 14 the panel shares the app's own store, whose records it leaves alone. */
    [store fetchDataRecordsOfTypes:types completionHandler:^(NSArray<WKWebsiteDataRecord *> *records) {
      NSMutableArray *sites = [NSMutableArray array];
      for (WKWebsiteDataRecord *record in records) {
        if (record.displayName.length && ![record.displayName isEqualToString:@"wails"] && ![record.displayName isEqualToString:@"localhost"]) [sites addObject:record];
      }
      [store removeDataOfTypes:types forDataRecords:sites completionHandler:^{ actBrowserDone(call, 1); }];
    }];
  });
}
