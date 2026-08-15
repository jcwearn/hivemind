// Package static holds the browser-side assets, embedded into the binary.
//
// Everything the browser needs ships inside the executable: there is no CDN to
// be unreachable, no asset directory to forget to copy into the image, and
// `docker run` with no volumes is a complete installation.
package static

import "embed"

// FS holds the stylesheet, the client script, the icon, and the vendored
// htmx bundles.
//
//go:embed vendor styles.css app.js icon.svg
var FS embed.FS
