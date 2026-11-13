// Package web is the panel's front end: index.html, style.css and the ES
// modules it loads, embedded so the binary serves them under /panel/.
package web

import "embed"

// FS is the web root, with index.html at the top.
//
//go:embed index.html style.css app.js lib views
var FS embed.FS
