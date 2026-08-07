package web

import "testing"

func TestHostnameHref(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"app.example.com", "https://app.example.com"},
		{"a-b.sub.example.co.uk", "https://a-b.sub.example.co.uk"},
		{"", ""},
		{"*.example.com", ""},
		{"localhost", ""},
		{"javascript:alert(1)", ""},
		{"example.com/path", ""},
		{"exa mple.com", ""},
	}

	for _, tc := range cases {
		if got := HostnameHref(tc.host); got != tc.want {
			t.Errorf("HostnameHref(%q) = %q, want %q", tc.host, got, tc.want)
		}
	}
}
