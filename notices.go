package main

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Bekendmakingen in je buurt: permits, traffic decisions and other official notices near
// the visitor's address, from the KOOP repository behind officielebekendmakingen.nl (SRU,
// open data, no key). The repository cannot search by distance, so the server asks for the
// notices of the address's municipality (last notices.days days, in pages of 250) and keeps
// only those with a location: id, title, type, date and coordinates. Per municipality that is
// a few hundred kB at most; at most 4 municipalities are kept, each for notices.ttl.

// homeCaches: lookups on request for the visitor's address (Netcongestie, Bekendmakingen).
type homeCaches struct {
	gridArea *ttlCache[string]      // postcode -> congestion area id ("" = not on the map)
	gridInfo *ttlCache[GridArea]    // area id -> figures
	addr     *ttlCache[pdokAddr]    // postcode|number -> municipality and coordinates
	notices  *ttlCache[[]noticeRec] // municipality -> located notices
}

func newHomeCaches() *homeCaches {
	return &homeCaches{gridArea: newTTLCache[string](500), gridInfo: newTTLCache[GridArea](100), addr: newTTLCache[pdokAddr](500), notices: newTTLCache[[]noticeRec](4)}
}

type noticeRec struct {
	id, title, typ, date string
	pts                  [][2]float32 // lat, lon; a notice can name several addresses
}

type Notice struct {
	Title string `json:"title"`
	Type  string `json:"type,omitempty"`
	Date  string `json:"date"`
	URL   string `json:"url"`
	M     int    `json:"m"` // distance to the address, metres
}

type pdokAddr struct {
	Gemeente string
	Street   string
	Lat, Lon float64
}

var (
	noticeIDRe     = regexp.MustCompile(`^[a-z]{2,10}-\d{4}-[0-9a-z]{1,20}$`)
	pointLLRe      = regexp.MustCompile(`^\s*(-?\d+(?:\.\d+)?)\s+(-?\d+(?:\.\d+)?)\s*$`)
	pdokPointRe    = regexp.MustCompile(`^POINT\((-?\d+(?:\.\d+)?) (-?\d+(?:\.\d+)?)\)$`)
	errNoAddress   = errors.New("address not found")
	noticePageSize = 250
	noticeMaxRecs  = 4000
)

// pdokAddress looks up an address's municipality, street and coordinates (PDOK Locatieserver).
func (a *App) pdokAddress(ctx context.Context, pc string, nr int) (pdokAddr, error) {
	q := url.Values{"q": {fmt.Sprintf("postcode:%s and huisnummer:%d", pc, nr)}, "fq": {"type:adres"},
		"fl": {"gemeentenaam,straatnaam,centroide_ll"}, "rows": {"1"}}
	resp, err := a.fetcher.Do(ctx, FetchReq{URL: pdokURL + "?" + q.Encode(), Accept: "application/json"})
	if err != nil {
		return pdokAddr{}, err
	}
	var r struct {
		Response struct {
			Docs []struct {
				Gemeente string `json:"gemeentenaam"`
				Straat   string `json:"straatnaam"`
				Point    string `json:"centroide_ll"`
			} `json:"docs"`
		} `json:"response"`
	}
	if err := json.Unmarshal(resp.Body, &r); err != nil {
		return pdokAddr{}, fmt.Errorf("pdok: %w", err)
	}
	if len(r.Response.Docs) == 0 {
		return pdokAddr{}, errNoAddress
	}
	d := r.Response.Docs[0]
	m := pdokPointRe.FindStringSubmatch(d.Point)
	if m == nil || d.Gemeente == "" {
		return pdokAddr{}, errNoAddress
	}
	lon, _ := strconv.ParseFloat(m[1], 64)
	lat, _ := strconv.ParseFloat(m[2], 64)
	return pdokAddr{Gemeente: d.Gemeente, Street: d.Straat, Lat: lat, Lon: lon}, nil
}

// parseNoticePage reads one SRU page: the records with at least one location point, and
// the total number of records for the query.
func parseNoticePage(body []byte) (recs []noticeRec, total int, err error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	var cur *noticeRec
	var field string
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("bekendmakingen: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "record":
				cur = &noticeRec{}
			case "numberOfRecords", "identifier", "title", "type", "available", "locatiepunt", "message":
				field = t.Name.Local
				text.Reset()
			}
		case xml.CharData:
			if field != "" && text.Len() < 1000 {
				text.Write(t)
			}
		case xml.EndElement:
			if t.Name.Local == "record" {
				if cur != nil && len(cur.pts) > 0 && noticeIDRe.MatchString(cur.id) && cur.title != "" {
					recs = append(recs, *cur)
				}
				cur = nil
				continue
			}
			if t.Name.Local != field {
				continue
			}
			v := strings.TrimSpace(text.String())
			field = ""
			if t.Name.Local == "numberOfRecords" {
				total, _ = strconv.Atoi(v)
				continue
			}
			if t.Name.Local == "message" && cur == nil {
				return nil, 0, fmt.Errorf("bekendmakingen: %s", truncate(v, 80))
			}
			if cur == nil {
				continue
			}
			switch t.Name.Local {
			case "identifier":
				if cur.id == "" {
					cur.id = v
				}
			case "title":
				if cur.title == "" {
					cur.title = truncate(strings.Join(strings.Fields(v), " "), 200)
				}
			case "type":
				if cur.typ == "" {
					cur.typ = truncate(v, 40)
				}
			case "available":
				if len(v) >= 10 {
					cur.date = v[:10]
				}
			case "locatiepunt":
				if m := pointLLRe.FindStringSubmatch(v); m != nil && len(cur.pts) < 20 {
					lat, _ := strconv.ParseFloat(m[1], 64)
					lon, _ := strconv.ParseFloat(m[2], 64)
					if lat > 50 && lat < 54 && lon > 3 && lon < 8 {
						cur.pts = append(cur.pts, [2]float32{float32(lat), float32(lon)})
					}
				}
			}
		}
	}
	return recs, total, nil
}

