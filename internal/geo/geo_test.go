package geo

import (
	"math"
	"testing"
)

func TestHaversine(t *testing.T) {
	// Metrotown to SFU is roughly 8 km as the crow flies.
	d := Haversine(LatLng{49.2276, -123.0076}, Campus)
	if d < 7 || d > 10 {
		t.Fatalf("metrotown to sfu = %.2f km, want about 8", d)
	}
	if Haversine(Campus, Campus) != 0 {
		t.Fatal("distance to self should be 0")
	}
}

func TestDetourNeverNegativeOnTheWay(t *testing.T) {
	driver := LatLng{49.2276, -123.0076}
	onWay := LatLng{(driver.Lat + Campus.Lat) / 2, (driver.Lng + Campus.Lng) / 2}
	if d := DetourMin(driver, onWay); math.Abs(d) > 0.01 {
		t.Fatalf("detour for a rider on the straight line = %.4f, want about 0", d)
	}
	far := LatLng{49.30, -123.10}
	if DetourMin(driver, far) <= 0 {
		t.Fatal("detour for an off-route rider should be positive")
	}
}

func TestRings(t *testing.T) {
	cell, err := CellOf(Campus.Lat, Campus.Lng)
	if err != nil {
		t.Fatal(err)
	}
	rings, err := Rings(cell, 3)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{1, 6, 12, 18}
	if len(rings) != len(want) {
		t.Fatalf("got %d rings, want %d", len(rings), len(want))
	}
	for i, n := range want {
		if len(rings[i]) != n {
			t.Errorf("ring %d has %d cells, want %d", i, len(rings[i]), n)
		}
	}
	if rings[0][0] != cell {
		t.Error("ring 0 should be the origin cell")
	}
	if _, err := Rings("nonsense", 2); err == nil {
		t.Error("expected error for invalid cell")
	}
}
