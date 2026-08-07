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

func TestProbeSummaryLabel(t *testing.T) {
	cases := []struct {
		name string
		in   Probes
		want string
	}{
		{"nothing checked", Probes{Total: 5}, "not checked yet"},
		{"all ok", Probes{Total: 5, Checked: 5, OK: 5}, "5 / 5 ok"},
		{"one failing", Probes{Total: 5, Checked: 5, OK: 4}, "4 / 5 ok"},
		{"partly checked", Probes{Total: 5, Checked: 3, OK: 3}, "3 / 3 ok, 2 pending"},
	}

	for _, tc := range cases {
		if got := ProbeSummaryLabel(tc.in); got != tc.want {
			t.Errorf("%s: ProbeSummaryLabel() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestProbeSummaryToneFlagsAnyFailure(t *testing.T) {
	if got, want := probeSummaryTone(Probes{Total: 5, Checked: 5, OK: 5}), "text-emerald-300"; got != want {
		t.Errorf("all ok tone = %q, want %q", got, want)
	}
	if got, want := probeSummaryTone(Probes{Total: 5, Checked: 5, OK: 4}), "text-rose-300"; got != want {
		t.Errorf("one failing tone = %q, want %q", got, want)
	}
	// Unchecked hostnames must not turn the counter red on their own.
	if got, want := probeSummaryTone(Probes{Total: 5, Checked: 2, OK: 2}), "text-emerald-300"; got != want {
		t.Errorf("partly checked tone = %q, want %q", got, want)
	}
}
