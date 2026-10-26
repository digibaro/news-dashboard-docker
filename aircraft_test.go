package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const adsbSample = `{"ac":[
{"hex":"484f6b","flight":"LIFELN3 ","r":"PH-MAA","t":"EC35","desc":"EUROCOPTER EC-135","dbFlags":0,"alt_baro":1475,"gs":121.3,"track":87.2,"lat":51.7,"lon":5.75,"seen_pos":2.5},
{"hex":"484d8a","flight":"MEDIC01 ","r":"PH-HOW","t":"EC45","alt_baro":"ground","gs":0,"track":0,"lat":52.33,"lon":4.86,"seen_pos":10},
{"hex":"485111","flight":"ZXP02   ","r":"PH-PXB","t":"EC35","alt_baro":900,"gs":95,"track":180,"lat":52.2,"lon":5.95,"seen_pos":1},
{"hex":"485222","flight":"","r":"PH-PXZ","t":"A139","alt_baro":1200,"gs":130,"track":10,"lat":52.0,"lon":4.4,"seen_pos":1},
{"hex":"ae1234","flight":"RCH123  ","r":"07-7180","t":"C17","desc":"BOEING C-17 Globemaster","dbFlags":1,"alt_baro":28000,"gs":450,"track":95,"lat":52.5,"lon":5.0,"seen_pos":0.4},
{"hex":"ae9999","flight":"RCH999  ","t":"C17","dbFlags":1,"alt_baro":30000,"gs":450,"track":95,"lat":49.9,"lon":5.0,"seen_pos":0.4},
{"hex":"4ca123","flight":"KLM1234 ","r":"PH-BXA","t":"B738","dbFlags":0,"alt_baro":12000,"gs":300,"track":90,"lat":52.3,"lon":4.8,"seen_pos":1},
{"hex":"3c4567","flight":"MEDIC99 ","t":"EC45","alt_baro":800,"gs":100,"track":90,"seen_pos":1},
{"hex":"ae5555","flight":"STALE   ","dbFlags":1,"alt_baro":3000,"gs":200,"track":90,"lat":52.1,"lon":5.1,"seen_pos":400}
],"now":1761500000000,"msg":"No error","total":9}`

