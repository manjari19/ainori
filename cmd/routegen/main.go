// routegen fetches driving routes from the public OSRM demo server to SFU and saves them
// as GeoJSON in data/routes. Run it once and commit the output; the simulator reads the files.
// If the server cannot be reached it falls back to straight lines through hand-picked
// waypoints and says so, because those are not real roads.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type start struct {
	Name     string
	Lat, Lng float64
	// Fallback waypoints (lng, lat) used only when OSRM is unreachable.
	Fallback [][2]float64
}

const sfuLng, sfuLat = -122.9199, 49.2781

var starts = []start{
	{"coquitlam-centre", 49.2790, -122.8020, [][2]float64{{-122.8020, 49.2790}, {-122.8600, 49.2800}, {-122.9199, 49.2781}}},
	{"lougheed", 49.2487, -122.8970, [][2]float64{{-122.8970, 49.2487}, {-122.9000, 49.2650}, {-122.9199, 49.2781}}},
	{"metrotown", 49.2276, -123.0076, [][2]float64{{-123.0076, 49.2276}, {-123.0030, 49.2660}, {-122.9500, 49.2803}, {-122.9199, 49.2781}}},
	{"surrey-central", 49.1867, -122.8490, [][2]float64{{-122.8490, 49.1867}, {-122.8900, 49.2300}, {-122.9000, 49.2650}, {-122.9199, 49.2781}}},
	{"commercial-broadway", 49.2625, -123.0695, [][2]float64{{-123.0695, 49.2625}, {-123.0000, 49.2660}, {-122.9500, 49.2803}, {-122.9199, 49.2781}}},
	{"port-moody", 49.2827, -122.8312, [][2]float64{{-122.8312, 49.2827}, {-122.8800, 49.2810}, {-122.9199, 49.2781}}},
	{"new-westminster", 49.2014, -122.9122, [][2]float64{{-122.9122, 49.2014}, {-122.9300, 49.2400}, {-122.9199, 49.2781}}},
}

type osrmResp struct {
	Code   string `json:"code"`
	Routes []struct {
		Geometry struct {
			Coordinates [][2]float64 `json:"coordinates"`
		} `json:"geometry"`
	} `json:"routes"`
}

func fetch(s start) ([][2]float64, error) {
	url := fmt.Sprintf("https://router.project-osrm.org/route/v1/driving/%f,%f;%f,%f?overview=full&geometries=geojson",
		s.Lng, s.Lat, sfuLng, sfuLat)
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r osrmResp
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	if r.Code != "Ok" || len(r.Routes) == 0 {
		return nil, fmt.Errorf("osrm code %q", r.Code)
	}
	return r.Routes[0].Geometry.Coordinates, nil
}

func main() {
	outDir := "data/routes"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	os.MkdirAll(outDir, 0o755)
	for _, s := range starts {
		coords, err := fetch(s)
		source := "osrm"
		if err != nil {
			fmt.Printf("%s: OSRM failed (%v), using hand-picked waypoints (straight lines, not real roads)\n", s.Name, err)
			coords, source = s.Fallback, "fallback-waypoints"
		}
		feature := map[string]any{
			"type":       "Feature",
			"properties": map[string]any{"name": s.Name, "source": source},
			"geometry":   map[string]any{"type": "LineString", "coordinates": coords},
		}
		b, _ := json.Marshal(feature)
		path := filepath.Join(outDir, s.Name+".geojson")
		if err := os.WriteFile(path, b, 0o644); err != nil {
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("%s: %d points (%s)\n", s.Name, len(coords), source)
		time.Sleep(300 * time.Millisecond) // be polite to the public server
	}
}
