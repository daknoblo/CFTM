package demo

import (
	"strings"
	"testing"
)

func TestRewriteMovesRootURLsUnderTheBasePath(t *testing.T) {
	in := `<a href="/ingress">x</a><a href="/">home</a>` +
		`<link href="/static/app.css?v=abc"/><script src="/static/app.js?v=abc"></script>` +
		`<a href="https://example.com/keep">external</a>`

	got := rewrite(in, "/CFTM")

	for _, want := range []string{
		`href="/CFTM/ingress/"`,
		`href="/CFTM/"`,
		`href="/CFTM/static/app.css?v=abc"`,
		`src="/CFTM/static/app.js?v=abc"`,
		`href="https://example.com/keep"`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("rewrite() is missing %s\ngot: %s", want, got)
		}
	}
}

func TestRewriteWithoutABasePath(t *testing.T) {
	got := rewrite(`<a href="/audit">a</a>`, "")
	if want := `href="/audit/"`; !strings.Contains(got, want) {
		t.Errorf("rewrite() = %s, want it to contain %s", got, want)
	}
}

func TestCheckLinksRejectsAPageNobodyExported(t *testing.T) {
	written := map[string]bool{"/CFTM/": true, "/CFTM/audit/": true}
	bodies := map[string]string{
		"/": `<a href="/CFTM/audit/">audit</a><a href="/CFTM/notifications/">notifications</a>`,
	}

	err := checkLinks(bodies, written, "/CFTM")
	if err == nil {
		t.Fatal("checkLinks() = nil, want an error for the unexported page")
	}
	if !strings.Contains(err.Error(), "notifications") {
		t.Errorf("error %q should name the missing page", err)
	}
}

func TestCheckLinksAcceptsACompleteSite(t *testing.T) {
	written := map[string]bool{"/CFTM/": true, "/CFTM/audit/": true}
	bodies := map[string]string{
		// Static assets and external links are not pages and must not trip it.
		"/": `<a href="/CFTM/audit/">a</a><link href="/CFTM/static/app.css"/>` +
			`<a href="https://example.com">x</a><a href="/CFTM/">home</a>`,
	}

	if err := checkLinks(bodies, written, "/CFTM"); err != nil {
		t.Errorf("checkLinks() = %v, want nil", err)
	}
}

func TestRewriteNeutralizesTheInteractiveParts(t *testing.T) {
	in := `<script src="/static/htmx.min.js?v=1" defer></script>` +
		`<form method="post" action="/findings/ignore"><button>Mute</button></form>` +
		`<p>kept</p>`

	got := rewrite(in, "/CFTM")

	if strings.Contains(got, "htmx.min.js") {
		t.Error("the htmx bundle must not survive the export, nothing can serve its requests")
	}
	if strings.Contains(got, "<form") || strings.Contains(got, "Mute") {
		t.Errorf("forms must be replaced, got: %s", got)
	}
	if !strings.Contains(got, "<p>kept</p>") {
		t.Errorf("surrounding markup must survive, got: %s", got)
	}
}
