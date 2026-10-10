package gui

import (
	"encoding/base64"
	"strings"

	_ "embed"
)

//go:embed assets/index.html
var indexHTML string

//go:embed assets/style.css
var styleCSS string

//go:embed assets/app.js
var appJS string

//go:embed assets/logo.svg
var logoSVG []byte

//go:embed assets/favicon.ico
var faviconICO []byte

func pageHTML() string {
	// The page runs from a temporary file, so assets must be self-contained.
	html := strings.ReplaceAll(indexHTML, "{{LOGO}}", "data:image/svg+xml;base64,"+base64.StdEncoding.EncodeToString(logoSVG))
	html = strings.ReplaceAll(html, "{{FAVICON}}", "data:image/x-icon;base64,"+base64.StdEncoding.EncodeToString(faviconICO))
	html = strings.Replace(html, "/*{{STYLE}}*/", styleCSS, 1)
	html = strings.Replace(html, "/*{{SCRIPT}}*/", appJS, 1)
	return html
}
