package application

import "github.com/zyuapp/ai-coding-tool-go/internal/contracts"

// Requests that need the transfer deadline rather than the ordinary request deadline.
func IsComputerTransfer(message ComputerClientMessage) bool {
	switch message := message.(type) {
	case ComputerInputRequest:
		for _, input := range message.Inputs {
			if send, ok := input.(contracts.TaskCommandAttachmentsSend); ok && len(send.Attachments) > 0 {
				return true
			}
		}
		return false
	case contracts.ComputerQueryRequest:
		switch message.Query.(type) {
		case contracts.ComputerQueryAttachment, contracts.ComputerQueryMessageImage:
			return true
		}
	}
	return false
}

// Whether a decoded value is a message one computer may send another.
func IsComputerClientMessage(value any) bool {
	return contracts.IsComputerClientRequest(value)
}
