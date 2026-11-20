package application

import "github.com/zyuapp/ai-coding-tool-go/internal/domain"

func InitialAppUpdatesState() AppUpdatesState {
	return AppUpdatesState{AppUpdate: domain.NoAppUpdate, AppBuild: nil, AppUpdateRequest: nil, EngineUpgradesOwed: false}
}
