package web

import "embed"

// Files contains the production Vue build. Run `npm run build` in frontend before release.
//
//go:embed all:dist
var Files embed.FS