// fetchNotices gets a municipality's located notices of the last days, page by page.
func (a *App) fetchNotices(ctx context.Context, gemeente string, days int) ([]noticeRec, error) {
	cfg := a.config().Notices
	from := time.Now().AddDate(0, 0, -days).Format("2006-01-02")
	query := fmt.Sprintf(`c.product-area==officielepublicaties AND dt.modified>=%s AND w.ligtInGemeente=="%s"`, from, strings.ReplaceAll(gemeente, `"`, ""))
	var out []noticeRec
	for start := 1; start <= noticeMaxRecs; start += noticePageSize {
		q := url.Values{"query": {query}, "maximumRecords": {strconv.Itoa(noticePageSize)}, "startRecord": {strconv.Itoa(start)}, "httpAccept": {"application/xml"}}
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: cfg.URL + "?" + q.Encode(), Accept: "application/xml", MaxBody: 8 << 20})
		if err != nil {
			return nil, err
		}
		recs, total, err := parseNoticePage(resp.Body)
		if err != nil {
			return nil, err
		}
		out = append(out, recs...)
		if start+noticePageSize > total {
			break
		}
	}
	return out, nil
}

// nearbyNotices: the notices within radius metres of the point, newest first (at most 40).
func nearbyNotices(recs []noticeRec, lat, lon float64, radius int) []Notice {
	out := []Notice{}
	for _, n := range recs {
		best := math.MaxFloat64
		for _, p := range n.pts {
			best = min(best, distM(float32(lat), float32(lon), p[0], p[1]))
		}
		if best > float64(radius) {
			continue
		}
		out = append(out, Notice{Title: n.title, Type: n.typ, Date: n.date, URL: "https://zoek.officielebekendmakingen.nl/" + n.id + ".html", M: int(math.Round(best/10) * 10)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date > out[j].Date
		}
		return out[i].M < out[j].M
	})
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}

// handleNotices answers GET /api/notices?postcode=2311EZ&number=12.
func (a *App) handleNotices(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Notices.Enabled {
		writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": false})
		return
	}
	q := r.URL.Query()
	addr, ok := parseWasteAddr(q.Get("postcode"), q.Get("number"), "")
	if !ok {
		writeError(w, r, http.StatusBadRequest, "postcode of huisnummer ongeldig")
		return
	}
	ip, ctx := a.clientIP(r), context.WithoutCancel(r.Context())
	limited := func() bool { return !a.wx.limiter.allow(ip) }
	loc, _, _, err := a.home.addr.get(fmt.Sprintf("%s|%d", addr.Postcode, addr.Number), 30*24*time.Hour, func() (pdokAddr, error) {
		if limited() {
			return pdokAddr{}, errRateLimited
		}
		return a.pdokAddress(ctx, addr.Postcode, addr.Number)
	})
	if err == nil {
		var recs []noticeRec
		var at time.Time
		var stale bool
		recs, at, stale, err = a.home.notices.get(loc.Gemeente, cfg.Notices.TTL.D(), func() ([]noticeRec, error) {
			if limited() {
				return nil, errRateLimited
			}
			return a.fetchNotices(ctx, loc.Gemeente, cfg.Notices.Days)
		})
		if err == nil {
			e := map[string]any{"enabled": true, "gemeente": loc.Gemeente, "street": loc.Street, "radius": cfg.Notices.Radius, "days": cfg.Notices.Days,
				"items": nearbyNotices(recs, loc.Lat, loc.Lon, cfg.Notices.Radius), "fetched_at": at.UTC().Truncate(time.Second)}
			if stale {
				e["error"] = "verouderd: officielebekendmakingen.nl is nu niet bereikbaar"
			}
			writeJSON(w, r, http.StatusOK, 900, e)
			return
		}
	}
	switch {
	case errors.Is(err, errRateLimited):
		writeError(w, r, http.StatusTooManyRequests, "te veel verzoeken, probeer het zo opnieuw")
	case errors.Is(err, errNoAddress):
		writeJSON(w, r, http.StatusOK, 3600, map[string]any{"enabled": true, "not_found": true})
	default:
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": true, "error": shortErr(err).Error()})
	}
}
