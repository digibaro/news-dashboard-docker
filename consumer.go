package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ---------------------------------------------------------------------------
// Productwaarschuwingen: the NVWA's safety warnings and recalls of food and products
// (its RSS of the topic Veiligheidswaarschuwingen). Last 90 days, newest first, at most 12.

type Recall struct {
	Title     string    `json:"title"`
	URL       string    `json:"url"`
	Published time.Time `json:"published"`
	Allergen  bool      `json:"allergen,omitempty"` // a warning for people with an allergy
}

var recallPrefixRe = regexp.MustCompile(`(?i)^\s*(veiligheidswaarschuwing|terugroepactie|waarschuwing)\s*[:\-–]?\s*`)

func parseRecalls(body []byte, now time.Time) ([]Recall, error) {
	items, err := parseFeed(body, "")
	if err != nil {
		return nil, fmt.Errorf("nvwa: %w", err)
	}
	out := []Recall{}
	for _, it := range items {
		t, ok := parseDate(it.Date)
		title := strings.TrimSpace(html.UnescapeString(it.Title))
		if !ok || title == "" || !strings.HasPrefix(it.Link, "https://") || now.Sub(t) > 90*24*time.Hour {
			continue
		}
		short := recallPrefixRe.ReplaceAllString(title, "")
		if short == "" {
			short = title
		}
		r := []rune(short)
		r[0] = unicode.ToUpper(r[0])
		low := strings.ToLower(title)
		out = append(out, Recall{Title: truncate(string(r), 160), URL: it.Link, Published: t,
			Allergen: strings.Contains(low, "allergen") || strings.Contains(low, "allergie")})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Published.After(out[j].Published) })
	if len(out) > 12 {
		out = out[:12]
	}
	return out, nil
}

func (a *App) handleRecalls(w http.ResponseWriter, r *http.Request) {
	if !a.config().Recalls.Enabled {
		writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": false})
		return
	}
	e := a.feedEntry("nvwa:recalls")
	e["enabled"] = true
	if v, ok := a.threats.get("nvwa:recalls").Data.([]Recall); ok {
		e["items"] = v
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}

// ---------------------------------------------------------------------------
// Phishing op NL-merken (in Datalekken): phishing sites that pose as a Dutch bank, government
// service or shop, from two public lists: PhishStats (recent reports, searched by page title
// and address) and OpenPhish's community feed (the ~300 newest phishing URLs). Only the host
// name is kept and shown defanged, never as a link; entries are kept 90 days.

type PhishSite struct {
	Brand  string    `json:"brand"`
	Host   string    `json:"host"` // defanged
	Seen   time.Time `json:"seen"`
	Source string    `json:"source"`
}

type PhishNL struct {
	Sites []PhishSite `json:"sites"`
}

var defaultPhishBrands = []string{"Rabobank", "ING", "ABN AMRO", "DigiD", "Belastingdienst", "Toeslagen", "MijnOverheid", "PostNL",
	"bunq", "ASN Bank", "SNS", "Triodos", "Knab", "RegioBank", "Marktplaats", "bol.com", "Odido", "KPN", "Ziggo", "CJIB", "Tikkie",
	"ANWB", "Thuisbezorgd", "Coolblue", "Eneco", "Vattenfall", "Essent", "Zilveren Kruis", "UWV", "SVB"}

type phishBrand struct {
	name, slug string
	title      *regexp.Regexp
}

func phishBrands(names []string) []phishBrand {
	if len(names) == 0 {
		names = defaultPhishBrands
	}
	out := make([]phishBrand, 0, len(names))
	for _, n := range names {
		n = strings.TrimSpace(n)
		var slug strings.Builder
		for _, r := range strings.ToLower(n) {
			if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
				slug.WriteRune(r)
			}
		}
		if slug.Len() < 3 {
			continue
		}
		flags := "(?i)" // short names (ING, SNS, KPN) only in capitals, so "ing" in English titles does not count
		if len([]rune(n)) <= 3 {
			flags = ""
		}
		out = append(out, phishBrand{name: n, slug: slug.String(), title: regexp.MustCompile(flags + `(^|[^\pL\pN])` + regexp.QuoteMeta(n) + `($|[^\pL\pN])`)})
	}
	return out
}

// matchHost: the brand whose name is in the host. Long names (7+ letters) may be part of a
// label or span labels (rabobank-veilig, mijn.overheid.nl.example); short ones must be a whole
// label or a hyphenated part (mijn-digid).
// The brand's own .nl and .com domains do not count.
func (b phishBrand) matchHost(host string) bool {
	host = strings.ToLower(host)
	for _, own := range []string{b.slug + ".nl", b.slug + ".com"} {
		if host == own || strings.HasSuffix(host, "."+own) {
			return false
		}
	}
	if len(b.slug) >= 7 && strings.Contains(strings.NewReplacer("-", "", ".", "").Replace(host), b.slug) { // mijn.overheid.nl.evil.example
		return true
	}
	for _, tok := range strings.FieldsFunc(host, func(r rune) bool { return r == '.' || r == '-' }) {
		if tok == b.slug {
			return true
		}
	}
	return false
}

