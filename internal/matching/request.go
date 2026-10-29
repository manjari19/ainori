// Package matching turns ride requests into trips: find nearby drivers, filter, score,
// claim a seat atomically, persist, and announce the result.
package matching

import (
	"fmt"
	"strconv"
	"time"

	pb "ainori/gen/ainori/v1"
)

const (
	StreamRequests = "stream:ride_requests"
	StreamMatches  = "stream:matches"
)

// Request is a rider asking for a seat.
type Request struct {
	ID          string
	RiderID     string
	Lat, Lng    float64
	ClassStart  time.Time
	Gender      pb.Gender
	WomenNBOnly bool
	CreatedAt   time.Time
}

// Fields encodes the request as flat stream fields.
func (r Request) Fields() map[string]string {
	return map[string]string{
		"request_id":       r.ID,
		"rider_id":         r.RiderID,
		"lat":              strconv.FormatFloat(r.Lat, 'f', 6, 64),
		"lng":              strconv.FormatFloat(r.Lng, 'f', 6, 64),
		"class_start_unix": strconv.FormatInt(r.ClassStart.Unix(), 10),
		"gender":           GenderName(r.Gender),
		"women_nb_only":    boolStr(r.WomenNBOnly),
		"created_unix_ms":  strconv.FormatInt(r.CreatedAt.UnixMilli(), 10),
	}
}

func ParseRequest(f map[string]string) (Request, error) {
	var r Request
	var err error
	r.ID, r.RiderID = f["request_id"], f["rider_id"]
	if r.ID == "" || r.RiderID == "" {
		return r, fmt.Errorf("request is missing request_id or rider_id")
	}
	if r.Lat, err = strconv.ParseFloat(f["lat"], 64); err != nil {
		return r, fmt.Errorf("bad lat: %w", err)
	}
	if r.Lng, err = strconv.ParseFloat(f["lng"], 64); err != nil {
		return r, fmt.Errorf("bad lng: %w", err)
	}
	cs, err := strconv.ParseInt(f["class_start_unix"], 10, 64)
	if err != nil {
		return r, fmt.Errorf("bad class_start_unix: %w", err)
	}
	r.ClassStart = time.Unix(cs, 0)
	r.Gender = ParseGender(f["gender"])
	r.WomenNBOnly = f["women_nb_only"] == "1"
	if ms, err := strconv.ParseInt(f["created_unix_ms"], 10, 64); err == nil {
		r.CreatedAt = time.UnixMilli(ms)
	}
	return r, nil
}

func ParseGender(s string) pb.Gender {
	switch s {
	case "woman":
		return pb.Gender_GENDER_WOMAN
	case "man":
		return pb.Gender_GENDER_MAN
	case "nonbinary":
		return pb.Gender_GENDER_NONBINARY
	}
	return pb.Gender_GENDER_UNSPECIFIED
}

func GenderName(g pb.Gender) string {
	switch g {
	case pb.Gender_GENDER_WOMAN:
		return "woman"
	case pb.Gender_GENDER_MAN:
		return "man"
	case pb.Gender_GENDER_NONBINARY:
		return "nonbinary"
	}
	return ""
}

func boolStr(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
