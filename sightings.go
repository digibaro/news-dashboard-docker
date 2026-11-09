package main

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Zeldzame waarnemingen: the rare species seen in the Netherlands today and yesterday, from
// waarneming.nl's "recent rarities" RSS feeds (no key): 1 vogels, 2 zoogdieren, 3 reptielen en
// amfibieën. Each feed covers three days and gives a date, not a time, so "the last 24 hours"
// is today and yesterday. The site's bot check stops browser-like user agents; the dashboard's
// own fetch.user_agent gets the feed. The visitor's browser filters by group and rarity.

var sightGroups = []int{1, 2, 3}

var sightGap = time.Second

type Sighting struct {
	Group    int    `json:"group"` // 1 vogels, 2 zoogdieren, 3 reptielen en amfibieën
	Date     string `json:"date"`  // YYYY-MM-DD
	Name     string `json:"name"`
	Latin    string `json:"latin,omitempty"`
	URL      string `json:"url"`
	Count    int    `json:"count,omitempty"` // observations that day (0: one)
	VeryRare bool   `json:"very_rare,omitempty"`
	Place    string `json:"place,omitempty"` // a location with its province, or province codes
	Notes    string `json:"notes,omitempty"`
}

type SightData struct {
	Items []Sighting `json:"items"`
}

var (
	sightCountRe = regexp.MustCompile(`(\d+) waarnemingen`)
	sightRareRe  = regexp.MustCompile(`Zeldzaamheid:\s*([^<]+)`)
	sightPlaceRe = regexp.MustCompile(`Locatie:\s*(.*?)(?:<br>|$)`)
	sightNotesRe = regexp.MustCompile(`(?s)Notities:\s*(.*?)(?:<br>|$)`)
	sightTagRe   = regexp.MustCompile(`<[^>]*>`)
)

// parseSightings reads one feed and keeps the items dated on or after since. Links must stay on
// the feed's own site (origin).
func parseSightings(body []byte, group int, origin, since string) ([]Sighting, error) {
	var rss struct {
		Items []struct {
			Title string `xml:"title"`
			Link  string `xml:"link"`
			Desc  string `xml:"description"`
		} `xml:"channel>item"`
		XMLName xml.Name
	}
	if err := xml.Unmarshal(body, &rss); err != nil || rss.XMLName.Local != "rss" {
		return nil, errors.New("geen RSS (botcontrole?)")
	}
	var out []Sighting
	for _, it := range rss.Items {
		date, name, ok := strings.Cut(it.Title, ": ")
		if !ok || len(date) != 10 || date < since || !strings.HasPrefix(it.Link, origin+"/") {
			continue
		}
		s := Sighting{Group: group, Date: date, URL: it.Link}
		s.Name, s.Latin, _ = strings.Cut(name, " - ")
		s.Name, s.Latin = strings.TrimSpace(s.Name), strings.TrimSpace(s.Latin)
		d := it.Desc
		if m := sightCountRe.FindStringSubmatch(d); m != nil {
			s.Count, _ = strconv.Atoi(m[1])
		}
		if m := sightRareRe.FindStringSubmatch(d); m != nil {
			s.VeryRare = strings.TrimSpace(m[1]) == "zeer zeldzaam"
		}
		text := func(re *regexp.Regexp) string {
			if m := re.FindStringSubmatch(d); m != nil {
				return strings.Join(strings.Fields(html.UnescapeString(sightTagRe.ReplaceAllString(m[1], ""))), " ")
			}
			return ""
		}
		s.Place, s.Notes = truncate(text(sightPlaceRe), 80), truncate(text(sightNotesRe), 160)
		if s.Name != "" {
			out = append(out, s)
		}
	}
	return out, nil
}

func (a *App) runSightings(ctx context.Context) error {
	const key = "waarneming:rarities"
	cfg := a.config().Sightings
	origin := frameOrigin(cfg.URL)
	since := time.Now().In(amsterdam).AddDate(0, 0, -1).Format("2006-01-02")
	var all []Sighting
	var err error
	for i, g := range sightGroups {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sightGap):
			}
		}
		resp, e := a.fetcher.Do(ctx, FetchReq{URL: cfg.URL + strconv.Itoa(g) + "/", Accept: "application/rss+xml"})
		var items []Sighting
		if e == nil {
			items, e = parseSightings(resp.Body, g, origin, since)
		}
		if e != nil {
			err = e
			break
		}
		all = append(all, items...)
	}
	if err != nil {
		err = fmt.Errorf("waarneming.nl: %w", err)
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	// newest day first, the very rare first within a day, then the most observed
	sort.SliceStable(all, func(i, j int) bool {
		x, y := all[i], all[j]
		if x.Date != y.Date {
			return x.Date > y.Date
		}
		if x.VeryRare != y.VeryRare {
			return x.VeryRare
		}
		return x.Count > y.Count
	})
	a.threats.ok(key, SightData{Items: all}, "", "")
	return nil
}

func (a *App) handleSightings(w http.ResponseWriter, r *http.Request) {
	if !a.config().Sightings.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("waarneming:rarities")
	e["enabled"] = true
	if v, ok := a.threats.get("waarneming:rarities").Data.(SightData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}
