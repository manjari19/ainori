package matching

import (
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	pb "ainori/gen/ainori/v1"
	"ainori/internal/geo"
)

var (
	t0    = time.Date(2026, 10, 29, 8, 0, 0, 0, time.UTC)
	rider = geo.LatLng{Lat: 49.2400, Lng: -123.0000}
)

func req(classIn time.Duration) Request {
	return Request{ID: "r", RiderID: "rider", Lat: rider.Lat, Lng: rider.Lng,
		ClassStart: t0.Add(classIn), Gender: pb.Gender_GENDER_WOMAN}
}

func driver(id string, lat, lng float64, seats int32) *pb.Driver {
	return &pb.Driver{Id: id, Lat: lat, Lng: lng, SeatsLeft: seats, Gender: pb.Gender_GENDER_MAN}
}

// with returns a modified copy; protobuf messages must not be copied by value.
func with(d *pb.Driver, fn func(*pb.Driver)) *pb.Driver {
	c := proto.Clone(d).(*pb.Driver)
	fn(c)
	return c
}

func TestEvaluateFilters(t *testing.T) {
	near := driver("d", 49.2380, -123.0020, 2)
	tests := []struct {
		name   string
		req    Request
		driver *pb.Driver
		want   Reason
	}{
		{"ok", req(2 * time.Hour), near, ""},
		{"full car", req(2 * time.Hour), with(near, func(d *pb.Driver) { d.SeatsLeft = 0 }), ReasonNoSeats},
		{"rider wants women/nb only, driver is a man", func() Request { r := req(2 * time.Hour); r.WomenNBOnly = true; return r }(), near, ReasonPreference},
		{"driver is women/nb only, rider is a man", func() Request { r := req(2 * time.Hour); r.Gender = pb.Gender_GENDER_MAN; return r }(),
			with(near, func(d *pb.Driver) { d.WomenNbOnly = true; d.Gender = pb.Gender_GENDER_WOMAN }), ReasonPreference},
		{"both women/nb only and match", func() Request { r := req(2 * time.Hour); r.WomenNBOnly = true; return r }(),
			with(near, func(d *pb.Driver) { d.WomenNbOnly = true; d.Gender = pb.Gender_GENDER_NONBINARY }), ""},
		{"class too soon", req(15 * time.Minute), near, ReasonLate},
		{"exactly at the 10 minute buffer is rejected only if under", req(5 * time.Minute), near, ReasonLate},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, got := Evaluate(tc.req, tc.driver, t0)
			if got != tc.want {
				t.Fatalf("reason = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRankPrefersLowerDetour(t *testing.T) {
	// "onroute" sits between the rider and campus-ish side; "backwards" is behind the rider.
	onroute := driver("onroute", 49.2300, -123.0100, 2)
	backwards := driver("backwards", 49.2000, -123.1000, 2)
	ranked, skipped := Rank(req(3*time.Hour), []*pb.Driver{backwards, onroute}, t0)
	if len(skipped) != 0 || len(ranked) != 2 {
		t.Fatalf("ranked %d skipped %v", len(ranked), skipped)
	}
	if ranked[0].Driver.Id != "onroute" {
		t.Fatalf("best = %s, want onroute (scores %.2f vs %.2f)", ranked[0].Driver.Id, ranked[0].Score, ranked[1].Score)
	}
}

func TestRankCountsSkipReasons(t *testing.T) {
	full := driver("full", 49.2380, -123.0020, 0)
	ok := driver("ok", 49.2380, -123.0020, 1)
	_, skipped := Rank(req(2*time.Hour), []*pb.Driver{full, full, ok}, t0)
	if skipped[ReasonNoSeats] != 2 {
		t.Fatalf("skipped = %v, want 2 no_seats", skipped)
	}
}

func TestEarlyPenaltyHurtsVeryEarlyArrival(t *testing.T) {
	d := driver("d", 49.2380, -123.0020, 2)
	soon, _ := Evaluate(req(60*time.Minute), d, t0)
	late, _ := Evaluate(req(5*time.Hour), d, t0)
	if late.Score <= soon.Score {
		t.Fatalf("arriving hours early should score worse: %.2f vs %.2f", late.Score, soon.Score)
	}
}

func TestDominantReason(t *testing.T) {
	if got := DominantReason(map[Reason]int{}); got != ReasonNoDriverNear {
		t.Fatalf("empty = %s", got)
	}
	if got := DominantReason(map[Reason]int{ReasonLate: 3, ReasonNoSeats: 1}); got != ReasonLate {
		t.Fatalf("got %s want late", got)
	}
}
