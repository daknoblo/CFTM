package demo

import (
	"fmt"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/daknoblo/CFTM/internal/web"
)

// pages are the routes the exporter walks. Partials and the JSON API are left
// out: without a server there is nothing to poll.
var pages = []string{"/", "/ingress", "/audit", "/events", "/logs", "/about"}

// Export renders the whole UI to dir as a static site. basePath is the prefix
// the site is served under, "/CFTM" for a GitHub Pages project site.
func Export(handler http.Handler, tunnelIDs []string, dir, basePath string) error {
	basePath = "/" + strings.Trim(basePath, "/")
	if basePath == "/" {
		basePath = ""
	}

	routes := append([]string{}, pages...)
	for _, id := range tunnelIDs {
		routes = append(routes, "/tunnels/"+id)
	}

	for _, route := range routes {
		body, err := render(handler, route)
		if err != nil {
			return err
		}
		if err := writePage(dir, route, rewrite(body, basePath)); err != nil {
			return err
		}
	}

	return copyStatic(dir)
}

// render runs one request through the real handler, in process.
func render(handler http.Handler, route string) (string, error) {
	req := httptest.NewRequest(http.MethodGet, route, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		return "", fmt.Errorf("demo: GET %s returned %d", route, rec.Code)
	}
	return rec.Body.String(), nil
}

// writePage stores a route as an index.html so a static host can serve it under
// the same path the application uses.
func writePage(dir, route, body string) error {
	target := filepath.Join(dir, filepath.FromSlash(strings.Trim(route, "/")), "index.html")
	if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
		return err
	}
	return os.WriteFile(target, []byte(body), 0o600)
}

// Matches every root-absolute URL the templates emit. External links start with
// a scheme and are left alone.
var rootURL = regexp.MustCompile(`(href|src|action)="/([^"]*)"`)

// Our forms are flat, so a non-greedy match is enough to swallow one whole.
var formBlock = regexp.MustCompile(`(?s)<form[^>]*>.*?</form>`)

// The htmx bundle is dropped rather than shipped: without a server every
// hx-get would just log a 404 every ten seconds.
var htmxScript = regexp.MustCompile(`<script src="/static/htmx\.min\.js[^"]*"[^>]*></script>`)

func rewrite(body, basePath string) string {
	body = htmxScript.ReplaceAllString(body, "")
	body = formBlock.ReplaceAllString(body,
		`<span class="badge badge-muted" title="Actions are disabled in the demo">demo</span>`)

	body = rootURL.ReplaceAllStringFunc(body, func(m string) string {
		parts := rootURL.FindStringSubmatch(m)
		attr, rest := parts[1], parts[2]
		// Directory-style links so index.html resolves without a rewrite rule.
		if !strings.HasPrefix(rest, "static/") && rest != "" && !strings.Contains(rest, "?") {
			rest += "/"
		}
		return attr + `="` + basePath + "/" + rest + `"`
	})

	return body
}

// copyStatic writes the embedded assets next to the pages.
func copyStatic(dir string) error {
	assets, err := fs.Sub(web.StaticFS, "assets/static")
	if err != nil {
		return err
	}

	return fs.WalkDir(assets, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		// htmx is not shipped because the export strips its attributes' effect.
		if path.Base(name) == "htmx.min.js" {
			return nil
		}
		body, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		target := filepath.Join(dir, "static", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o600)
	})
}
