package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Bruggen (Verkeer panel): bridge openings from NDW open data.
//   - every movable bridge comes from Rijkswaterstaat's Fairway Information Services
//     (FIS, vaarweginformatie.nl): name, place and position, refreshed daily. Visitors
//     choose favourites from this list (id "B<FIS id>");
//   - open now: records "bridgeSwingInOperation" in NDW's actueel beeld, the same
//     download as the traffic panel (the traffic job runs at bridges.interval);
//   - planned: NDW's planning feed for bridge openings (mostly Noord-Holland).
// NDW identifies a bridge only by ISRS code (RIS-index) and position; an opening
// belongs to the movable FIS bridge at that point (within 150 m). Bridges NDW has
// reported are remembered (with cache.snapshot_path in <snapshot>.bridges.json), so a
// favourite NDW never reports says so instead of "closed".
// Memory: ~1500 bridges in one slice (float32 positions), grid cells and the id map
// hold indexes; openings keep an index and Unix seconds; raw downloads are dropped.

type BridgeInfo struct {
	ID   string  `json:"id"` // B<FIS id>, or the ISRS code for an NDW bridge FIS does not know
	Name string  `json:"name"`
	City string  `json:"city,omitempty"`
	Lat  float32 `json:"lat"`
	Lon  float32 `json:"lon"`
	NDW  bool    `json:"ndw,omitempty"` // NDW has reported openings of this bridge
}

type BridgeOpening struct {
	ID    string     `json:"id"`
	Start time.Time  `json:"start"`
	End   *time.Time `json:"end,omitempty"`
}

type bridgeRec struct {
	ndw        string // ISRS code
	lat, lon   float32
	start, end int64 // Unix seconds; end 0 = unknown
}

type bridgeStore struct {
	mu        sync.Mutex
	all       []BridgeInfo       // FIS bridges, then NDW-only bridges
	byID      map[string]int32   // bridge id → index in all
	grid      map[[2]int][]int32 // ~1 km cells of FIS bridges
	fisN      int                // all[:fisN] come from FIS
	fisAt     time.Time
	ndwIdx    map[string]int32 // ISRS code → index in all
	seen      map[string]int64 // bridge ids NDW reported, last time (Unix)
	dirty     bool
	open      []bridgeRec
	openAt    time.Time
	planned   []bridgeRec
	plannedAt time.Time
}

func newBridgeStore() *bridgeStore {
	return &bridgeStore{byID: map[string]int32{}, grid: map[[2]int][]int32{}, ndwIdx: map[string]int32{}, seen: map[string]int64{}}
}

const (
	bridgeOpenMax = 2 * time.Hour // an "open" record older than this without an end is a missed close
	bridgeMatchM  = 150.0         // FIS bridge within this distance of the NDW point
)

var bridgeIDRe = regexp.MustCompile(`^[A-Z][A-Z0-9]{2,24}$`)

// bridgeRecords picks the bridge openings out of parsed NDW records; ISRS codes are
// interned so 1600 planned openings share a few dozen strings.
func bridgeRecords(recs []ndwRecord) []bridgeRec {
	var out []bridgeRec
	intern := map[string]string{}
	for _, r := range recs {
		if r.gnm != "bridgeSwingInOperation" || r.extCode == "" || (r.status != "" && r.status != "active" && r.status != "definedByValidityTimeSpec") {
			continue
		}
		st, err := time.Parse(time.RFC3339Nano, r.start)
		if err != nil {
			continue
		}
		code, ok := intern[r.extCode]
		if !ok {
			code = strings.Clone(r.extCode)
			intern[code] = code
		}
		b := bridgeRec{ndw: code, lat: float32(r.lat), lon: float32(r.lon), start: st.Unix()}
		if en, err := time.Parse(time.RFC3339Nano, r.end); err == nil {
			b.end = en.Unix()
		}
		out = append(out, b)
	}
	return out
}

func gridCell(lat, lon float32) [2]int {
	return [2]int{int(math.Floor(float64(lat) * 100)), int(math.Floor(float64(lon) * 60))}
}

func distM(lat1, lon1, lat2, lon2 float32) float64 {
	return math.Hypot(float64(lat2-lat1)*111320, float64(lon2-lon1)*111320*math.Cos(float64(lat1)*math.Pi/180))
}

// nearestFIS: index of the movable FIS bridge within 150 m of the point, or -1 (caller holds mu).
func (s *bridgeStore) nearestFIS(lat, lon float32) int32 {
	c := gridCell(lat, lon)
	best, bestD := int32(-1), bridgeMatchM
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			for _, i := range s.grid[[2]int{c[0] + dy, c[1] + dx}] {
				if d := distM(lat, lon, s.all[i].Lat, s.all[i].Lon); d <= bestD {
					best, bestD = i, d
				}
			}
		}
	}
	return best
}

