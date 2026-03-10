package ui

import "embed"

//go:embed templates/*.html
var Templates embed.FS

//go:embed dist/output.css
//go:embed src/app.js
var Static embed.FS
