#import <Carbon/Carbon.h>
#import <Foundation/Foundation.h>
#include "_cgo_export.h"

static EventHandlerRef actHotkeyHandlerRef = NULL;
static NSMutableDictionary<NSNumber *, NSValue *> *actHotkeyRefs = nil;

static OSStatus actHotkeyHandler(EventHandlerCallRef next, EventRef event, void *data) {
  EventHotKeyID hotkey;
  if (GetEventParameter(event, kEventParamDirectObject, typeEventHotKeyID, NULL, sizeof(hotkey), NULL, &hotkey) != noErr) return eventNotHandledErr;
  if (hotkey.signature != 'aict') return eventNotHandledErr;
  actHotkeyPressed(hotkey.id);
  return noErr;
}

static void actOnMainThread(dispatch_block_t work) {
  if ([NSThread isMainThread]) work();
  else dispatch_sync(dispatch_get_main_queue(), work);
}

int actRegisterHotkey(uint32_t identifier, uint32_t keyCode, uint32_t modifiers) {
  __block int registered = 0;
  actOnMainThread(^{
    if (!actHotkeyHandlerRef) {
      EventTypeSpec pressed = {kEventClassKeyboard, kEventHotKeyPressed};
      if (InstallApplicationEventHandler(&actHotkeyHandler, 1, &pressed, NULL, &actHotkeyHandlerRef) != noErr) return;
      actHotkeyRefs = [NSMutableDictionary dictionary];
    }
    EventHotKeyRef ref = NULL;
    EventHotKeyID hotkey = {'aict', identifier};
    if (RegisterEventHotKey(keyCode, modifiers, hotkey, GetApplicationEventTarget(), 0, &ref) != noErr || !ref) return;
    actHotkeyRefs[@(identifier)] = [NSValue valueWithPointer:ref];
    registered = 1;
  });
  return registered;
}

void actUnregisterHotkey(uint32_t identifier) {
  actOnMainThread(^{
    NSValue *ref = actHotkeyRefs[@(identifier)];
    if (!ref) return;
    UnregisterEventHotKey((EventHotKeyRef)ref.pointerValue);
    [actHotkeyRefs removeObjectForKey:@(identifier)];
  });
}
