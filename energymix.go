package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Energieprijzen, tabs Stroommix and Netcongestie.
//
// Stroommix: Dutch electricity generation by source and the renewable share, measured
// (15-minute values, about 3 hours behind) and forecast for today, from Fraunhofer ISE's
// energy-charts (CC BY 4.0, no key). It comes from ENTSO-E, so rooftop solar that never
// reaches the public grid is largely missing; the panel says so.
//
// Gasvoorraad: how full the gas storages in the Netherlands and the EU are (GIE AGSI+,
// free key in keys.agsi_api_key).
//
// Netcongestie: the capacity map of Netbeheer Nederland for the visitor's postcode, asked
// on request and cached (postcode -> area for a week, an area's figures for 6 hours).

type MixSource struct {
	Key string  `json:"key"` // wind | solar | gas | coal | nuclear | biomass | other
	MW  float64 `json:"mw"`
}

type MixPoint struct {
	T     int64   `json:"t"` // unix seconds, start of the hour
	Share float64 `json:"share"`
}

type EnergyMix struct {
	At       time.Time   `json:"at"`       // time of the last measured value
	Share    float64     `json:"share"`    // renewable share of generation, %
	Sources  []MixSource `json:"sources"`  // largest first
	TotalMW  float64     `json:"total_mw"` // generation
	LoadMW   float64     `json:"load_mw"`  // consumption
	Forecast []MixPoint  `json:"forecast"` // renewable share per hour, today (local)
}

var mixGroups = map[string]string{
	"Wind offshore": "wind", "Wind onshore": "wind", "Solar": "solar", "Fossil gas": "gas",
	"Fossil hard coal": "coal", "Fossil brown coal / lignite": "coal", "Fossil oil": "other", "Nuclear": "nuclear",
	"Biomass": "biomass", "Waste": "other", "Others": "other", "Hydro Run-of-River": "other", "Hydro water reservoir": "other",
	"Geothermal": "other", "Other renewables": "other",
}

type ecSeries struct {
	Unix  []int64 `json:"unix_seconds"`
	Types []struct {
		Name string     `json:"name"`
		Data []*float64 `json:"data"`
	} `json:"production_types"`
	RenShare []*float64 `json:"ren_share"`
}

// parseEnergyMix reads energy-charts' public_power: the last 15-minute slot with values.
func parseEnergyMix(body []byte) (EnergyMix, error) {
	var r ecSeries
	if err := json.Unmarshal(body, &r); err != nil {
		return EnergyMix{}, fmt.Errorf("energy-charts: %w", err)
	}
	last := -1
	for _, p := range r.Types {
		if mixGroups[p.Name] == "" {
			continue
		}
		for i := len(p.Data) - 1; i > last; i-- {
			if p.Data[i] != nil {
				last = i
				break
			}
		}
	}
	if last < 0 || last >= len(r.Unix) {
		return EnergyMix{}, errors.New("energy-charts: no values")
	}
	m := EnergyMix{At: time.Unix(r.Unix[last], 0).UTC()}
	sum := map[string]float64{}
	for _, p := range r.Types {
		var v float64
		if last < len(p.Data) && p.Data[last] != nil {
			v = *p.Data[last]
		}
		switch {
		case p.Name == "Load":
			m.LoadMW = math.Round(v)
		case p.Name == "Renewable share of generation":
			m.Share = round1(v)
		case mixGroups[p.Name] != "" && v > 0:
			sum[mixGroups[p.Name]] += v
			m.TotalMW += v
		}
	}
	for _, k := range []string{"wind", "solar", "gas", "coal", "nuclear", "biomass", "other"} {
		if sum[k] >= 1 {
			m.Sources = append(m.Sources, MixSource{Key: k, MW: math.Round(sum[k])})
		}
	}
	if len(m.Sources) == 0 {
		return EnergyMix{}, errors.New("energy-charts: no generation")
	}
	for i := 1; i < len(m.Sources); i++ { // largest first (insertion sort, at most 7)
		for j := i; j > 0 && m.Sources[j].MW > m.Sources[j-1].MW; j-- {
			m.Sources[j], m.Sources[j-1] = m.Sources[j-1], m.Sources[j]
		}
	}
	m.TotalMW = math.Round(m.TotalMW)
	if m.Share == 0 && m.TotalMW > 0 {
		m.Share = round1((sum["wind"] + sum["solar"] + sum["biomass"]) / m.TotalMW * 100)
	}
	return m, nil
}

