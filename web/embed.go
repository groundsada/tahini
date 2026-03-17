package web

import "embed"

//go:embed templates/*.html
var TemplateFS embed.FS
