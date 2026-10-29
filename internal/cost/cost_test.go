package cost

import "testing"

func TestPerPerson(t *testing.T) {
	tests := []struct {
		name   string
		km     float64
		fuel   float64
		price  float64
		riders int
		want   float64
	}{
		{"single rider splits with driver", 20, 8, 2.0, 1, 1.60},
		{"full car of three riders", 20, 8, 2.0, 3, 0.80},
		{"rounds to cents", 10, 7.3, 1.85, 2, 0.45},
		{"no riders means driver pays all", 20, 8, 2.0, 0, 3.20},
		{"negative riders treated as zero", 20, 8, 2.0, -1, 3.20},
		{"zero distance", 0, 8, 2.0, 2, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := PerPerson(tc.km, tc.fuel, tc.price, tc.riders); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