// idx: the bridge index for an NDW record (caller holds mu).
func (s *bridgeStore) idx(r bridgeRec) int32 {
	if i, ok := s.ndwIdx[r.ndw]; ok {
		return i
	}
	i := s.nearestFIS(r.lat, r.lon)
	if i < 0 { // no FIS bridge here (or the FIS list is not loaded yet)
		var ok bool
		if i, ok = s.byID[r.ndw]; !ok {
			i = int32(len(s.all))
			s.all = append(s.all, BridgeInfo{ID: r.ndw, Name: "Brug " + r.ndw, Lat: r.lat, Lon: r.lon})
			s.byID[r.ndw] = i
		}
	}
	if s.fisN > 0 { // keep the match; before the FIS list it may still change
		s.ndwIdx[r.ndw] = i
	}
	return i
}

func (s *bridgeStore) markSeen(recs []bridgeRec, now time.Time) {
	for _, r := range recs {
		id := s.all[s.idx(r)].ID
		if t, ok := s.seen[id]; !ok || now.Unix()-t > 86400 {
			s.dirty = true
		}
		s.seen[id] = now.Unix()
	}
}

func (a *App) setBridgesOpen(recs []ndwRecord, now time.Time) {
	s := a.bridges
	if s == nil {
		return
	}
	br := bridgeRecords(recs)
	s.mu.Lock()
	s.markSeen(br, now)
	s.open, s.openAt = br, now
	s.mu.Unlock()
	a.saveBridgeState()
}

func (a *App) runBridgePlan(ctx context.Context) error {
	const key = "ndw:bridgeplan"
	cfg := a.config().Bridges
	s := a.bridges
	s.mu.Lock()
	needFIS := cfg.NamesURL != "" && (s.fisN == 0 || time.Since(s.fisAt) > 24*time.Hour)
	s.mu.Unlock()
	if needFIS {
		if err := a.loadFISBridges(ctx, cfg.NamesURL); err != nil {
			slog.Warn("fetch failed", "source", "fis:bridges", "err", err)
		}
	}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: cfg.PlannedURL, Accept: "application/gzip, application/xml;q=0.9, */*;q=0.5"})
	if err == nil {
		var recs []ndwRecord
		if recs, err = parseNDWBody(resp.Body); err == nil {
			br := bridgeRecords(recs)
			now := time.Now()
			s.mu.Lock()
			s.markSeen(br, now)
			s.planned, s.plannedAt = br, now
			s.mu.Unlock()
			a.threats.ok(key, len(br), "", "")
			a.saveBridgeState()
			return nil
		}
	}
	a.threats.fail(key, err)
	slog.Warn("fetch failed", "source", key, "err", err)
	return err
}

// parseNDWBody: a DATEX II publication, gzipped or not.
func parseNDWBody(b []byte) ([]ndwRecord, error) {
	var body io.Reader = bytes.NewReader(b)
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(body)
		if err != nil {
			return nil, fmt.Errorf("ndw: %w", err)
		}
		body = io.LimitReader(zr, 40<<20)
	}
	return parseNDW(body)
}

var (
	fisMemberRe = regexp.MustCompile(`(?s)<gml:featureMember>(.*?)</gml:featureMember>`)
	fisGMLIDRe  = regexp.MustCompile(`gml:id="Bridge_(\d+)"`)
	fisPosRe    = regexp.MustCompile(`<gml:pos>\s*([-0-9.]+)\s+([-0-9.]+)\s*</gml:pos>`)
	fisNameRe   = regexp.MustCompile(`<app:Name>([^<]*)</app:Name>`)
	fisCityRe   = regexp.MustCompile(`<app:City>([^<]*)</app:City>`)
)

// loadFISBridges: every movable bridge in the Netherlands (one WFS request, ~0.8 MB).
func (a *App) loadFISBridges(ctx context.Context, base string) error {
	q := url.Values{"SERVICE": {"WFS"}, "VERSION": {"1.1.0"}, "REQUEST": {"GetFeature"}, "TYPENAME": {"app:Bridge"},
		"PROPERTYNAME": {"app:Name,app:City,app:Geometry"},
		"FILTER":       {`<Filter xmlns="http://www.opengis.net/ogc"><PropertyIsEqualTo><PropertyName>CanOpen</PropertyName><Literal>true</Literal></PropertyIsEqualTo></Filter>`}}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "?" + q.Encode(), Accept: "text/xml, application/xml;q=0.9"})
	if err != nil {
		return err
	}
	list := parseFISBridges(resp.Body)
	if len(list) < 100 {
		return fmt.Errorf("fis: only %d bridges", len(list))
	}
	s := a.bridges
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all = list
	s.fisN = len(list)
	s.byID = make(map[string]int32, len(list))
	s.grid = map[[2]int][]int32{}
	for i, b := range list {
		s.byID[b.ID] = int32(i)
		c := gridCell(b.Lat, b.Lon)
		s.grid[c] = append(s.grid[c], int32(i))
	}
	s.fisAt = time.Now()
	s.ndwIdx = map[string]int32{} // match again against the new list
	return nil
}

