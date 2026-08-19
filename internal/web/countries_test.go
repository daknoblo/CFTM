package web

import "testing"

func TestCountryName(t *testing.T) {
	cases := map[string]string{
		"DE":   "Germany",
		"de":   "Germany",
		" us ": "United States",
		"GB":   "United Kingdom",
		// Cloudflare reports these where it could not place the address.
		"XX": "Unknown origin",
		"T1": "Unknown origin",
		"":   "Unknown",
		// An unlisted or newly assigned code still has to render.
		"ZZ": "ZZ",
	}
	for in, want := range cases {
		if got := CountryName(in); got != want {
			t.Errorf("CountryName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestOriginBadgeClass(t *testing.T) {
	if got := originBadgeClass(OriginCountry{Requests: 5}); got != "badge badge-muted" {
		t.Errorf("originBadgeClass() = %q, want the muted badge without refusals", got)
	}
	if got := originBadgeClass(OriginCountry{Requests: 5, Denied: 1}); got != "badge badge-warn" {
		t.Errorf("originBadgeClass() = %q, want the warning badge once requests were refused", got)
	}
}

func TestOriginCodesCoversNameAndCode(t *testing.T) {
	got := originCodes([]OriginCountry{
		{Code: "DE", Name: "Germany"},
		{Code: "US", Name: "United States"},
	})
	want := "DE Germany US United States"
	if got != want {
		t.Errorf("originCodes() = %q, want %q", got, want)
	}
}