func phishHost(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	h := strings.ToLower(u.Hostname())
	if h == "" || len(h) > 120 {
		return ""
	}
	return h
}

// phishStatsQuery: one PhishStats search for all brands, by page title and by address.
func phishStatsQuery(base string, brands []phishBrand) string {
	var parts []string
	for _, b := range brands {
		if len(b.name) >= 4 {
			parts = append(parts, "(title,like,~"+strings.ToLower(b.name)+"~)")
		}
		if len(b.slug) >= 5 {
			parts = append(parts, "(url,like,~"+b.slug+"~)")
		}
	}
	q := url.Values{"_where": {strings.Join(parts, "~or")}, "_sort": {"-id"}, "_size": {"100"}, "_fields": {"id,date,title,url"}}
	return base + "?" + q.Encode()
}

func matchPhishStats(body []byte, brands []phishBrand) ([]PhishSite, error) {
	var rows []struct {
		Date  string `json:"date"`
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("phishstats: %w", err)
	}
	var out []PhishSite
	for _, r := range rows {
		host := phishHost(r.URL)
		t, ok := parseDate(r.Date)
		if host == "" || !ok {
			continue
		}
		for _, b := range brands {
			if b.matchHost(host) || b.title.MatchString(r.Title) {
				out = append(out, PhishSite{Brand: b.name, Host: host, Seen: t, Source: "PhishStats"})
				break
			}
		}
	}
	return out, nil
}

func matchOpenPhish(body []byte, brands []phishBrand, now time.Time) []PhishSite {
	var out []PhishSite
	sc := bufio.NewScanner(bytes.NewReader(body))
	for sc.Scan() {
		host := phishHost(sc.Text())
		if host == "" {
			continue
		}
		for _, b := range brands {
			if b.matchHost(host) {
				out = append(out, PhishSite{Brand: b.name, Host: host, Seen: now, Source: "OpenPhish"})
				break
			}
		}
	}
	return out
}

// mergePhish adds new sightings to the kept list: one entry per host (the first sighting),
// newest first, at most 30, none older than 90 days. Hosts arrive raw and are stored defanged.
func mergePhish(old, add []PhishSite, now time.Time) []PhishSite {
	byHost := map[string]PhishSite{}
	for _, s := range old {
		byHost[s.Host] = s
	}
	for _, s := range add {
		s.Host = defang(s.Host)
		if p, ok := byHost[s.Host]; !ok || s.Seen.Before(p.Seen) {
			byHost[s.Host] = s
		}
	}
	out := []PhishSite{}
	for _, s := range byHost {
		if now.Sub(s.Seen) <= 90*24*time.Hour {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Seen.Equal(out[j].Seen) {
			return out[i].Seen.After(out[j].Seen)
		}
		return out[i].Host < out[j].Host
	})
	if len(out) > 30 {
		out = out[:30]
	}
	return out
}

func (a *App) runPhishNL(ctx context.Context) error {
	const key = "phish:nl"
	cfg := a.config().Breaches
	brands := phishBrands(cfg.Brands)
	now := time.Now()
	var found []PhishSite
	var errs []string
	if cfg.PhishStatsURL != "" {
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: phishStatsQuery(cfg.PhishStatsURL, brands), Accept: "application/json"})
		if err == nil {
			var s []PhishSite
			if s, err = matchPhishStats(resp.Body, brands); err == nil {
				found = append(found, s...)
			}
		}
		if err != nil {
			errs = append(errs, "phishstats: "+shortErr(err).Error())
		}
	}
	if cfg.OpenPhishURL != "" {
		if resp, err := a.fetcher.Do(ctx, FetchReq{URL: cfg.OpenPhishURL, Accept: "text/plain"}); err == nil {
			found = append(found, matchOpenPhish(resp.Body, brands, now)...)
		} else {
			errs = append(errs, "openphish: "+shortErr(err).Error())
		}
	}
	sources := 0
	for _, u := range []string{cfg.PhishStatsURL, cfg.OpenPhishURL} {
		if u != "" {
			sources++
		}
	}
	prev, _ := a.threats.get(key).Data.(PhishNL)
	if len(errs) == sources {
		err := errors.New(strings.Join(errs, "; "))
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	a.threats.ok(key, PhishNL{Sites: mergePhish(prev.Sites, found, now)}, "", "")
	return nil
}
