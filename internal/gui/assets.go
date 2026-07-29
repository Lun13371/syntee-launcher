package gui

import (
	"strings"

	_ "embed"
)

//go:embed assets/index.html
var indexHTML string

//go:embed assets/style.css
var styleCSS string

//go:embed assets/app.js
var appJS string

func pageHTML() string {
	html := indexHTML
	html = strings.Replace(html, "/*{{STYLE}}*/", styleCSS, 1)
	html = strings.Replace(html, "/*{{SCRIPT}}*/", appJS, 1)
	return html
}
