// Package web embeds the built Vue SPA. Run `npm run build` in web/ first;
// the committed placeholder in dist/ keeps `go build` working from a clean
// checkout.
package web

import "embed"

//go:embed all:dist
var Dist embed.FS
