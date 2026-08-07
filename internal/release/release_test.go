package release

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newTestChecker(t *testing.T, h http.HandlerFunc) *Checker {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(WithURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestLatest(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"plain tag", `{"tag_name":"2026.4.0"}`, "2026.4.0"},
		{"v prefix stripped", `{"tag_name":"v2026.4.0"}`, "2026.4.0"},
		{"surrounding whitespace", `{"tag_name":"  2026.4.0 "}`, "2026.4.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestChecker(t, func(w http.ResponseWriter, r *http.Request) {
				if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
					t.Errorf("Accept = %q, want the GitHub media type", got)
				}
				_, _ = w.Write([]byte(tc.body))
			})

			got, err := c.Latest(t.Context())
			if err != nil {
				t.Fatalf("Latest() error = %v, want nil", err)
			}
			if got != tc.want {
				t.Errorf("Latest() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestLatestErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"rate limited", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}},
		{"malformed json", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`not json`))
		}},
		{"missing tag", func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{}`))
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newTestChecker(t, tc.handler)
			if _, err := c.Latest(t.Context()); err == nil {
				t.Error("Latest() error = nil, want an error")
			}
		})
	}
}