func TestParseAircraft(t *testing.T) {
	cfg, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	list, err := parseAircraft(cfg, []byte(adsbSample), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, x := range list {
		got = append(got, x.Group+":"+x.Callsign+x.Reg)
	}
	// lifeliners first, then police, then military; the KLM flight, the C-17 over Luxembourg, the
	// aircraft without position and the stale position are left out
	if strings.Join(got, ",") != "lifeliner:LIFELN3PH-MAA,lifeliner:MEDIC01PH-HOW,police:PH-PXZ,police:ZXP02PH-PXB,military:RCH12307-7180" {
		t.Fatalf("aircraft = %v", got)
	}
	l := list[0]
	if l.Name != "Lifeliner 3" || l.AltM != 450 || l.SpeedKmh != 225 || l.Track != 87 || l.Ground || l.Near == nil || l.Near.Name != "Volkel" {
		t.Errorf("lifeliner 3 = %+v %+v", l, l.Near)
	}
	if h := list[1]; !h.Ground || h.AltM != 0 || h.Name != "" || !h.Seen.Equal(time.UnixMilli(1761500000000).Add(-10*time.Second).UTC()) {
		t.Errorf("on the ground = %+v", h)
	}
	cfg.Aircraft.Police, cfg.Aircraft.Military = false, false
	if list, _ := parseAircraft(cfg, []byte(adsbSample), time.Now()); len(list) != 2 {
		t.Errorf("only lifeliners: %d", len(list))
	}
	if _, err := parseAircraft(cfg, []byte(`{"msg":"rate limited"}`), time.Now()); err == nil {
		t.Error("an error message must be an error")
	}
}

func TestNearestPlace(t *testing.T) {
	if p := nearestPlace(52.51, 6.09); p == nil || p.Name != "Zwolle" || p.KM != 0 || p.nl() != "boven Zwolle" {
		t.Errorf("above Zwolle: %+v", p)
	}
	if p := nearestPlace(52.55, 6.15); p == nil || p.Name != "Zwolle" || p.Dir != "NE" || p.KM != 6 || p.en() != "6 km northeast of Zwolle" {
		t.Errorf("NE of Zwolle: %+v", p)
	}
	if p := nearestPlace(55, 3); p != nil {
		t.Errorf("far at sea: %+v", p)
	}
}

func TestAirStateDue(t *testing.T) {
	cfg, _ := parseConfig([]byte(validConfig))
	s := newAirState()
	now := time.Now()
	if ok, _ := s.due(cfg, now, false); !ok {
		t.Fatal("the first fetch is due")
	}
	s.lastFetch = now.Add(-2 * time.Minute)
	if ok, _ := s.due(cfg, now, false); ok {
		t.Error("idle: every 10 minutes")
	}
	if ok, _ := s.due(cfg, now, true); ok {
		t.Error("push subscribers: every 5 minutes")
	}
	s.lastFetch = now.Add(-6 * time.Minute)
	if ok, _ := s.due(cfg, now, true); !ok {
		t.Error("push subscribers: due after 5 minutes")
	}
	s.lastFetch, s.lastViewer = now.Add(-70*time.Second), now
	if ok, _ := s.due(cfg, now, false); !ok {
		t.Error("a viewer: every minute")
	}
	for range 400 { // past 80 % of the budget the active interval doubles
		s.fetches = append(s.fetches, now.Add(-time.Hour))
	}
	if ok, _ := s.due(cfg, now, false); ok {
		t.Error("late in the budget: every 2 minutes")
	}
	for range 50 {
		s.fetches = append(s.fetches, now.Add(-time.Hour))
	}
	s.lastFetch = now.Add(-time.Hour)
	if ok, why := s.due(cfg, now, false); ok || why != "budget" {
		t.Errorf("budget used up: %v %q", ok, why)
	}
	s.fetches = []time.Time{now.Add(-25 * time.Hour)}
	s.backoff = now.Add(time.Minute)
	if ok, why := s.due(cfg, now, false); ok || why != "backoff" || len(s.fetches) != 0 {
		t.Errorf("backoff after 429: %v %q %d", ok, why, len(s.fetches))
	}
}

func TestLifelinerPushAndHandler(t *testing.T) {
	a := newTestApp(t, validConfig)
	cfg := a.config()
	now := time.Now()
	near, far := 51.70, 52.90
	lon := 5.70
	a.push.subs["https://fcm.googleapis.com/near"] = &pushSub{Endpoint: "https://fcm.googleapis.com/near", Topics: []string{"lifeliner"}, Lat: &near, Lon: &lon}
	a.push.subs["https://fcm.googleapis.com/far"] = &pushSub{Endpoint: "https://fcm.googleapis.com/far", Topics: []string{"lifeliner"}, Lat: &far, Lon: &lon}
	a.push.subs["https://fcm.googleapis.com/other"] = &pushSub{Endpoint: "https://fcm.googleapis.com/other", Topics: []string{"quakes"}, Lat: &near, Lon: &lon}
	list, _ := parseAircraft(cfg, []byte(adsbSample), now)
	for i := range list {
		list[i].Seen = now
	}
	a.threats.ok("adsb:radar", AircraftData{Aircraft: list}, "", "")
	msgs := a.lifelinerPush(cfg, now)
	if len(msgs) != 1 || !strings.Contains(msgs[0].Title[0], "Lifeliner 3") || !strings.Contains(msgs[0].Body[0], "Volkel") {
		t.Fatalf("messages = %+v", msgs)
	}
	m := msgs[0]
	if !m.For(a.push.subs["https://fcm.googleapis.com/near"]) || m.For(a.push.subs["https://fcm.googleapis.com/far"]) || m.For(a.push.subs["https://fcm.googleapis.com/other"]) {
		t.Error("only the device within 10 km")
	}
	if len(a.lifelinerPush(cfg, now.Add(time.Minute))) != 0 {
		t.Error("one message per aircraft and device")
	}
	if !a.lifelinerSubscribers() {
		t.Error("subscribers present")
	}

	h := a.routes("/")
	b := get(h, "GET", "/api/aircraft?live=1", nil).Body.String()
	if !strings.Contains(b, `"enabled":true`) || !strings.Contains(b, `"name":"Lifeliner 3"`) || !strings.Contains(b, `"budget":{`) || !strings.Contains(b, `"near":{"name":"Volkel"`) {
		t.Errorf("api: %s", b)
	}
	a.radar.mu.Lock()
	viewer := a.radar.lastViewer
	a.radar.mu.Unlock()
	if time.Since(viewer) > time.Minute {
		t.Error("live=1 marks a viewer")
	}
	off := newTestApp(t, validConfig+"aircraft: { enabled: false }\n")
	if b := get(off.routes("/"), "GET", "/api/aircraft", nil).Body.String(); !strings.Contains(b, `"enabled":false`) {
		t.Errorf("disabled: %s", b)
	}
	for _, tc := range []string{"aircraft: { url: \"http://x\" }\n", "aircraft: { active_interval: 10s }\n", "aircraft: { daily_budget: 5 }\n"} {
		if _, err := parseConfig([]byte(validConfig + tc)); err == nil {
			t.Errorf("%q accepted", tc)
		}
	}
}

func TestAircraftHexAndSAR(t *testing.T) {
	cfg, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"ac":[
{"hex":"486449","flight":"","r":"","t":"A189","alt_baro":500,"gs":120,"track":10,"lat":52.9,"lon":4.7,"seen_pos":1},
{"hex":"486A31","flight":"","t":"EC35","alt_baro":800,"gs":110,"track":10,"lat":52.1,"lon":5.1,"seen_pos":1}],"now":1761500000000}`
	list, err := parseAircraft(cfg, []byte(body), time.Now())
	if err != nil || len(list) != 2 || list[0].Group != "lifeliner" || list[0].Hex != "486a31" || list[1].Group != "sar" || !list[1].Heli {
		t.Fatalf("by hex: %+v %v", list, err)
	}
	if _, err := parseConfig([]byte(validConfig + "aircraft: { sar_hex: [\"48644\"] }\n")); err == nil {
		t.Error("a 5-digit ICAO address was accepted")
	}
}

func TestAirTrails(t *testing.T) {
	s := newAirState()
	t0 := time.Now()
	for i := range 3 {
		l := []Aircraft{{Hex: "a", Lat: 52 + float32(i)/10, Lon: 5, Seen: t0.Add(time.Duration(i) * time.Minute)}}
		s.addTrails(l, t0.Add(time.Duration(i)*time.Minute))
		if len(l[0].Trail) != i {
			t.Fatalf("step %d: trail %v", i, l[0].Trail)
		}
	}
	s.addTrails([]Aircraft{{Hex: "b", Lat: 51, Lon: 4, Seen: t0}}, t0.Add(time.Hour))
	if _, ok := s.trails["a"]; ok || len(s.trails) != 1 {
		t.Errorf("an aircraft gone for 30 minutes is forgotten: %v", s.trails)
	}
}

func TestTiles(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 100)...)
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/7/65/42.png" {
			_, _ = w.Write(png)
			return
		}
		_, _ = w.Write([]byte("<html>not a tile</html>"))
	}))
	defer srv.Close()
	a := newTestApp(t, validConfig+"aircraft: { tile_url: \""+srv.URL+"/{z}/{x}/{y}.png\" }\n")
	h := a.routes("/")
	for i := range 2 {
		r := get(h, "GET", "/api/tile/7/65/42.png", nil)
		if r.Code != 200 || r.Header().Get("Content-Type") != "image/png" || !strings.Contains(r.Header().Get("Cache-Control"), "max-age=604800") || r.Body.Len() != len(png) {
			t.Fatalf("tile %d: %d %v", i, r.Code, r.Header())
		}
	}
	if hits != 1 {
		t.Errorf("the second request comes from the cache (%d fetches)", hits)
	}
	for _, u := range []string{"/api/tile/7/10/10.png", "/api/tile/3/4/2.png", "/api/tile/6/32/21.png", "/api/tile/13/4200/2700.png", "/api/tile/7/x/42.png"} {
		if r := get(h, "GET", u, nil); r.Code != 404 {
			t.Errorf("%s outside the map: %d", u, r.Code)
		}
	}
	if r := get(h, "GET", "/api/tile/7/66/42.png", nil); r.Code != 502 {
		t.Errorf("not a PNG: %d", r.Code)
	}
	if x0, x1, y0, y1 := tileRange(7); x0 > 65 || x1 < 66 || y0 > 41 || y1 < 43 {
		t.Errorf("tile range z7: %d-%d %d-%d", x0, x1, y0, y1)
	}
	c := newTileCache(1000)
	for i := range 6 {
		c.put(fmt.Sprint(i), make([]byte, 200))
	}
	if _, ok := c.get("0"); ok || c.bytes > 1000 {
		t.Errorf("LRU: %d bytes", c.bytes)
	}
	if r := get(h, "GET", "/vendor/leaflet.js", nil); r.Code != 200 || !strings.Contains(r.Body.String(), "Leaflet") {
		t.Errorf("leaflet.js: %d", r.Code)
	}
	for _, tc := range []string{"aircraft: { tile_url: \"http://example.com/{z}/{x}/{y}.png\" }\n", "aircraft: { tile_url: \"https://x.nl/tiles.png\" }\n"} {
		if _, err := parseConfig([]byte(validConfig + tc)); err == nil {
			t.Errorf("%q accepted", tc)
		}
	}
	if strings.Contains(csp, "frame-src") {
		t.Error("the page frames nothing")
	}
}
