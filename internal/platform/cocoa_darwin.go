package platform

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Foundation -framework AppKit -framework UserNotifications -framework IOKit -framework CoreGraphics

#import <Foundation/Foundation.h>
#import <AppKit/AppKit.h>
#import <UserNotifications/UserNotifications.h>
#import <IOKit/pwr_mgt/IOPMLib.h>
#import <CoreGraphics/CoreGraphics.h>
#import <objc/runtime.h>
#include <stdlib.h>

extern void platformNoticeClicked(char *identifier);
extern void platformDockMarkAllRead(void);
extern void platformScreenLock(int locked);

static void onMain(dispatch_block_t block) {
	if ([NSThread isMainThread]) block();
	else dispatch_async(dispatch_get_main_queue(), block);
}

// Notifications ------------------------------------------------------------------------------

@interface ACTNoticeDelegate : NSObject <UNUserNotificationCenterDelegate>
@end

@implementation ACTNoticeDelegate
- (void)userNotificationCenter:(UNUserNotificationCenter *)center willPresentNotification:(UNNotification *)notification withCompletionHandler:(void (^)(UNNotificationPresentationOptions))completionHandler {
	completionHandler(UNNotificationPresentationOptionBanner | UNNotificationPresentationOptionList);
}
- (void)userNotificationCenter:(UNUserNotificationCenter *)center didReceiveNotificationResponse:(UNNotificationResponse *)response withCompletionHandler:(void (^)(void))completionHandler {
	if ([response.actionIdentifier isEqualToString:UNNotificationDefaultActionIdentifier]) {
		platformNoticeClicked((char *)[response.notification.request.identifier UTF8String]);
	}
	completionHandler();
}
@end

static ACTNoticeDelegate *noticeDelegate = nil;
static BOOL noticeAuthorized = NO;
static BOOL noticeAsked = NO;

static int noticesSupported(void) {
	return [[NSBundle mainBundle] bundleIdentifier] != nil;
}

static void showNotice(UNUserNotificationCenter *center, NSString *identifier, NSString *title, NSString *body) {
	UNMutableNotificationContent *content = [[UNMutableNotificationContent alloc] init];
	content.title = title;
	content.body = body;
	UNNotificationRequest *request = [UNNotificationRequest requestWithIdentifier:identifier content:content trigger:nil];
	[center addNotificationRequest:request withCompletionHandler:nil];
}

static void postNotice(const char *identifier, const char *title, const char *body) {
	if (!noticesSupported()) return;
	NSString *nsIdentifier = [NSString stringWithUTF8String:identifier];
	NSString *nsTitle = [NSString stringWithUTF8String:title];
	NSString *nsBody = [NSString stringWithUTF8String:body];
	onMain(^{
		UNUserNotificationCenter *center = [UNUserNotificationCenter currentNotificationCenter];
		if (noticeDelegate == nil) {
			noticeDelegate = [[ACTNoticeDelegate alloc] init];
			center.delegate = noticeDelegate;
		}
		if (noticeAuthorized) {
			showNotice(center, nsIdentifier, nsTitle, nsBody);
			return;
		}
		[center requestAuthorizationWithOptions:(UNAuthorizationOptionAlert | UNAuthorizationOptionBadge | UNAuthorizationOptionSound) completionHandler:^(BOOL granted, NSError *error) {
			noticeAsked = YES;
			noticeAuthorized = granted;
			if (granted) showNotice(center, nsIdentifier, nsTitle, nsBody);
		}];
	});
}

// Dock ---------------------------------------------------------------------------------------

static BOOL dockHasUnread = NO;
static BOOL dockMenuInstalled = NO;

@interface ACTDockTarget : NSObject
@end
@implementation ACTDockTarget
- (void)markAllRead:(id)sender { platformDockMarkAllRead(); }
@end

static ACTDockTarget *dockTarget = nil;

static NSMenu *dockMenu(id self, SEL _cmd, NSApplication *sender) {
	NSMenu *menu = [[NSMenu alloc] init];
	menu.autoenablesItems = NO;
	NSMenuItem *item = [[NSMenuItem alloc] initWithTitle:@"Mark All as Read" action:@selector(markAllRead:) keyEquivalent:@""];
	item.target = dockTarget;
	item.enabled = dockHasUnread;
	[menu addItem:item];
	return menu;
}

static void installDockMenuWhenReady(int attempts) {
	onMain(^{
		id delegate = [NSApp delegate];
		if (delegate == nil) {
			if (attempts > 0) {
				dispatch_after(dispatch_time(DISPATCH_TIME_NOW, (int64_t)(0.5 * NSEC_PER_SEC)), dispatch_get_main_queue(), ^{ installDockMenuWhenReady(attempts - 1); });
			}
			return;
		}
		if (dockMenuInstalled) return;
		dockMenuInstalled = YES;
		if (dockTarget == nil) dockTarget = [[ACTDockTarget alloc] init];
		Class cls = [delegate class];
		SEL selector = @selector(applicationDockMenu:);
		if (!class_addMethod(cls, selector, (IMP)dockMenu, "@@:@")) {
			class_replaceMethod(cls, selector, (IMP)dockMenu, "@@:@");
		}
	});
}