func parseFISBridges(body []byte) []BridgeInfo {
	var out []BridgeInfo
	for _, m := range fisMemberRe.FindAllSubmatch(body, -1) {
		f := m[1]
		id, p, n := fisGMLIDRe.FindSubmatch(f), fisPosRe.FindSubmatch(f), fisNameRe.FindSubmatch(f)
		if id == nil || p == nil || n == nil {
			continue
		}
		lon, _ := strconv.ParseFloat(string(p[1]), 32)
		lat, _ := strconv.ParseFloat(string(p[2]), 32)
		name := strings.TrimSpace(htmlUnescape(string(n[1])))
		if name == "" || lat < 50.7 || lat > 53.7 || lon < 3.2 || lon > 7.3 { // the Netherlands
			continue
		}
		b := BridgeInfo{ID: "B" + string(id[1]), Name: name, Lat: float32(lat), Lon: float32(lon)}
		if c := fisCityRe.FindSubmatch(f); c != nil {
			b.City = strings.TrimSpace(htmlUnescape(string(c[1])))
		}
		out = append(out, b)
	}
	return slices.Clip(out)
}

var xmlUnescaper = strings.NewReplacer("&amp;", "&", "&apos;", "'", "&#39;", "'", "&quot;", `"`, "&lt;", "<", "&gt;", ">")

func htmlUnescape(s string) string { return xmlUnescaper.Replace(s) }

// info for an index (caller holds mu).
func (s *bridgeStore) info(i int32) BridgeInfo {
	b := s.all[i]
	_, b.NDW = s.seen[b.ID]
	return b
}

// BridgesData for /api/bridges: open now, the favourites' next 3 planned openings
// (FavPlanned), the next planned openings of the other bridges, and the bridges these
// refer to. Renamed maps favourite ids from before the FIS list (ISRS codes) to bridge ids.
type BridgesData struct {
	Open       []BridgeOpening   `json:"open"`
	FavPlanned []BridgeOpening   `json:"fav_planned"`
	Planned    []BridgeOpening   `json:"planned"`
	Bridges    []BridgeInfo      `json:"bridges"`
	Renamed    map[string]string `json:"renamed,omitempty"`
	OpenAt     *time.Time        `json:"open_at,omitempty"`
	PlannedAt  *time.Time        `json:"planned_at,omitempty"`
}

func opening(id string, start, end int64) BridgeOpening {
	o := BridgeOpening{ID: id, Start: time.Unix(start, 0).UTC()}
	if end > 0 {
		e := time.Unix(end, 0).UTC()
		o.End = &e
	}
	return o
}

func (s *bridgeStore) snapshot(now time.Time, favIn []string) BridgesData {
	s.mu.Lock()
	defer s.mu.Unlock()
	d := BridgesData{Open: []BridgeOpening{}, FavPlanned: []BridgeOpening{}, Planned: []BridgeOpening{}, Bridges: []BridgeInfo{}}
	fav := map[string]bool{}
	refs := map[int32]bool{}
	for _, id := range favIn {
		if i, ok := s.ndwIdx[id]; ok && s.all[i].ID != id { // an ISRS code from before the FIS list
			if d.Renamed == nil {
				d.Renamed = map[string]string{}
			}
			d.Renamed[id] = s.all[i].ID
			id = s.all[i].ID
		}
		fav[id] = true
		if i, ok := s.byID[id]; ok {
			refs[i] = true
		}
	}
	n := now.Unix()
	if now.Sub(s.openAt) < 15*time.Minute { // an old download says nothing about now
		openIDs := map[int32]bool{}
		for _, r := range s.open {
			if r.start > n+60 || n-r.start > int64(bridgeOpenMax/time.Second) || (r.end > 0 && r.end < n) {
				continue
			}
			i := s.idx(r)
			if openIDs[i] {
				continue
			}
			openIDs[i], refs[i] = true, true
			d.Open = append(d.Open, opening(s.all[i].ID, r.start, r.end))
		}
		t := s.openAt
		d.OpenAt = &t
	}
	type plan struct {
		i          int32
		start, end int64
	}
	var all []plan
	dup := map[[2]int64]bool{}
	for _, r := range s.planned {
		if r.end == 0 || r.end <= n || r.start > n+86400 {
			continue
		}
		i := s.idx(r)
		if k := [2]int64{int64(i), r.start}; !dup[k] {
			dup[k] = true
			all = append(all, plan{i, r.start, r.end})
		}
	}
	sort.Slice(all, func(a, b int) bool { return all[a].start < all[b].start })
	perFav := map[int32]int{}
	for _, p := range all {
		id := s.all[p.i].ID
		switch {
		case fav[id]:
			if perFav[p.i] < 3 {
				perFav[p.i]++
				d.FavPlanned = append(d.FavPlanned, opening(id, p.start, p.end))
			}
		case len(d.Planned) < 60:
			d.Planned = append(d.Planned, opening(id, p.start, p.end))
			refs[p.i] = true
		}
	}
	if !s.plannedAt.IsZero() {
		t := s.plannedAt
		d.PlannedAt = &t
	}
	sort.Slice(d.Open, func(a, b int) bool { return d.Open[a].Start.After(d.Open[b].Start) })
	for i := range refs {
		d.Bridges = append(d.Bridges, s.info(i))
	}
	sort.Slice(d.Bridges, func(a, b int) bool { return d.Bridges[a].ID < d.Bridges[b].ID })
	return d
}

