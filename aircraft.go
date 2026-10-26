package main

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Luchtradar: Lifeliner trauma helicopters, police helicopters and military aircraft above
// the Netherlands, from the ADS-B network of adsb.lol (open data, ODbL; the same API as ADS-B
// Exchange's, so aircraft.url can point to that with keys.adsb_api_key). One point query
// returns all traffic around the country; only the aircraft of our groups are kept, with a trail of
// their last 30 minutes. The page draws them on an OpenStreetMap map (Leaflet) whose tiles this
// server fetches and caches (/api/tile), so the browser contacts nobody else.
//
// adsb.lol allows ~500 requests a day per IP, so the server does not poll continuously: every
// active_interval while a visitor has the panel open, every push_interval while a device
// subscribes to "Lifeliner in de buurt", otherwise every idle_interval, and never more than
// daily_budget requests in 24 hours. All visitors share the server's data.

type Aircraft struct {
	Hex      string       `json:"hex"`
	Group    string       `json:"group"` // lifeliner, police, sar, military
	Name     string       `json:"name,omitempty"`
	Callsign string       `json:"callsign,omitempty"`
	Reg      string       `json:"reg,omitempty"`
	Type     string       `json:"type,omitempty"` // ICAO type code
	Desc     string       `json:"desc,omitempty"` // type in words
	Lat      float32      `json:"lat"`
	Lon      float32      `json:"lon"`
	AltM     int          `json:"alt_m"` // barometric altitude in metres; 0 on the ground
	Ground   bool         `json:"ground,omitempty"`
	Heli     bool         `json:"heli,omitempty"` // a helicopter (ADS-B category A7, or one of the emergency groups)
	SpeedKmh int          `json:"speed_kmh"`
	Track    int          `json:"track"`
	Seen     time.Time    `json:"seen"` // time of the position
	Near     *NearPlace   `json:"near,omitempty"`
	Trail    [][2]float32 `json:"trail,omitempty"` // older positions (lat, lon), oldest first
}

// NearPlace: the nearest town, "4 km NE of Zwolle" (Dir is N, NE, E, SE, S, SW, W or NW; KM 0 = above it).
type NearPlace struct {
	Name string `json:"name"`
	KM   int    `json:"km"`
	Dir  string `json:"dir,omitempty"`
}

type AircraftData struct {
	Aircraft []Aircraft `json:"aircraft"`
}

type airPoint struct {
	t        time.Time
	lat, lon float32
}

type airState struct {
	mu         sync.Mutex
	fetchMu    sync.Mutex
	trails     map[string][]airPoint // hex -> positions of the last 30 minutes
	fetches    []time.Time           // requests in the last 24 hours
	lastFetch  time.Time
	lastViewer time.Time
	notified   map[string]time.Time // push endpoint|hex -> when a "Lifeliner nearby" was sent
	backoff    time.Time            // after HTTP 429: no requests until then
}

func newAirState() *airState {
	return &airState{trails: map[string][]airPoint{}, notified: map[string]time.Time{}}
}

var icaoHexRe = regexp.MustCompile(`^[0-9a-fA-F]{6}$`)

// The area kept: the Netherlands with a margin (also the map's bounds and the tiles served).
const (
	airMinLat, airMaxLat = 50.65, 53.65
	airMinLon, airMaxLon = 3.2, 7.3
	airTrailAge          = 30 * time.Minute
	airTrailMax          = 60
)

// adsbAlt reads alt_baro: feet, or the string "ground".
type adsbAlt struct {
	ft     float64
	ground bool
}

func (a *adsbAlt) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		a.ground = bytes.Equal(b, []byte(`"ground"`))
		return nil
	}
	return json.Unmarshal(b, &a.ft)
}

