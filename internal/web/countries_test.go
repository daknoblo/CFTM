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

// The flag is plain text, so it survives a content security policy that
// forbids external sources. Where the font has no glyph the caller shows the
// code instead, which is what the flag stood for.
func TestCountryFlag(t *testing.T) {
	cases := map[string]string{
		"DE":   "\U0001F1E9\U0001F1EA",
		"de":   "\U0001F1E9\U0001F1EA",
		" us ": "\U0001F1FA\U0001F1F8",
		// Nothing stands for an unplaceable origin, so no flag is invented.
		"XX": "",
		"T1": "",
		"":   "",
		// Not an ISO alpha-2 code, so no flag can be built from it.
		"D":   "",
		"DEU": "",
		"D1":  "",
	}
	for in, want := range cases {
		if got := CountryFlag(in); got != want {
			t.Errorf("CountryFlag(%q) = %q, want %q", in, got, want)
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
