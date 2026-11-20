package application

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/contracts"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

func InitialGitState() GitState {
	return GitState{Environments: map[string]contracts.ChangedFilesResult{}, PullRequest: nil, MemberPullRequests: map[string]*domain.PullRequestRead{}}
}
