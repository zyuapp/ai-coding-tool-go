package devices

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AVFoundation -framework Foundation
#import <AVFoundation/AVFoundation.h>

// AVAuthorizationStatus: 0 not determined, 1 restricted, 2 denied, 3 authorized.
static int actMicrophoneStatus(void) {
  return (int)[AVCaptureDevice authorizationStatusForMediaType:AVMediaTypeAudio];
}

// Asks once and waits for the answer. Needs NSMicrophoneUsageDescription in the app's Info.plist.
static int actRequestMicrophone(void) {
  dispatch_semaphore_t answered = dispatch_semaphore_create(0);
  __block BOOL allowed = NO;
  [AVCaptureDevice requestAccessForMediaType:AVMediaTypeAudio completionHandler:^(BOOL granted) {
    allowed = granted;
    dispatch_semaphore_signal(answered);
  }];
  dispatch_semaphore_wait(answered, DISPATCH_TIME_FOREVER);
  return allowed ? 1 : 0;
}
*/
import "C"

import "context"

// microphoneAccess is whether the app may hear the microphone, asking the person when it never has.
func microphoneAccess(ctx context.Context) bool {
	switch C.actMicrophoneStatus() {
	case 3:
		return true
	case 0:
		answer := make(chan bool, 1)
		go func() { answer <- C.actRequestMicrophone() != 0 }()
		select {
		case allowed := <-answer:
			return allowed
		case <-ctx.Done():
			return false
		}
	}
	return false
}
