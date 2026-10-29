// Package cost splits the gas bill for a trip.
package cost

import "math"

// PerPerson is the share each person pays. The driver pays a share too, so the bill
// is split across ridersOnTrip + 1 people. The result is rounded to cents.
func PerPerson(distanceKm, fuelLPer100Km, gasPricePerL float64, ridersOnTrip int) float64 {
	if ridersOnTrip < 0 {
		ridersOnTrip = 0
	}
	litres := distanceKm * fuelLPer100Km / 100
	total := litres * gasPricePerL
	return math.Round(total/float64(ridersOnTrip+1)*100) / 100
}
