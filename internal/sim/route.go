// Package sim is the load simulator: it plays hundreds of drivers and riders against a
// running gateway and measures what comes back.
package sim

import (
	"encoding/json"
	"fmt"
	"sort"

	"ainori/data/routes"
	"ainori/internal/geo"
)

// Route is a driving route to campus with cumulative distances, so a driver can be
// placed at "x km along the route".
type Route struct {
	Name string
	Pts  []geo.LatLng
	cum  []float64 // km from the start to each point
}

func (r *Route) Length() float64 { return r.cum[len(r.cum)-1] }

// At returns the position dist km along the route (clamped to the ends).
func (r *Route) At(dist float64) geo.LatLng {
	if dist <= 0 {
		return r.Pts[0]
	}
	if dist >= r.Length() {
		return r.Pts[len(r.Pts)-1]
	}
	i := sort.SearchFloat64s(r.cum, dist)
	segLen := r.cum[i] - r.cum[i-1]
	if segLen == 0 {
		return r.Pts[i]
	}
	f := (dist - r.cum[i-1]) / segLen
	a, b := r.Pts[i-1], r.Pts[i]
	return geo.LatLng{Lat: a.Lat + (b.Lat-a.Lat)*f, Lng: a.Lng + (b.Lng-a.Lng)*f}
}

// LoadRoutes reads the embedded GeoJSON files.
func LoadRoutes() ([]*Route, error) {
	entries, err := routes.FS.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read embedded routes: %w", err)
	}
	var out []*Route
	for _, e := range entries {
		if e.IsDir() || len(e.Name()) < 8 || e.Name()[len(e.Name())-8:] != ".geojson" {
			continue
		}
		b, err := routes.FS.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		var f struct {
			Properties struct {
				Name string `json:"name"`
			} `json:"properties"`
			Geometry struct {
				Coordinates [][2]float64 `json:"coordinates"`
			} `json:"geometry"`
		}
		if err := json.Unmarshal(b, &f); err != nil {
			return nil, fmt.Errorf("parse %s: %w", e.Name(), err)
		}
		if len(f.Geometry.Coordinates) < 2 {
			return nil, fmt.Errorf("%s has fewer than 2 points", e.Name())
		}
		r := &Route{Name: f.Properties.Name}
		for i, c := range f.Geometry.Coordinates {
			p := geo.LatLng{Lat: c[1], Lng: c[0]} // GeoJSON is lng, lat
			r.Pts = append(r.Pts, p)
			if i == 0 {
				r.cum = append(r.cum, 0)
			} else {
				r.cum = append(r.cum, r.cum[i-1]+geo.Haversine(r.Pts[i-1], p))
			}
		}
		out = append(out, r)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no routes embedded")
	}
	return out, nil
}
