package prober

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Probe result classes, ordered from best to worst outcome.
const (
	// ClassOK means the origin itself answered, so the whole path works.
	ClassOK = "ok"
	// ClassAccessChallenge means Cloudflare Access intercepted at the edge; the
	// tunnel was never traversed. Expected when no service token is configured.
	ClassAccessChallenge = "access_challenge"
	// ClassAccessDenied means the service token is not authorized for this app.
	ClassAccessDenied = "access_denied"
	// ClassEdgeCached is a cached response, not evidence of tunnel traversal.
	ClassEdgeCached = "edge_cached"
	// ClassTunnelDown means no connector is available (Cloudflare error 1033).
	ClassTunnelDown = "tunnel_down"
	// ClassOriginError means the tunnel is up but the origin did not answer.
	ClassOriginError = "origin_error"

	ClassTimeout   = "timeout"
	ClassDNSError  = "dns_error"
	ClassTLSError  = "tls_error"
	ClassHTTPError = "http_error"
)

// accessDomain is the hostname suffix Cloudflare Access redirects to.
const accessDomain = ".cloudflareaccess.com"

// Cloudflare edge status codes that describe a tunnel or origin failure rather
// than an application response.
var edgeFailureStatus = map[int]string{
	http.StatusBadGateway:     ClassOriginError, // 502
	http.StatusGatewayTimeout: ClassOriginError, // 504
	520:                       ClassOriginError,
	521:                       ClassOriginError,
	522:                       ClassOriginError,
	523:                       ClassOriginError,
	524:                       ClassOriginError,
	530:                       ClassTunnelDown,
}

// Classify maps a response onto a probe class. The body is only inspected for
// Cloudflare error codes and is expected to be truncated by the caller.
func Classify(resp *http.Response, body []byte) string {
	if class, ok := edgeFailureStatus[resp.StatusCode]; ok {
		if hasCloudflareErrorCode(body, "1033") {
			return ClassTunnelDown
		}
		return class
	}

	if isAccessRedirect(resp) {
		return ClassAccessChallenge
	}

	switch resp.StatusCode {
	case http.StatusUnauthorized:
		if hasAccessMarkers(resp, body) {
			return ClassAccessChallenge
		}
	case http.StatusForbidden:
		if hasAccessMarkers(resp, body) {
			return ClassAccessDenied
		}
	}

	switch strings.ToUpper(strings.TrimSpace(resp.Header.Get("CF-Cache-Status"))) {
	case "HIT", "STALE", "UPDATING", "REVALIDATED":
		return ClassEdgeCached
	}

	// Any other status came from the origin, so the tunnel is working. A 404 or
	// a 500 is the application's business, not the monitor's.
	return ClassOK
}

// ClassifyError maps a transport failure onto a probe class.
func ClassifyError(err error) string {
	if err == nil {
		return ClassOK
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return ClassDNSError
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return ClassTimeout
	}

	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return ClassTimeout
	}

	if strings.Contains(err.Error(), "tls:") || strings.Contains(err.Error(), "certificate") {
		return ClassTLSError
	}

	return ClassHTTPError
}

// IsHealthy reports whether a class means the origin was reached end to end.
func IsHealthy(class string) bool { return class == ClassOK }

// IsFailure reports whether a class indicates a broken path rather than a
// missing authorization.
func IsFailure(class string) bool {
	switch class {
	case ClassTunnelDown, ClassOriginError, ClassTimeout, ClassDNSError, ClassTLSError, ClassHTTPError:
		return true
	default:
		return false
	}
}

func isAccessRedirect(resp *http.Response) bool {
	if resp.StatusCode < 300 || resp.StatusCode > 399 {
		return false
	}
	location := resp.Header.Get("Location")
	if location == "" {
		return false
	}
	target, err := url.Parse(location)
	if err != nil {
		return false
	}
	return strings.HasSuffix(strings.ToLower(target.Hostname()), accessDomain)
}

func hasAccessMarkers(resp *http.Response, body []byte) bool {
	for key := range resp.Header {
		if strings.HasPrefix(strings.ToLower(key), "cf-access") {
			return true
		}
	}
	return strings.Contains(strings.ToLower(string(body)), "cloudflareaccess.com")
}

// hasCloudflareErrorCode looks for the "error code: 1033" marker Cloudflare
// renders on its interstitial pages.
func hasCloudflareErrorCode(body []byte, code string) bool {
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "error code: "+code) || strings.Contains(lower, "error "+code)
}
