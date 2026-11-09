package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Hardloopagenda: upcoming road races, park runs and fun runs from hardlopen.nl, the Atletiekunie's running calendar
// (HTML list, no key). Once a day the server reads the list pages for running.types (about
// 25 pages of 10 events, one second apart) and looks up each place with PDOK (cached; only
// new places cost a request). The visitor's browser filters by distance from its own place
// and by race distance, so the server keeps one list of about 250 events (some 50 kB).

var runTypes = map[string]bool{"wegevenement": true, "trailrun": true, "cross": true, "estafette": true, "ultrarun": true,
	"trainingsloop": true, "funrun": true, "parkloop": true, "studentenevenement": true, "coopertest": true, "survivalrun": true}

const (
	runMaxPages = 40
	runMaxPlace = 2000 // cached place lookups
)

var runPageGap = time.Second

type RunEvent struct {
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Date      string    `json:"date"` // YYYY-MM-DD, the first day
	Types     []string  `json:"types,omitempty"`
	Dist      []float64 `json:"dist,omitempty"` // km
	Place     string    `json:"place,omitempty"`
	Lat       float64   `json:"lat,omitempty"`
	Lon       float64   `json:"lon,omitempty"`
	Price     string    `json:"price,omitempty"`
	Certified bool      `json:"certified,omitempty"`
}

type RunData struct {
	Events []RunEvent `json:"events"`
}

var (
	runItemRe  = regexp.MustCompile(`<a href="([^"]+)"\s+class="vertical-list-item\b`)
	runDayRe   = regexp.MustCompile(`__date__day">\s*(\d{1,2})\s*<`)
	runMonRe   = regexp.MustCompile(`__date__month hide-sm-down">\s*([a-z]{3}) (\d{4})\s*<`)
	runTitleRe = regexp.MustCompile(`__title">\s*<span>([^<]*)</span>`)
	runTypeRe  = regexp.MustCompile(`(?s)#icon-run"></use>\s*</svg>\s*<div>(.*?)</div>`)
	runSpanRe  = regexp.MustCompile(`<span>([^<•]+)</span>`)
	runDistRe  = regexp.MustCompile(`data-tippy-content="([0-9][0-9, km-]*km)"`)
	runPlaceRe = regexp.MustCompile(`#icon-location"></use>\s*</svg>\s*<span>([^<]*)</span>`)
	runPriceRe = regexp.MustCompile(`#icon-ticket"></use>\s*</svg>\s*<span>([^<]*)</span>`)
	runMonths  = map[string]time.Month{"jan": 1, "feb": 2, "mrt": 3, "apr": 4, "mei": 5, "jun": 6, "jul": 7, "aug": 8, "sep": 9, "okt": 10, "nov": 11, "dec": 12}
)

// parseRunEvents reads one list page. Links must stay on the calendar's own site (origin).
func parseRunEvents(body []byte, origin string) []RunEvent {
	s := string(body)
	idx := runItemRe.FindAllStringSubmatchIndex(s, -1)
	var out []RunEvent
	for i, m := range idx {
		end := len(s)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		b := s[m[1]:end]
		if j := strings.Index(b, "vertical-list-item__attributes"); j >= 0 {
			if k := strings.Index(b[j:], "</a>"); k >= 0 {
				b = b[:j+k]
			}
		}
		ev := RunEvent{URL: html.UnescapeString(s[m[2]:m[3]])}
		if !strings.HasPrefix(ev.URL, origin+"/") {
			continue
		}
		day, mon, title := runDayRe.FindStringSubmatch(b), runMonRe.FindStringSubmatch(b), runTitleRe.FindStringSubmatch(b)
		if day == nil || mon == nil || title == nil {
			continue
		}
		d, _ := strconv.Atoi(day[1])
		y, _ := strconv.Atoi(mon[2])
		mo, ok := runMonths[mon[1]]
		if !ok || d < 1 || d > 31 {
			continue
		}
		ev.Date = time.Date(y, mo, d, 12, 0, 0, 0, time.UTC).Format("2006-01-02")
		ev.Title = truncate(strings.Join(strings.Fields(html.UnescapeString(title[1])), " "), 140)
		if ev.Title == "" {
			continue
		}
		if t := runTypeRe.FindStringSubmatch(b); t != nil {
			for _, sp := range runSpanRe.FindAllStringSubmatch(t[1], 6) {
				if v := strings.TrimSpace(html.UnescapeString(sp[1])); v != "" {
					ev.Types = append(ev.Types, truncate(v, 40))
				}
			}
		}
		if dm := runDistRe.FindStringSubmatch(b); dm != nil {
			for _, p := range strings.Split(dm[1], " - ") {
				v, err := strconv.ParseFloat(strings.ReplaceAll(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p), "km")), ",", "."), 64)
				if err == nil && v > 0 && v < 1000 {
					ev.Dist = append(ev.Dist, v)
				}
			}
		}
		if p := runPlaceRe.FindStringSubmatch(b); p != nil {
			ev.Place = truncate(strings.TrimSpace(html.UnescapeString(p[1])), 60)
		}
		if p := runPriceRe.FindStringSubmatch(b); p != nil {
			ev.Price = truncate(strings.Join(strings.Fields(html.UnescapeString(p[1])), " "), 40)
		}
		ev.Certified = strings.Contains(b, `data-tippy-content="Gecertificeerd parcours"`)
		out = append(out, ev)
	}
	return out
}

type runPlace struct {
	Name     string
	Lat, Lon float64
}

// runPlaces caches PDOK lookups by place (and by title for events without a place); a miss is
// cached too, so a place PDOK doesn't know costs one request a process lifetime.
var runPlaces = struct {
	sync.Mutex
	m map[string]*runPlace
}{m: map[string]*runPlace{}}

