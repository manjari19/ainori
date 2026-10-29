// Package geo holds the small geometry helpers shared by the services.
package geo

import (
	"fmt"
	"math"

	"github.com/uber/h3-go/v4"
)

// Res is the H3 resolution used everywhere (hexagons about 0.5 km per edge).
const Res = 8

const (
	earthRadiusKm = 6371.0
	roadFactor    = 1.3 // straight line to road distance, a deliberate simplification
	speedKmh      = 40.0
)

// Campus is SFU Burnaby, where every trip ends.
var Campus = LatLng{Lat: 49.2781, Lng: -122.9199}

type LatLng struct{ Lat, Lng float64 }

// CellOf returns the H3 cell (as its hex string) containing the point.
func CellOf(lat, lng float64) (string, error) {
	c, err := h3.LatLngToCell(h3.NewLatLng(lat, lng), Res)
	if err != nil {
		return "", fmt.Errorf("lat/lng to cell: %w", err)
	}
	return c.String(), nil
}

// Rings returns the cells around a cell grouped by distance: Rings[0] is the cell
// itself, Rings[1] the six neighbours, and so on up to maxK.
func Rings(cell string, maxK int) ([][]string, error) {
	origin := h3.Cell(h3.IndexFromString(cell))
	if !origin.IsValid() {
		return nil, fmt.Errorf("invalid h3 cell %q", cell)
	}
	rings, err := h3.GridDiskDistances(origin, maxK)
	if err != nil {
		return nil, fmt.Errorf("grid disk: %w", err)
	}
	out := make([][]string, len(rings))
	for i, ring := range rings {
		for _, c := range ring {
			out[i] = append(out[i], c.String())
		}
	}
	return out, nil
}

// Haversine is the great-circle distance in km.
func Haversine(a, b LatLng) float64 {
	rad := math.Pi / 180
	dLat := (b.Lat - a.Lat) * rad
	dLng := (b.Lng - a.Lng) * rad
	h := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(a.Lat*rad)*math.Cos(b.Lat*rad)*math.Sin(dLng/2)*math.Sin(dLng/2)
	return 2 * earthRadiusKm * math.Asin(math.Sqrt(h))
}

// TravelMin estimates driving minutes between two points.
func TravelMin(a, b LatLng) float64 {
	return Haversine(a, b) * roadFactor / speedKmh * 60
}

// DetourMin is the extra minutes a driver spends to pick the rider up on the way to campus.
func DetourMin(driver, rider LatLng) float64 {
	return TravelMin(driver, rider) + TravelMin(rider, Campus) - TravelMin(driver, Campus)
}
