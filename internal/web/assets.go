package web

import "embed"

// StaticFS holds the compiled CSS and vendored JavaScript served under /static.
// app.css is produced by the Tailwind CLI (see the Makefile target "css").
//
//go:embed assets/static
var StaticFS embed.FS