type adsbAircraft struct {
	Hex     string   `json:"hex"`
	Flight  string   `json:"flight"`
	R       string   `json:"r"`
	T       string   `json:"t"`
	Desc    string   `json:"desc"`
	DBFlags int      `json:"dbFlags"`
	Lat     *float64 `json:"lat"`
	Lon     *float64 `json:"lon"`
	Alt     adsbAlt  `json:"alt_baro"`
	GS      float64  `json:"gs"`
	Track   float64  `json:"track"`
	SeenPos float64  `json:"seen_pos"`
	Cat     string   `json:"category"`
}

// airGroup: which group an aircraft belongs to ("" = none). Callsign prefixes and
// registrations come from the config; military = the database flag of the ADS-B network.
func airGroup(cfg *Config, a *adsbAircraft, callsign string) string {
	reg := strings.ToUpper(a.R)
	match := func(prefixes, regs, hexes []string) bool {
		for _, p := range prefixes {
			if p != "" && strings.HasPrefix(callsign, strings.ToUpper(p)) {
				return true
			}
		}
		return reg != "" && slices.ContainsFunc(regs, func(r string) bool { return strings.EqualFold(r, reg) }) ||
			slices.ContainsFunc(hexes, func(h string) bool { return strings.EqualFold(h, a.Hex) })
	}
	ac := cfg.Aircraft
	switch {
	case ac.Lifeliner && match(ac.LifelinerCallsigns, ac.LifelinerRegs, ac.LifelinerHex):
		return "lifeliner"
	case ac.Police && match(ac.PoliceCallsigns, ac.PoliceRegs, ac.PoliceHex):
		return "police"
	case ac.SAR && match(ac.SARCallsigns, ac.SARRegs, ac.SARHex):
		return "sar"
	case ac.Military && a.DBFlags&1 != 0:
		return "military"
	}
	return ""
}

// airName: "Lifeliner 3" for the callsign LIFELN3; otherwise empty.
func airName(group, callsign string) string {
	if group == "lifeliner" {
		if n, ok := strings.CutPrefix(callsign, "LIFELN"); ok && len(n) == 1 && n[0] >= '1' && n[0] <= '9' {
			return "Lifeliner " + n
		}
	}
	return ""
}