// parseRenForecast averages energy-charts' 15-minute renewable-share forecast per hour.
func parseRenForecast(body []byte) ([]MixPoint, error) {
	var r ecSeries
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("energy-charts: %w", err)
	}
	var out []MixPoint
	var acc float64
	var n int
	var hour int64 = -1
	flush := func() {
		if n > 0 {
			out = append(out, MixPoint{T: hour, Share: round1(acc / float64(n))})
		}
		acc, n = 0, 0
	}
	for i, u := range r.Unix {
		if i >= len(r.RenShare) {
			break
		}
		if h := u - u%3600; h != hour {
			flush()
			hour = h
		}
		if v := r.RenShare[i]; v != nil && *v >= 0 && *v <= 100 {
			acc += *v
			n++
		}
	}
	flush()
	if len(out) == 0 {
		return nil, errors.New("energy-charts: no forecast")
	}
	return out, nil
}

func (a *App) runEnergyMix(ctx context.Context) error {
	const key = "ec:mix"
	base := strings.TrimSuffix(a.config().Energy.MixURL, "/")
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/public_power?country=nl", Accept: "application/json"})
	var m EnergyMix
	if err == nil {
		m, err = parseEnergyMix(resp.Body)
	}
	if err != nil {
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	// the forecast is optional: without it the tab shows only the measured mix
	if resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "/ren_share_forecast?country=nl", Accept: "application/json"}); err == nil {
		if f, err := parseRenForecast(resp.Body); err == nil {
			m.Forecast = f
		}
	}
	if m.Forecast == nil {
		m.Forecast = []MixPoint{}
	}
	a.threats.ok(key, m, "", "")
	return nil
}

// ---------------------------------------------------------------------------
// Gasvoorraad (GIE AGSI+)

type GasStorage struct {
	Name    string  `json:"name"`
	Day     string  `json:"day"`                // gas day, YYYY-MM-DD
	Full    float64 `json:"full"`               // %
	Trend   float64 `json:"trend"`              // percentage points per day
	TWh     float64 `json:"twh"`                // gas in storage
	YearAgo float64 `json:"year_ago,omitempty"` // % full on the same day last year
}

type GasStorageData struct {
	NL *GasStorage `json:"nl,omitempty"`
	EU *GasStorage `json:"eu,omitempty"`
}

