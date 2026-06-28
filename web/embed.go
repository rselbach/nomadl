package web

import _ "embed"

//go:embed index.html
var IndexHTML []byte

//go:embed htmx.min.js
var HTMXJS []byte
