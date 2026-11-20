// Package mobile is the phone bridge: the local server phones and other computers reach through
// Tailscale Serve, the paired devices it trusts, and the push relay that wakes a closed phone.
package mobile

import (
	"github.com/zyuapp/ai-coding-tool-go/internal/application"
	"github.com/zyuapp/ai-coding-tool-go/internal/domain"
)

// BindHost is the only address the server binds: a phone reaches it through Tailscale Serve, never over the LAN.
const BindHost = "127.0.0.1"

// MobileHealthPath is a private probe that proves Tailscale Serve reaches this app rather than another local service.
const MobileHealthPath = domain.MobileAppPath + "/health"

const MobileHealthResponse = "aicodingtool-mobile-v1"

// MobileInstanceHeader names which running copy of the app answered the probe. The body only says
// "this app", which a dev build and the installed app beside it both say, so this header is what
// tells this process's server from the other's when both sit behind the same Tailscale name.
const MobileInstanceHeader = "x-aicodingtool-instance"

// MobileInstance is this process's own instance, sent in MobileInstanceHeader.
var MobileInstance = application.NewID()

func LoopbackAddress(port int) *domain.MobileAddress {
	return &domain.MobileAddress{Kind: domain.MobileAddressKindLoopback, Host: BindHost, Port: float64(port)}
}

// TailscaleAddress: Serve terminates TLS on 443 and proxies to the local port, so the name is all a phone needs.
func TailscaleAddress(magicDNSName string) *domain.MobileAddress {
	return &domain.MobileAddress{Kind: domain.MobileAddressKindTailscaleHTTPS, Host: magicDNSName, Port: 443}
}

// ReachableAddresses is every way a phone can reach the running server. Loopback is always there
// because it is the bind; the tailnet name is there only while Tailscale is serving it.
func ReachableAddresses(port int, magicDNSName *string) []*domain.MobileAddress {
	addresses := []*domain.MobileAddress{}
	if magicDNSName != nil && *magicDNSName != "" {
		addresses = append(addresses, TailscaleAddress(*magicDNSName))
	}
	return append(addresses, LoopbackAddress(port))
}

// AllowedOrigins are the origins a browser may open a socket from: the addresses the server itself
// answers on. `localhost` is spelled out beside the loopback address because a browser resolves it to both.
func AllowedOrigins(addresses []*domain.MobileAddress) []string {
	origins := []string{}
	add := func(origin string) {
		for _, held := range origins {
			if held == origin {
				return
			}
		}
		origins = append(origins, origin)
	}
	for _, address := range addresses {
		add(domain.AddressOrigin(*address))
		if address.Kind == domain.MobileAddressKindLoopback {
			local := *address
			local.Host = "localhost"
			add(domain.AddressOrigin(local))
		}
	}
	return origins
}