// parseAircraft reads an ADS-B point query and keeps the aircraft of our groups inside the area.
func parseAircraft(cfg *Config, body []byte, now time.Time) ([]Aircraft, error) {
	var r struct {
		Ac  []adsbAircraft `json:"ac"`
		Now float64        `json:"now"` // ms since 1970
		Msg string         `json:"msg"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("adsb: %w", err)
	}
	if r.Ac == nil && r.Msg != "" && r.Msg != "No error" {
		return nil, fmt.Errorf("adsb: %s", truncate(r.Msg, 80))
	}
	at := now
	if r.Now > 1e12 {
		at = time.UnixMilli(int64(r.Now))
	}
	out := []Aircraft{}
	for i := range r.Ac {
		a := &r.Ac[i]
		if a.Lat == nil || a.Lon == nil || *a.Lat < airMinLat || *a.Lat > airMaxLat || *a.Lon < airMinLon || *a.Lon > airMaxLon || a.SeenPos > 120 {
			continue
		}
		cs := strings.ToUpper(strings.TrimSpace(a.Flight))
		g := airGroup(cfg, a, cs)
		if g == "" {
			continue
		}
		x := Aircraft{Hex: strings.ToLower(a.Hex), Group: g, Name: airName(g, cs), Callsign: cs, Reg: truncate(a.R, 12), Type: truncate(a.T, 6),
			Desc: truncate(a.Desc, 60), Lat: float32(*a.Lat), Lon: float32(*a.Lon), Ground: a.Alt.ground, SpeedKmh: int(math.Round(a.GS * 1.852)),
			Track: int(math.Round(a.Track)) % 360, Seen: at.Add(-time.Duration(a.SeenPos * float64(time.Second))).UTC().Truncate(time.Second)}
		x.Heli = a.Cat == "A7" || g != "military"
		if !x.Ground {
			x.AltM = max(0, int(math.Round(a.Alt.ft*0.3048/10))*10)
		}
		x.Near = nearestPlace(x.Lat, x.Lon)
		out = append(out, x)
	}
	order := map[string]int{"lifeliner": 0, "police": 1, "sar": 2, "military": 3}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Group] != order[out[j].Group] {
			return order[out[i].Group] < order[out[j].Group]
		}
		return out[i].Callsign+out[i].Reg < out[j].Callsign+out[j].Reg
	})
	return out, nil
}

// addTrails records the positions and adds each aircraft's trail; aircraft not seen for
// 30 minutes are forgotten (caller holds mu).
func (s *airState) addTrails(list []Aircraft, now time.Time) {
	seen := map[string]bool{}
	for i := range list {
		x := &list[i]
		seen[x.Hex] = true
		tr := s.trails[x.Hex]
		for len(tr) > 0 && now.Sub(tr[0].t) > airTrailAge {
			tr = tr[1:]
		}
		if n := len(tr); n == 0 || tr[n-1].lat != x.Lat || tr[n-1].lon != x.Lon {
			tr = append(tr, airPoint{x.Seen, x.Lat, x.Lon})
		}
		if len(tr) > airTrailMax {
			tr = tr[len(tr)-airTrailMax:]
		}
		s.trails[x.Hex] = slices.Clip(tr)
		if len(tr) > 1 {
			x.Trail = make([][2]float32, 0, len(tr)-1)
			for _, p := range tr[:len(tr)-1] {
				x.Trail = append(x.Trail, [2]float32{p.lat, p.lon})
			}
		}
	}
	for hex, tr := range s.trails {
		if !seen[hex] && (len(tr) == 0 || now.Sub(tr[len(tr)-1].t) > airTrailAge) {
			delete(s.trails, hex)
		}
	}
}

// due: whether the server should fetch now, and why not (caller holds mu).
func (s *airState) due(cfg *Config, now time.Time, pushSubs bool) (bool, string) {
	ac := cfg.Aircraft
	for len(s.fetches) > 0 && now.Sub(s.fetches[0]) > 24*time.Hour {
		s.fetches = s.fetches[1:]
	}
	if now.Before(s.backoff) {
		return false, "backoff"
	}
	if len(s.fetches) >= ac.DailyBudget {
		return false, "budget"
	}
	iv := ac.IdleInterval.D()
	if pushSubs {
		iv = min(iv, ac.PushInterval.D())
	}
	if now.Sub(s.lastViewer) < 2*time.Minute {
		active := ac.ActiveInterval.D()
		if len(s.fetches) >= ac.DailyBudget*8/10 { // the last fifth of the budget lasts twice as long
			active *= 2
		}
		iv = min(iv, active)
	}
	return now.Sub(s.lastFetch) >= iv-3*time.Second, ""
}

func (a *App) lifelinerSubscribers() bool {
	p := a.push
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.subs {
		if s.Lat != nil && slices.Contains(s.Topics, "lifeliner") {
			return true
		}
	}
	return false
}

// runAircraft runs every 15 s and fetches only when due.
func (a *App) runAircraft(ctx context.Context) error {
	a.fetchAircraftIfDue(ctx)
	return nil
}

func (a *App) fetchAircraftIfDue(ctx context.Context) {
	s := a.radar
	if !s.fetchMu.TryLock() {
		return
	}
	defer s.fetchMu.Unlock()
	cfg := a.config()
	now := time.Now()
	subs := cfg.Push.Enabled && a.lifelinerSubscribers()
	s.mu.Lock()
	ok, _ := s.due(cfg, now, subs)
	if ok {
		s.lastFetch = now
		s.fetches = append(s.fetches, now)
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	const key = "adsb:radar"
	req := FetchReq{URL: cfg.Aircraft.URL, Accept: "application/json", Timeout: 20 * time.Second}
	if k := cfg.Keys.ADSBAPIKey; k != "" {
		if u, err := url.Parse(cfg.Aircraft.URL); err == nil {
			req.Header = map[string]string{"x-rapidapi-key": k, "x-rapidapi-host": u.Host, "api-auth": k}
		}
	}
	resp, err := a.fetcher.Do(ctx, req)
	var list []Aircraft
	if err == nil {
		list, err = parseAircraft(cfg, resp.Body, time.Now())
	}
	if err != nil {
		if strings.Contains(err.Error(), "429") {
			s.mu.Lock()
			s.backoff = time.Now().Add(15 * time.Minute)
			s.mu.Unlock()
		}
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return
	}
	s.mu.Lock()
	s.addTrails(list, time.Now())
	s.mu.Unlock()
	a.threats.ok(key, AircraftData{Aircraft: list}, "", "")
}

// handleAircraft answers GET /api/aircraft; ?live=1 means the panel is open on screen, which
// switches the server to active_interval for the next two minutes.
func (a *App) handleAircraft(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Aircraft.Enabled {
		writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": false})
		return
	}
	s := a.radar
	now := time.Now()
	if r.URL.Query().Get("live") == "1" {
		s.mu.Lock()
		s.lastViewer = now
		s.mu.Unlock()
		go a.fetchAircraftIfDue(context.WithoutCancel(r.Context()))
	}
	s.mu.Lock()
	_, why := s.due(cfg, now, false)
	used := len(s.fetches)
	s.mu.Unlock()
	e := a.feedEntry("adsb:radar")
	e["enabled"] = true
	e["groups"] = map[string]bool{"lifeliner": cfg.Aircraft.Lifeliner, "police": cfg.Aircraft.Police, "sar": cfg.Aircraft.SAR, "military": cfg.Aircraft.Military}
	e["bounds"] = [4]float64{airMinLat, airMinLon, airMaxLat, airMaxLon}
	e["interval_s"] = int(cfg.Aircraft.ActiveInterval.D().Seconds())
	e["budget"] = map[string]any{"used": used, "limit": cfg.Aircraft.DailyBudget, "exhausted": why == "budget"}
	if v, ok := a.threats.get("adsb:radar").Data.(AircraftData); ok {
		e["aircraft"] = v.Aircraft
	}
	writeJSON(w, r, http.StatusOK, 0, e)
}

// lifelinerPush: "Lifeliner in de buurt" for the devices within push.lifeliner_km of an
// airborne Lifeliner; each device hears about an aircraft at most once per 90 minutes.
func (a *App) lifelinerPush(cfg *Config, now time.Time) []pushMsg {
	if !cfg.Aircraft.Enabled || !cfg.Aircraft.Lifeliner {
		return nil
	}
	st := a.threats.get("adsb:radar")
	d, ok := st.Data.(AircraftData)
	if !ok || now.Sub(st.FetchedAt) > 10*time.Minute {
		return nil
	}
	type sub struct {
		endpoint string
		lat, lon float32
	}
	var subs []sub
	p := a.push
	p.mu.Lock()
	for _, s := range p.subs {
		if s.Lat != nil && s.Lon != nil && slices.Contains(s.Topics, "lifeliner") {
			subs = append(subs, sub{s.Endpoint, float32(*s.Lat), float32(*s.Lon)})
		}
	}
	p.mu.Unlock()
	radius := float64(cfg.Push.LifelinerKM) * 1000
	s := a.radar
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, t := range s.notified {
		if now.Sub(t) > 90*time.Minute {
			delete(s.notified, k)
		}
	}
	var msgs []pushMsg
	for _, x := range d.Aircraft {
		if x.Group != "lifeliner" || x.Ground || now.Sub(x.Seen) > 5*time.Minute {
			continue
		}
		to := map[string]bool{}
		for _, u := range subs {
			k := u.endpoint + "|" + x.Hex
			if _, done := s.notified[k]; done || distM(u.lat, u.lon, x.Lat, x.Lon) > radius {
				continue
			}
			s.notified[k] = now
			to[u.endpoint] = true
		}
		if len(to) == 0 {
			continue
		}
		name := x.Name
		if name == "" {
			name = "Lifeliner " + cmp2(x.Callsign, x.Reg)
		}
		where := [2]string{"", ""}
		if x.Near != nil {
			where = [2]string{", nu " + x.Near.nl(), ", now " + x.Near.en()}
		}
		alt := strconv.Itoa(x.AltM)
		msgs = append(msgs, pushMsg{Topic: "lifeliner", Tag: "lifeliner-" + x.Hex, TTL: 1800, URL: "#panel-aircraft",
			Title: [2]string{name + " vliegt bij je in de buurt", name + " is flying near you"},
			Body: [2]string{fmt.Sprintf("Traumahelikopter binnen %d km van je locatie%s, op %s m hoogte.", cfg.Push.LifelinerKM, where[0], alt),
				fmt.Sprintf("Trauma helicopter within %d km of your location%s, at %s m.", cfg.Push.LifelinerKM, where[1], alt)},
			For: func(ps *pushSub) bool { return to[ps.Endpoint] }})
	}
	return msgs
}

func cmp2(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

var dirNL = map[string]string{"N": "ten noorden van", "NE": "ten noordoosten van", "E": "ten oosten van", "SE": "ten zuidoosten van",
	"S": "ten zuiden van", "SW": "ten zuidwesten van", "W": "ten westen van", "NW": "ten noordwesten van"}
var dirEN = map[string]string{"N": "north of", "NE": "northeast of", "E": "east of", "SE": "southeast of",
	"S": "south of", "SW": "southwest of", "W": "west of", "NW": "northwest of"}

func (n *NearPlace) nl() string {
	if n.KM == 0 {
		return "boven " + n.Name
	}
	return fmt.Sprintf("%d km %s %s", n.KM, dirNL[n.Dir], n.Name)
}

func (n *NearPlace) en() string {
	if n.KM == 0 {
		return "above " + n.Name
	}
	return fmt.Sprintf("%d km %s %s", n.KM, dirEN[n.Dir], n.Name)
}

type airPlace struct {
	name     string
	lat, lon float32
}

// airPlaces: towns to describe a position with (provincial capitals, larger towns, the Wadden islands).
var airPlaces = []airPlace{
	{"Amsterdam", 52.37, 4.90}, {"Rotterdam", 51.92, 4.48}, {"Den Haag", 52.08, 4.31}, {"Utrecht", 52.09, 5.12}, {"Eindhoven", 51.44, 5.48},
	{"Groningen", 53.22, 6.57}, {"Tilburg", 51.56, 5.09}, {"Almere", 52.37, 5.22}, {"Breda", 51.59, 4.78}, {"Nijmegen", 51.84, 5.86},
	{"Apeldoorn", 52.21, 5.97}, {"Arnhem", 51.98, 5.91}, {"Haarlem", 52.38, 4.64}, {"Enschede", 52.22, 6.89}, {"Amersfoort", 52.16, 5.39},
	{"Zaandam", 52.44, 4.83}, {"'s-Hertogenbosch", 51.69, 5.30}, {"Zwolle", 52.51, 6.09}, {"Leiden", 52.16, 4.49}, {"Maastricht", 50.85, 5.69},
	{"Dordrecht", 51.81, 4.67}, {"Zoetermeer", 52.06, 4.49}, {"Deventer", 52.25, 6.16}, {"Delft", 52.01, 4.36}, {"Alkmaar", 52.63, 4.75},
	{"Heerlen", 50.89, 5.98}, {"Venlo", 51.37, 6.17}, {"Leeuwarden", 53.20, 5.80}, {"Hilversum", 52.22, 5.18}, {"Assen", 52.99, 6.56},
	{"Emmen", 52.78, 6.90}, {"Lelystad", 52.52, 5.47}, {"Middelburg", 51.50, 3.61}, {"Vlissingen", 51.44, 3.57}, {"Goes", 51.50, 3.89},
	{"Terneuzen", 51.33, 3.83}, {"Roosendaal", 51.53, 4.47}, {"Bergen op Zoom", 51.49, 4.29}, {"Helmond", 51.48, 5.66}, {"Oss", 51.77, 5.52},
	{"Roermond", 51.19, 5.99}, {"Sittard", 51.00, 5.87}, {"Weert", 51.25, 5.71}, {"Gouda", 52.01, 4.71}, {"Ede", 52.04, 5.66},
	{"Wageningen", 51.97, 5.67}, {"Zutphen", 52.14, 6.20}, {"Doetinchem", 51.97, 6.29}, {"Winterswijk", 51.97, 6.72}, {"Almelo", 52.36, 6.66},
	{"Hengelo", 52.27, 6.79}, {"Hardenberg", 52.58, 6.62}, {"Meppel", 52.70, 6.19}, {"Hoogeveen", 52.72, 6.48}, {"Kampen", 52.56, 5.91},
	{"Harderwijk", 52.34, 5.62}, {"Emmeloord", 52.71, 5.75}, {"Heerenveen", 52.96, 5.92}, {"Sneek", 53.03, 5.66}, {"Drachten", 53.11, 6.10},
	{"Harlingen", 53.17, 5.42}, {"Den Helder", 52.96, 4.76}, {"Hoorn", 52.64, 5.06}, {"Enkhuizen", 52.70, 5.29}, {"Purmerend", 52.50, 4.95},
	{"IJmuiden", 52.46, 4.61}, {"Schiphol", 52.31, 4.76}, {"Hoofddorp", 52.30, 4.69}, {"Gorinchem", 51.83, 4.97},
	{"Tiel", 51.89, 5.43}, {"Culemborg", 51.96, 5.23}, {"Veenendaal", 52.03, 5.56}, {"Spijkenisse", 51.85, 4.33}, {"Hellevoetsluis", 51.83, 4.13},
	{"Zierikzee", 51.65, 3.92}, {"Delfzijl", 53.33, 6.92}, {"Winschoten", 53.14, 7.03}, {"Stadskanaal", 52.99, 6.95}, {"Veendam", 53.11, 6.88},
	{"Texel", 53.06, 4.80}, {"Vlieland", 53.30, 5.07}, {"Terschelling", 53.40, 5.35}, {"Ameland", 53.45, 5.77}, {"Schiermonnikoog", 53.48, 6.17},
	{"Volkel", 51.66, 5.70}, {"vliegbasis Leeuwarden", 53.23, 5.76}, {"Gilze-Rijen", 51.57, 4.93}, {"Woensdrecht", 51.45, 4.34}, {"De Kooy", 52.92, 4.78},
}

func nearestPlace(lat, lon float32) *NearPlace {
	best, bestD := -1, math.MaxFloat64
	for i, p := range airPlaces {
		if d := distM(p.lat, p.lon, lat, lon); d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 || bestD > 40000 {
		return nil
	}
	p := airPlaces[best]
	km := int(math.Round(bestD / 1000))
	if km <= 1 {
		return &NearPlace{Name: p.name}
	}
	dy := float64(lat - p.lat)
	dx := float64(lon-p.lon) * math.Cos(float64(p.lat)*math.Pi/180)
	deg := math.Mod(math.Atan2(dx, dy)*180/math.Pi+360, 360)
	dirs := []string{"N", "NE", "E", "SE", "S", "SW", "W", "NW"}
	return &NearPlace{Name: p.name, KM: km, Dir: dirs[int(math.Round(deg/45))%8]}
}

// ---------------------------------------------------------------------------
// Map tiles for Luchtradar: GET /api/tile/{z}/{x}/{y} fetches aircraft.tile_url (OpenStreetMap by
// default) for zoom 7–12 inside the map's bounds only, so this is no open tile proxy. Tiles are kept
// in a small LRU (tileCacheBytes) and browsers may cache them for a week, as the OSM tile policy asks.

const (
	tileMinZoom, tileMaxZoom = 7, 12
	tileCacheBytes           = 4 << 20
	tilePadLat, tilePadLon   = 1.5, 3.0 // a wide panel shows more than the bounds
)

type tileCache struct {
	mu    sync.Mutex
	max   int
	bytes int
	ll    *list.List
	m     map[string]*list.Element
}

type tileEntry struct {
	key  string
	body []byte
}

func newTileCache(max int) *tileCache {
	return &tileCache{max: max, ll: list.New(), m: map[string]*list.Element{}}
}

func (c *tileCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.m[key]; ok {
		c.ll.MoveToFront(el)
		return el.Value.(*tileEntry).body, true
	}
	return nil, false
}

func (c *tileCache) put(key string, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[key]; ok || len(body) > c.max/4 {
		return
	}
	c.m[key] = c.ll.PushFront(&tileEntry{key, body})
	c.bytes += len(body)
	for c.bytes > c.max && c.ll.Len() > 1 {
		last := c.ll.Back()
		e := last.Value.(*tileEntry)
		c.bytes -= len(e.body)
		c.ll.Remove(last)
		delete(c.m, e.key)
	}
}

// tileRange: the tile columns and rows that cover the map's bounds plus the margin the page may pan
// into (tilePadLat/Lon, as its maxBounds), with one tile to spare.
func tileRange(z int) (x0, x1, y0, y1 int) {
	n := float64(int(1) << z)
	tx := func(lon float64) int { return int(math.Floor((lon + 180) / 360 * n)) }
	ty := func(lat float64) int {
		r := lat * math.Pi / 180
		return int(math.Floor((1 - math.Log(math.Tan(r)+1/math.Cos(r))/math.Pi) / 2 * n))
	}
	return tx(airMinLon-tilePadLon) - 1, tx(airMaxLon+tilePadLon) + 1, ty(airMaxLat+tilePadLat) - 1, ty(airMinLat-tilePadLat) + 1
}

func (a *App) handleTile(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Aircraft.Enabled {
		http.NotFound(w, r)
		return
	}
	z, err1 := strconv.Atoi(r.PathValue("z"))
	x, err2 := strconv.Atoi(r.PathValue("x"))
	y, err3 := strconv.Atoi(strings.TrimSuffix(r.PathValue("y"), ".png"))
	if err1 != nil || err2 != nil || err3 != nil || z < tileMinZoom || z > tileMaxZoom {
		http.NotFound(w, r)
		return
	}
	if x0, x1, y0, y1 := tileRange(z); x < x0 || x > x1 || y < y0 || y > y1 {
		http.NotFound(w, r)
		return
	}
	key := fmt.Sprintf("%d/%d/%d", z, x, y)
	body, ok := a.tiles.get(key)
	if !ok {
		if !a.images.limiter.allow(a.clientIP(r)) {
			writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken")
			return
		}
		u := strings.NewReplacer("{z}", strconv.Itoa(z), "{x}", strconv.Itoa(x), "{y}", strconv.Itoa(y)).Replace(cfg.Aircraft.TileURL)
		resp, err := a.fetcher.Do(context.WithoutCancel(r.Context()), FetchReq{URL: u, Accept: "image/png,image/*", MaxBody: 512 << 10, Timeout: 15 * time.Second})
		if err != nil || !bytes.HasPrefix(resp.Body, []byte("\x89PNG")) {
			http.Error(w, "tile unavailable", http.StatusBadGateway)
			return
		}
		body = resp.Body
		a.tiles.put(key, body)
	}
	h := w.Header()
	h.Set("Content-Type", "image/png")
	h.Set("Cache-Control", "public, max-age=604800")
	h.Set("Content-Length", strconv.Itoa(len(body)))
	_, _ = w.Write(body)
}
