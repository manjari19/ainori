// Package routes embeds the precomputed GeoJSON driving routes to SFU.
package routes

import "embed"

//go:embed *.geojson
var FS embed.FS
