package web

// healthyTone colors the healthy counter red as soon as one tunnel is not.
func healthyTone(t Totals) string {
	if t.Tunnels > 0 && t.Healthy < t.Tunnels {
		return "text-rose-300"
	}
	return "text-emerald-300"
}

// findingsTone keeps the finding counter neutral while there is nothing to do.
func findingsTone(t Totals) string {
	if t.Findings > 0 {
		return "text-amber-300"
	}
	return "text-slate-100"
}

// probeLabel renders a probe class, falling back to a dash when never checked.
func probeLabel(p Probe) string {
	if !p.Known || p.Class == "" {
		return "—"
	}
	return p.Class
}

// accessLabel renders the Access protection state of a hostname.
func accessLabel(a AccessState) string {
	switch {
	case !a.Known:
		return "unknown"
	case a.Protected && a.HasBypass:
		return "bypass policy"
	case a.Protected && a.HasToken:
		return "token allowed"
	case a.Protected:
		return "protected"
	default:
		return "public"
	}
}

// accessBadgeClass colors the Access state.
func accessBadgeClass(a AccessState) string {
	switch {
	case !a.Known:
		return "badge"
	case !a.Protected:
		return "badge badge-error"
	case a.HasBypass:
		return "badge badge-warn"
	default:
		return "badge badge-ok"
	}
}

// tokenBadgeClass colors a service token by remaining lifetime.
func tokenBadgeClass(t ServiceToken) string {
	switch t.Severity {
	case "critical":
		return "badge badge-error"
	case "warning":
		return "badge badge-warn"
	default:
		return "badge badge-ok"
	}
}

// eventBadgeClass colors an event by its derived severity.
func eventBadgeClass(e Event) string {
	switch e.Severity {
	case "critical":
		return "badge badge-error"
	case "warning":
		return "badge badge-warn"
	default:
		return "badge badge-muted"
	}
}

// logLevelClass colors a buffered log line.
func logLevelClass(level string) string {
	switch level {
	case "ERROR":
		return "text-rose-300"
	case "WARN":
		return "text-amber-300"
	case "DEBUG":
		return "text-slate-500"
	default:
		return "text-slate-400"
	}
}

// featureLabel names a feature state for the About page.
func featureLabel(state string) string {
	switch state {
	case "on":
		return "active"
	case "unconfigured":
		return "incomplete"
	default:
		return "off"
	}
}

// featureBadgeClass colors a feature state, calling out the ones that are
// switched on but missing a credential.
func featureBadgeClass(state string) string {
	switch state {
	case "on":
		return "badge badge-ok"
	case "unconfigured":
		return "badge badge-warn"
	default:
		return "badge badge-muted"
	}
}
