package domain

import (
	"regexp"
	"strconv"
	"strings"
)

const MaxComputerName = 128

// AwayStatus is "reconnecting" or "offline".
func AwayStatus(status ComputerStatus) string {
	if status == ComputerStatusConnecting {
		return "reconnecting"
	}
	return "offline"
}

func OfflineMessage(link *ComputerLink) string {
	away := link.Name + " is " + AwayStatus(link.Status) + "."
	if link.Error != nil && *link.Error != "" {
		return away + " " + *link.Error
	}
	return away
}

// ComputerAddress is the address as dialled, or false when it is not a host with an optional port.
func ComputerAddress(text string) (string, bool) {
	address := strings.TrimRight(computerSchemePattern.ReplaceAllString(jsTrim(text), ""), "/")
	match := computerAddressPattern.FindStringSubmatch(address)
	if match == nil {
		return "", false
	}
	if match[1] == "" {
		return address, true
	}
	if port, _ := strconv.Atoi(match[1]); port > 0 && port <= 65535 {
		return address, true
	}
	return "", false
}

var computerSchemePattern = regexp.MustCompile(`(?i)^(?:https?|wss?)://`)

var computerAddressPattern = regexp.MustCompile(`(?i)^(?:[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*|\[[0-9a-f:.]+\])(?::(\d{1,5}))?$`)
