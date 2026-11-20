package computeruse

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
	"github.com/zyuapp/ai-coding-tool-go/internal/platform"
)

// needsGrants: Linux has no permission switches; the session decides what the driver reaches.
const needsGrants = false

func (h *Host) permissions() *domain.ComputerUsePermissions {
	return linuxPermissions(platform.ComputerUseCapability(h.env), h.binaryExists())
}

func (h *Host) request(domain.ComputerUsePermission) *domain.ComputerUsePermissions {
	return h.permissions()
}
