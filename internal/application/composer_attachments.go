package application

var NoAttachmentSend = &AttachmentSendState{Busy: false, Error: nil, Sent: []string{}}

// The main composer sends for whichever thread is in front, so it has no thread of its own to key by.
const MainComposer = ""

// The send state of a thread's composer, or the main composer's when taskID is nil.
func AttachmentSendFor(sends map[string]*AttachmentSendState, taskID *string) *AttachmentSendState {
	key := MainComposer
	if taskID != nil {
		key = *taskID
	}
	if send, ok := sends[key]; ok && send != nil {
		return send
	}
	return NoAttachmentSend
}
