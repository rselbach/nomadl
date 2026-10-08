// Package web embeds the browser UI.
package web

import "embed"

// Files holds the UI: index.html, the app's ES modules and stylesheet,
// and the vendored Preact and htm modules.
//
//go:embed index.html app.css app.js lib ui vendor/*.mjs
var Files embed.FS