var runPlaceClean = regexp.MustCompile(`(?i)\s+ca\.?$`)

// pdokPlaces asks PDOK for woonplaatsen or gemeenten that match q.
func (a *App) pdokPlaces(ctx context.Context, q, fq string, rows int) ([]runPlace, error) {
	v := url.Values{"q": {q}, "fq": {fq}, "fl": {"type,woonplaatsnaam,gemeentenaam,centroide_ll"}, "rows": {strconv.Itoa(rows)}}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: pdokURL + "?" + v.Encode(), Accept: "application/json"})
	if err != nil {
		return nil, err
	}
	var r struct {
		Response struct {
			Docs []struct {
				Type       string `json:"type"`
				Woonplaats string `json:"woonplaatsnaam"`
				Gemeente   string `json:"gemeentenaam"`
				Point      string `json:"centroide_ll"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return nil, fmt.Errorf("pdok: %w", err)
	}
	var out []runPlace
	for _, d := range r.Response.Docs {
		m := pdokPointRe.FindStringSubmatch(d.Point)
		name := d.Woonplaats
		if d.Type == "gemeente" {
			name = d.Gemeente
		}
		if m == nil || name == "" {
			continue
		}
		lon, _ := strconv.ParseFloat(m[1], 64)
		lat, _ := strconv.ParseFloat(m[2], 64)
		out = append(out, runPlace{Name: name, Lat: lat, Lon: lon})
	}
	return out, nil
}

var runAlias = map[string]string{"'s-Gravenhage": "Den Haag"}

// titlePlace finds a place named in the title as a whole word ("Omloop van Zoetermeer"); short
// names are skipped, since they are often surnames ("Wim van Beek").
func titlePlace(title string, cands []runPlace) *runPlace {
	low := " " + strings.ToLower(title) + " "
	for _, c := range cands {
		for _, n := range []string{c.Name, runAlias[c.Name]} {
			if len([]rune(n)) < 5 {
				continue
			}
			ln := strings.ToLower(n)
			i := strings.Index(low, ln)
			if i < 1 {
				continue
			}
			before, after := rune(low[i-1]), rune(low[i+len(ln)])
			if strings.ContainsRune(" (/-", before) && strings.ContainsRune(" )/-,.", after) {
				return &runPlace{Name: n, Lat: c.Lat, Lon: c.Lon}
			}
		}
	}
	return nil
}

// locateRun gives an event coordinates: its place, or a place in its title. A PDOK error leaves
// the event without coordinates and uncached, so the next run tries again.
func (a *App) locateRun(ctx context.Context, ev *RunEvent) {
	key, title := "p:"+ev.Place, ev.Place == ""
	if title {
		key = "t:" + ev.Title
	}
	runPlaces.Lock()
	p, ok := runPlaces.m[key]
	runPlaces.Unlock()
	if !ok {
		var err error
		if title {
			var c []runPlace
			c, err = a.pdokPlaces(ctx, strings.Join(strings.FieldsFunc(ev.Title, func(r rune) bool { return strings.ContainsRune("()[]:!?\"&+*", r) }), " "), "type:(woonplaats OR gemeente)", 5)
			p = titlePlace(ev.Title, c)
		} else {
			name := runPlaceClean.ReplaceAllString(strings.TrimSpace(strings.Split(ev.Place, ",")[0]), "")
			var c []runPlace
			if c, err = a.pdokPlaces(ctx, name, "type:woonplaats", 1); err == nil && len(c) == 0 {
				c, err = a.pdokPlaces(ctx, name, "type:gemeente", 1)
			}
			if len(c) > 0 {
				p = &c[0]
			}
		}
		if err != nil {
			return
		}
		runPlaces.Lock()
		if len(runPlaces.m) >= runMaxPlace {
			clear(runPlaces.m)
		}
		runPlaces.m[key] = p
		runPlaces.Unlock()
	}
	if p != nil {
		ev.Lat, ev.Lon = p.Lat, p.Lon
		if ev.Place == "" {
			ev.Place = p.Name
		}
	}
}

func (a *App) runRunning(ctx context.Context) error {
	const key = "hardlopen:events"
	cfg := a.config().Running
	origin := frameOrigin(cfg.URL)
	var all []RunEvent
	seen := map[string]bool{}
	var err error
	for page := 1; page <= runMaxPages; page++ {
		if page > 1 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(runPageGap):
			}
		}
		q := url.Values{"type[]": cfg.Types, "page": {strconv.Itoa(page)}}
		resp, e := a.fetcher.Do(ctx, FetchReq{URL: cfg.URL + "?" + q.Encode(), Accept: "text/html"})
		if e != nil {
			err = e
			break
		}
		evs := parseRunEvents(resp.Body, origin)
		fresh := 0
		for _, ev := range evs {
			if !seen[ev.URL] {
				seen[ev.URL] = true
				all = append(all, ev)
				fresh++
			}
		}
		if fresh == 0 { // past the last page (the site repeats the last one or shows none)
			break
		}
	}
	if err == nil && len(all) == 0 {
		err = errors.New("geen evenementen gevonden")
	}
	if err != nil {
		err = fmt.Errorf("hardlopen.nl: %w", err)
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	for i := range all {
		a.locateRun(ctx, &all[i])
	}
	a.threats.ok(key, RunData{Events: all}, "", "")
	return nil
}

func (a *App) handleRunning(w http.ResponseWriter, r *http.Request) {
	if !a.config().Running.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("hardlopen:events")
	e["enabled"] = true
	if v, ok := a.threats.get("hardlopen:events").Data.(RunData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 600, e)
}