// agsiNum reads AGSI's numbers, which arrive as strings ("71.23") or "-".
func agsiNum(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

// parseAGSI reads the first (newest) day of an AGSI+ answer.
func parseAGSI(body []byte) (*GasStorage, error) {
	var r struct {
		Data []map[string]any `json:"data"`
		Msg  string           `json:"message"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("agsi: %w", err)
	}
	if len(r.Data) == 0 {
		if r.Msg != "" {
			return nil, fmt.Errorf("agsi: %s", truncate(r.Msg, 80))
		}
		return nil, errors.New("agsi: no data")
	}
	d := r.Data[0]
	str := func(k string) string { s, _ := d[k].(string); return s }
	g := &GasStorage{Name: str("name"), Day: str("gasDayStart"), Full: round1(agsiNum(d["full"])), Trend: math.Round(agsiNum(d["trend"])*100) / 100,
		TWh: round1(agsiNum(d["gasInStorage"]))}
	if g.Day == "" || g.Full <= 0 || g.Full > 110 {
		return nil, errors.New("agsi: no fill level")
	}
	return g, nil
}

func (a *App) runGasStorage(ctx context.Context) error {
	const key = "agsi:storage"
	cfg := a.config()
	base := strings.TrimSuffix(cfg.Energy.GasStorageURL, "/")
	hdr := map[string]string{"x-key": cfg.Keys.AGSIAPIKey}
	get := func(q string) (*GasStorage, error) {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: base + "?" + q, Accept: "application/json", Header: hdr})
		if err != nil {
			return nil, err
		}
		return parseAGSI(resp.Body)
	}
	// the newest day, and the same day last year for comparison
	series := func(q string) (*GasStorage, error) {
		g, err := get(q + "&size=1")
		if err != nil {
			return nil, err
		}
		if day, err := time.Parse("2006-01-02", g.Day); err == nil {
			if old, err := get(q + "&date=" + day.AddDate(-1, 0, 0).Format("2006-01-02")); err == nil {
				g.YearAgo = old.Full
			}
		}
		return g, nil
	}
	var d GasStorageData
	nl, err := series("country=NL")
	if err != nil {
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	d.NL = nl
	if eu, err := series("type=eu"); err == nil {
		d.EU = eu
	}
	a.threats.ok(key, d, "", "")
	return nil
}

// ---------------------------------------------------------------------------
// Netcongestie (Netbeheer Nederland capaciteitskaart)

type GridSide struct {
	Queue    string `json:"queue,omitempty"`    // waiting list in MW ("13.9 MW")
	Requests string `json:"requests,omitempty"` // requests on the waiting list
	Solved   string `json:"solved,omitempty"`   // expected year the regional bottleneck is solved
	TenneT   int    `json:"tennet"`             // TenneT level: -1 unknown, 0 enough … 3 shortage
	TSolved  string `json:"tennet_solved,omitempty"`
}

type GridProject struct {
	Name     string `json:"name"`
	Operator string `json:"operator"`
	When     string `json:"when"`
}

type GridArea struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	Operator   string        `json:"operator,omitempty"`
	Withdrawal GridSide      `json:"withdrawal"` // afname
	Injection  GridSide      `json:"injection"`  // invoeding
	URL        string        `json:"url,omitempty"`
	Projects   []GridProject `json:"projects,omitempty"` // the first planned grid extensions
}

var (
	gridPCRe   = regexp.MustCompile(`^[1-9][0-9]{3}[A-Z]{2}$`)
	errGridNot = errors.New("postcode not on the capacity map")
)

func gridVal(s string) string {
	s = strings.TrimSpace(s)
	if s == "-" || s == "0" || s == "0 MW" {
		return ""
	}
	return truncate(s, 20)
}

func parseGridArea(body []byte) (GridArea, error) {
	var r struct {
		A *struct {
			ID, Name, Rnb                                     string
			QueueInjection, QueueWithdrawal                   string
			UniqueRequestsInjection, UniqueRequestsWithdrawal string
			YearSolvedWithdrawal, YearSolvedInjection         string
			TennetYearSolvedWithdrawal                        string
			TennetYearSolvedInjection                         string
			TennetInjectionMax, TennetWithdrawalMax           *int
			CongestionURL                                     string `json:"congestionUrl"`
			Projects                                          []struct {
				Name, GridOperator, DateString, Year string
			}
		} `json:"serviceArea"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return GridArea{}, fmt.Errorf("capaciteitskaart: %w", err)
	}
	if r.A == nil || r.A.ID == "" {
		return GridArea{}, errGridNot
	}
	lvl := func(p *int) int {
		if p == nil || *p < -1 || *p > 3 {
			return -1
		}
		return *p
	}
	s := r.A
	g := GridArea{ID: truncate(s.ID, 80), Name: truncate(firstNonEmpty(s.Name, s.ID), 80), Operator: truncate(s.Rnb, 40),
		Withdrawal: GridSide{Queue: gridVal(s.QueueWithdrawal), Requests: gridVal(s.UniqueRequestsWithdrawal), Solved: gridVal(s.YearSolvedWithdrawal),
			TenneT: lvl(s.TennetWithdrawalMax), TSolved: gridVal(s.TennetYearSolvedWithdrawal)},
		Injection: GridSide{Queue: gridVal(s.QueueInjection), Requests: gridVal(s.UniqueRequestsInjection), Solved: gridVal(s.YearSolvedInjection),
			TenneT: lvl(s.TennetInjectionMax), TSolved: gridVal(s.TennetYearSolvedInjection)}}
	if u, err := url.Parse(s.CongestionURL); err == nil && u.Scheme == "https" && u.Host != "" {
		g.URL = u.String()
	}
	// the first extensions by year: what is being built for this area
	ps := s.Projects
	for i := 1; i < len(ps); i++ {
		for j := i; j > 0 && ps[j].Year < ps[j-1].Year; j-- {
			ps[j], ps[j-1] = ps[j-1], ps[j]
		}
	}
	for _, p := range ps {
		if len(g.Projects) == 3 {
			break
		}
		if p.Name != "" {
			g.Projects = append(g.Projects, GridProject{Name: truncate(p.Name, 100), Operator: truncate(p.GridOperator, 30), When: truncate(p.DateString, 20)})
		}
	}
	return g, nil
}

func (a *App) gridPost(ctx context.Context, path string, body any) ([]byte, error) {
	b, _ := json.Marshal(body)
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: strings.TrimSuffix(a.config().Energy.GridURL, "/") + path, Method: http.MethodPost,
		Body: b, Accept: "application/json", Header: map[string]string{"Content-Type": "application/json"}})
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// handleGrid answers GET /api/grid?pc=2311EZ: the congestion area of that postcode.
func (a *App) handleGrid(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Energy.Enabled || !cfg.Energy.Grid {
		writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": false})
		return
	}
	pc := normPostcode(r.URL.Query().Get("pc"))
	if !gridPCRe.MatchString(pc) {
		writeError(w, r, http.StatusBadRequest, "postcode ongeldig")
		return
	}
	ip, ctx := a.clientIP(r), context.WithoutCancel(r.Context())
	id, _, _, err := a.home.gridArea.get(pc, 7*24*time.Hour, func() (string, error) {
		if !a.wx.limiter.allow(ip) {
			return "", errRateLimited
		}
		b, err := a.gridPost(ctx, "/find", map[string]string{"postalCode6": pc, "gridOperatorType": "RNB", "energyFlowDirection": "WITHDRAWAL"})
		if err != nil {
			return "", err
		}
		var f struct {
			A *struct{ ID string } `json:"serviceArea"`
		}
		if json.Unmarshal(b, &f) != nil {
			return "", errors.New("capaciteitskaart: unexpected answer")
		}
		if f.A == nil {
			return "", nil // not on the map: remembered too
		}
		return f.A.ID, nil
	})
	switch {
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	case err != nil:
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "error": shortErr(err).Error()})
		return
	case id == "":
		writeJSON(w, r, http.StatusOK, 3600, map[string]any{"enabled": true, "not_found": true})
		return
	}
	area, at, stale, err := a.home.gridInfo.get(id, 6*time.Hour, func() (GridArea, error) {
		if !a.wx.limiter.allow(ip) {
			return GridArea{}, errRateLimited
		}
		b, err := a.gridPost(ctx, "/get", map[string]string{"id": id})
		if err != nil {
			return GridArea{}, err
		}
		return parseGridArea(b)
	})
	switch {
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
		return
	case errors.Is(err, errGridNot):
		writeJSON(w, r, http.StatusOK, 3600, map[string]any{"enabled": true, "not_found": true})
		return
	case err != nil:
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "error": shortErr(err).Error()})
		return
	}
	e := map[string]any{"enabled": true, "area": area, "fetched_at": at.UTC().Truncate(time.Second)}
	if stale {
		e["error"] = "verouderd: de capaciteitskaart is nu niet bereikbaar"
	}
	writeJSON(w, r, http.StatusOK, 900, e)
}