static void installDockMenu(void) {
	installDockMenuWhenReady(120);
}

static void setDockUnread(int unread) {
	onMain(^{ dockHasUnread = unread != 0; });
}

static void setDockBadge(const char *label) {
	NSString *text = [NSString stringWithUTF8String:label];
	onMain(^{
		if (NSApp == nil) return;
		[[NSApp dockTile] setBadgeLabel:(text.length ? text : nil)];
	});
}

// Screen lock and idle ------------------------------------------------------------------------

static id lockObserver = nil;
static id unlockObserver = nil;

static void watchScreenLock(int on) {
	NSDistributedNotificationCenter *center = [NSDistributedNotificationCenter defaultCenter];
	if (on) {
		if (lockObserver != nil) return;
		lockObserver = [center addObserverForName:@"com.apple.screenIsLocked" object:nil queue:[NSOperationQueue mainQueue] usingBlock:^(NSNotification *note) { platformScreenLock(1); }];
		unlockObserver = [center addObserverForName:@"com.apple.screenIsUnlocked" object:nil queue:[NSOperationQueue mainQueue] usingBlock:^(NSNotification *note) { platformScreenLock(0); }];
	} else {
		if (lockObserver != nil) [center removeObserver:lockObserver];
		if (unlockObserver != nil) [center removeObserver:unlockObserver];
		lockObserver = nil;
		unlockObserver = nil;
	}
}

static int screenLocked(void) {
	CFDictionaryRef session = CGSessionCopyCurrentDictionary();
	if (session == NULL) return 0;
	int locked = 0;
	CFBooleanRef value = CFDictionaryGetValue(session, CFSTR("CGSSessionScreenIsLocked"));
	if (value != NULL && CFGetTypeID(value) == CFBooleanGetTypeID() && CFBooleanGetValue(value)) locked = 1;
	CFRelease(session);
	return locked;
}

static double idleSeconds(void) {
	return CGEventSourceSecondsSinceLastEventType(kCGEventSourceStateCombinedSessionState, kCGAnyInputEventType);
}

// Sleep --------------------------------------------------------------------------------------

static unsigned int holdAwake(void) {
	IOPMAssertionID assertion = 0;
	IOReturn result = IOPMAssertionCreateWithName(kIOPMAssertPreventUserIdleSystemSleep, kIOPMAssertionLevelOn, CFSTR("AI Coding Tool is working while the screen is locked"), &assertion);
	return result == kIOReturnSuccess ? assertion : 0;
}

static void releaseAwake(unsigned int assertion) {
	IOPMAssertionRelease((IOPMAssertionID)assertion);
}

// Trash --------------------------------------------------------------------------------------

static char *trashItem(const char *path) {
	NSError *error = nil;
	NSURL *url = [NSURL fileURLWithPath:[NSString stringWithUTF8String:path]];
	if ([[NSFileManager defaultManager] trashItemAtURL:url resultingItemURL:nil error:&error]) return NULL;
	return strdup([[error localizedDescription] UTF8String]);
}
*/
import "C"

import (
	"errors"
	"unsafe"
)

func cString(value string) (*C.char, func()) {
	pointer := C.CString(value)
	return pointer, func() { C.free(unsafe.Pointer(pointer)) }
}

func noticesSupported() bool { return C.noticesSupported() != 0 }

func postNotice(identifier, title, body string) {
	cIdentifier, freeIdentifier := cString(identifier)
	defer freeIdentifier()
	cTitle, freeTitle := cString(title)
	defer freeTitle()
	cBody, freeBody := cString(body)
	defer freeBody()
	C.postNotice(cIdentifier, cTitle, cBody)
}

func installCocoaDockMenu() { C.installDockMenu() }

func setCocoaDockUnread(unread bool) {
	value := 0
	if unread {
		value = 1
	}
	C.setDockUnread(C.int(value))
}

func setCocoaDockBadge(label string) {
	cLabel, free := cString(label)
	defer free()
	C.setDockBadge(cLabel)
}

func watchScreenLock(on bool) {
	value := 0
	if on {
		value = 1
	}
	C.watchScreenLock(C.int(value))
}

func screenLocked() bool { return C.screenLocked() != 0 }

func idleSeconds() float64 { return float64(C.idleSeconds()) }

func holdAwake() uint32 { return uint32(C.holdAwake()) }

func releaseAwake(assertion uint32) { C.releaseAwake(C.uint(assertion)) }

// TrashItem moves a file or folder to the Trash.
func TrashItem(path string) error {
	cPath, free := cString(path)
	defer free()
	failure := C.trashItem(cPath)
	if failure == nil {
		return nil
	}
	defer C.free(unsafe.Pointer(failure))
	return errors.New(C.GoString(failure))
}