// writeList streams every bridge as JSON (built on request, nothing kept).
func (s *bridgeStore) writeList(w io.Writer) error {
	s.mu.Lock()
	order := make([]int32, len(s.all))
	for i := range order {
		order[i] = int32(i)
	}
	sort.Slice(order, func(a, b int) bool {
		x, y := &s.all[order[a]], &s.all[order[b]]
		if x.City != y.City {
			return x.City < y.City
		}
		return x.Name < y.Name
	})
	list := make([]BridgeInfo, len(order))
	for k, i := range order {
		list[k] = s.info(i)
	}
	s.mu.Unlock()
	return json.NewEncoder(w).Encode(map[string]any{"enabled": true, "bridges": list})
}

func (a *App) handleBridges(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Bridges.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	var fav []string
	for _, id := range strings.Split(r.URL.Query().Get("fav"), ",") {
		if bridgeIDRe.MatchString(id) && len(fav) < 30 {
			fav = append(fav, id)
		}
	}
	resp := a.feedEntry("ndw:bridgeplan")
	resp["enabled"] = true
	resp["source"] = map[string]string{"name": "NDW (brugopeningen)", "url": "https://www.ndw.nu/", "names": "Rijkswaterstaat vaarweginformatie.nl"}
	resp["data"] = a.bridges.snapshot(time.Now(), fav)
	writeJSON(w, r, http.StatusOK, 60, resp)
}

// handleBridgeList: GET /api/bridges/list, every movable bridge (~1500, for Instellingen).
func (a *App) handleBridgeList(w http.ResponseWriter, r *http.Request) {
	if !a.config().Bridges.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	var buf bytes.Buffer
	if err := a.bridges.writeList(&buf); err != nil {
		writeError(w, r, http.StatusInternalServerError, "lijst niet beschikbaar")
		return
	}
	writeJSON(w, r, http.StatusOK, 600, json.RawMessage(buf.Bytes()))
}

// ---------------------------------------------------------------------------
// Persistence (only with cache.snapshot_path): which bridges NDW has reported.

func (a *App) bridgeStatePath() string {
	if p := a.config().Cache.SnapshotPath; p != "" {
		return p + ".bridges.json"
	}
	return ""
}

func (a *App) saveBridgeState() {
	path, s := a.bridgeStatePath(), a.bridges
	s.mu.Lock()
	if path == "" || !s.dirty {
		s.mu.Unlock()
		return
	}
	b, err := json.Marshal(map[string]any{"seen": s.seen})
	s.dirty = false
	s.mu.Unlock()
	if err == nil {
		tmp := path + ".tmp"
		if err = os.WriteFile(tmp, b, 0o600); err == nil {
			err = os.Rename(tmp, path)
		}
	}
	if err != nil {
		slog.Error("bridge state write failed", "path", path, "err", err)
	}
}

func (a *App) loadBridgeState() {
	path := a.bridgeStatePath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var st struct {
		Seen map[string]int64 `json:"seen"`
	}
	if json.Unmarshal(b, &st) != nil {
		return
	}
	s := a.bridges
	s.mu.Lock()
	for id, t := range st.Seen {
		if bridgeIDRe.MatchString(id) && time.Now().Unix()-t < 90*86400 {
			s.seen[id] = t
		}
	}
	s.mu.Unlock()
}
