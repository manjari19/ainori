package matching

import (
	"sort"
	"time"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/geo"
)

// Reason says why a driver was skipped or why a request went unmatched.
type Reason string

const (
	ReasonNoSeats      Reason = "no_seats"
	ReasonLate         Reason = "late_for_class"
	ReasonPreference   Reason = "preference"
	ReasonNoDriverNear Reason = "no_driver_nearby"
)

// MinArrivalBuffer is how long before class the rider must be on campus.
const MinArrivalBuffer = 10 * time.Minute

// Candidate is a driver who passed every hard filter, with the numbers behind the score.
type Candidate struct {
	Driver     *pb.Driver
	DetourMin  float64
	PickupKm   float64
	SlackMin   float64
	EstArrival time.Time
	Score      float64
}

func womanOrNB(g pb.Gender) bool {
	return g == pb.Gender_GENDER_WOMAN || g == pb.Gender_GENDER_NONBINARY
}

// Evaluate applies the hard filters and, if the driver passes, scores them (lower is better).
// A non-empty Reason means the driver was skipped.
func Evaluate(req Request, d *pb.Driver, now time.Time) (Candidate, Reason) {
	if d.SeatsLeft <= 0 {
		return Candidate{}, ReasonNoSeats
	}
	// Both sides are respected: the rider's setting and the driver's setting.
	if req.WomenNBOnly && !womanOrNB(d.Gender) {
		return Candidate{}, ReasonPreference
	}
	if d.WomenNbOnly && !womanOrNB(req.Gender) {
		return Candidate{}, ReasonPreference
	}

	driver := geo.LatLng{Lat: d.Lat, Lng: d.Lng}
	rider := geo.LatLng{Lat: req.Lat, Lng: req.Lng}
	toRider := geo.TravelMin(driver, rider)
	toCampus := geo.TravelMin(rider, geo.Campus)
	arrival := now.Add(time.Duration((toRider + toCampus) * float64(time.Minute)))
	slack := req.ClassStart.Sub(arrival)
	if slack < MinArrivalBuffer {
		return Candidate{}, ReasonLate
	}

	detour := geo.DetourMin(driver, rider)
	pickup := geo.Haversine(driver, rider)
	slackMin := slack.Minutes()
	early := slackMin - 30
	if early < 0 {
		early = 0
	}
	return Candidate{
		Driver:     d,
		DetourMin:  detour,
		PickupKm:   pickup,
		SlackMin:   slackMin,
		EstArrival: arrival,
		Score:      1.0*detour + 0.5*pickup + 0.2*early,
	}, ""
}

// Rank filters and scores all drivers, best first, and counts why the others were skipped.
func Rank(req Request, drivers []*pb.Driver, now time.Time) ([]Candidate, map[Reason]int) {
	skipped := map[Reason]int{}
	var out []Candidate
	for _, d := range drivers {
		c, reason := Evaluate(req, d, now)
		if reason != "" {
			skipped[reason]++
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Score < out[j].Score })
	return out, skipped
}

// DominantReason picks the label for an unmatched request: the most common skip reason,
// with ties broken in a fixed order so results are stable.
func DominantReason(skipped map[Reason]int) Reason {
	best, bestN := ReasonNoDriverNear, 0
	for _, r := range []Reason{ReasonNoSeats, ReasonLate, ReasonPreference} {
		if skipped[r] > bestN {
			best, bestN = r, skipped[r]
		}
	}
	return best
}
