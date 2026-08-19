package collector

import (
	"github.com/daknoblo/CFTM/internal/prober"
	"github.com/daknoblo/CFTM/internal/store"
)

// EventSeverity ranks an event for display and for deciding whether it is worth
// notifying about. It lives here because the collector owns both the event
// kinds and the severity scale.
func EventSeverity(e store.Event) Severity {
	switch e.Kind {
	case store.EventPollFailed, store.EventTunnelRemoved:
		return SeverityCritical
	case store.EventTunnelStatus:
		// A tunnel coming back is good news, not an incident.
		if e.ToState == "healthy" {
			return SeverityInfo
		}
		return SeverityCritical
	case store.EventConnectorRemoved:
		return SeverityWarning
	case store.EventProbe:
		if prober.IsFailure(e.ToState) {
			return SeverityWarning
		}
		return SeverityInfo
	default:
		return SeverityInfo
	}
}

// EventLabel is the short badge text for an event kind.
func EventLabel(kind string) string {
	switch kind {
	case store.EventTunnelStatus:
		return "status"
	case store.EventTunnelAdded:
		return "tunnel added"
	case store.EventTunnelRemoved:
		return "tunnel removed"
	case store.EventConnectorAdded:
		return "connector up"
	case store.EventConnectorRemoved:
		return "connector down"
	case store.EventConfigChanged:
		return "config"
	case store.EventVersionDrift:
		return "version"
	case store.EventProbe:
		return "probe"
	case store.EventAccessCoverage:
		return "access"
	case store.EventServiceTokenAging:
		return "service token"
	case store.EventOriginCountry:
		return "origin"
	case store.EventPollFailed:
		return "poll failed"
	default:
		return kind
	}
}
