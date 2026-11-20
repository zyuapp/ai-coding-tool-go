package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialRemoteState() RemoteState {
	return RemoteState{Remote: domain.EmptyMobileServerState()}
}
