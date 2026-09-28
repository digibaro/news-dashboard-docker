package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Kwetsbaarheden in mijn software: for the products in vulns.products, the CVEs
// published in the last vulns.days days (NVD API 2.0), with their CVSS score,
// the EPSS probability of exploitation in the next 30 days (FIRST.org) and
// whether CISA knows them as actively exploited (KEV catalogue). All open, no
// key needed; without an NVD key the API allows 5 requests per 30 seconds, so
// products are asked one by one with a pause.

type VulnItem struct {
	ID        string    `json:"id"`
	Published time.Time `json:"published"`
	Score     float64   `json:"score,omitempty"`
	Severity  string    `json:"severity,omitempty"` // CRITICAL, HIGH, MEDIUM, LOW
	EPSS      float64   `json:"epss,omitempty"`     // 0-1
	KEV       bool      `json:"kev,omitempty"`
	Summary   string    `json:"summary"`
	URL       string    `json:"url"`
}

type VulnProduct struct {
	Product  string     `json:"product"`
	Count    int        `json:"count"`
	Critical int        `json:"critical"`
	High     int        `json:"high"`
	KEV      int        `json:"kev"`
	Items    []VulnItem `json:"items"`
	Error    string     `json:"error,omitempty"`
}

type VulnData struct {
	Days     int           `json:"days"`
	Products []VulnProduct `json:"products"`
}

func parseNVD(body []byte) ([]VulnItem, error) {
	var r struct {
		Total int `json:"totalResults"`
		Vulns []struct {
			CVE struct {
				ID        string `json:"id"`
				Published string `json:"published"`
				Status    string `json:"vulnStatus"`
				Desc      []struct {
					Lang  string `json:"lang"`
					Value string `json:"value"`
				} `json:"descriptions"`
				Metrics map[string][]struct {
					Data struct {
						Score    float64 `json:"baseScore"`
						Severity string  `json:"baseSeverity"`
					} `json:"cvssData"`
					Severity string `json:"baseSeverity"` // CVSS v2 has it here
				} `json:"metrics"`
			} `json:"cve"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := []VulnItem{}
	for _, v := range r.Vulns {
		c := v.CVE
		if !cveRe.MatchString(c.ID) || strings.EqualFold(c.Status, "Rejected") {
			continue
		}
		it := VulnItem{ID: c.ID, URL: "https://nvd.nist.gov/vuln/detail/" + c.ID}
		it.Published, _ = time.Parse("2006-01-02T15:04:05.000", c.Published)
		for _, d := range c.Desc {
			if d.Lang == "en" {
				it.Summary = truncate(plainText(d.Value), 200)
				break
			}
		}
		for _, k := range []string{"cvssMetricV40", "cvssMetricV31", "cvssMetricV30", "cvssMetricV2"} {
			if m := c.Metrics[k]; len(m) > 0 {
				it.Score, it.Severity = m[0].Data.Score, strings.ToUpper(firstNonEmpty(m[0].Data.Severity, m[0].Severity))
				break
			}
		}
		out = append(out, it)
	}
	return out, nil
}

func parseEPSS(body []byte) (map[string]float64, error) {
	var r struct {
		Data []struct {
			CVE  string `json:"cve"`
			EPSS string `json:"epss"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := map[string]float64{}
	for _, d := range r.Data {
		if f, err := strconv.ParseFloat(d.EPSS, 64); err == nil {
			out[d.CVE] = f
		}
	}
	return out, nil
}

// parseKEVIDs: the ids in CISA's Known Exploited Vulnerabilities catalogue.
func parseKEVIDs(body []byte) (any, error) {
	var f struct {
		Vulns []struct {
			CVE string `json:"cveID"`
		} `json:"vulnerabilities"`
	}
	if err := json.Unmarshal(body, &f); err != nil {
		return nil, fmt.Errorf("cisa kev: %w", err)
	}
	if len(f.Vulns) == 0 {
		return nil, errors.New("cisa kev: empty")
	}
	ids := make(map[string]bool, len(f.Vulns))
	for _, v := range f.Vulns {
		ids[v.CVE] = true
	}
	return ids, nil
}

// rankVulns: actively exploited first, then by CVSS score and EPSS.
func rankVulns(items []VulnItem) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.KEV != b.KEV {
			return a.KEV
		}
		if a.Score != b.Score {
			return a.Score > b.Score
		}
		return a.EPSS > b.EPSS
	})
}

func (a *App) runVulns(ctx context.Context) error {
	const key = "nvd:vulns"
	cfg := a.config()
	vc := cfg.Vulns
	now := time.Now().UTC()
	pause := 7 * time.Second // NVD without a key: 5 requests per 30 s
	hdr := map[string]string{}
	if cfg.Keys.NVDAPIKey != "" {
		pause, hdr["apiKey"] = time.Second, cfg.Keys.NVDAPIKey
	}
	d := VulnData{Days: vc.Days, Products: []VulnProduct{}}
	var allIDs []string
	failed := 0
	for i, prod := range vc.Products {
		if i > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(pause):
			}
		}
		q := url.Values{"keywordSearch": {prod}, "pubStartDate": {now.AddDate(0, 0, -vc.Days).Format("2006-01-02T15:04:05.000Z")},
			"pubEndDate": {now.Format("2006-01-02T15:04:05.000Z")}, "resultsPerPage": {"200"}}
		p := VulnProduct{Product: prod, Items: []VulnItem{}}
		resp, err := a.fetcher.Do(ctx, FetchReq{URL: vc.NVDURL + "?" + q.Encode(), Accept: "application/json", Header: hdr})
		var items []VulnItem
		if err == nil {
			items, err = parseNVD(resp.Body)
		}
		if err != nil {
			p.Error = shortErr(err).Error()
			failed++
			d.Products = append(d.Products, p)
			continue
		}
		p.Items = items
		for _, it := range items {
			allIDs = append(allIDs, it.ID)
		}
		d.Products = append(d.Products, p)
	}
	if failed == len(vc.Products) && failed > 0 {
		err := errors.New("nvd: " + d.Products[0].Error)
		a.threats.fail(key, err)
		slog.Warn("fetch failed", "source", key, "err", err)
		return err
	}
	epss := map[string]float64{}
	for i := 0; i < len(allIDs); i += 100 { // EPSS takes up to 100 ids per request
		batch := allIDs[i:min(i+100, len(allIDs))]
		if resp, err := a.fetcher.Do(ctx, FetchReq{URL: vc.EPSSURL + "?cve=" + strings.Join(batch, ","), Accept: "application/json"}); err == nil {
			if m, err := parseEPSS(resp.Body); err == nil {
				for k, v := range m {
					epss[k] = v
				}
			}
		}
	}
	kev, _ := a.threats.get("vulns:kev").Data.(map[string]bool)
	for pi := range d.Products {
		p := &d.Products[pi]
		for i := range p.Items {
			it := &p.Items[i]
			it.EPSS, it.KEV = epss[it.ID], kev[it.ID]
			switch {
			case it.Score >= 9:
				p.Critical++
			case it.Score >= 7:
				p.High++
			}
			if it.KEV {
				p.KEV++
			}
		}
		p.Count = len(p.Items)
		rankVulns(p.Items)
		if len(p.Items) > 5 {
			p.Items = p.Items[:5]
		}
	}
	a.threats.ok(key, d, "", "")
	return nil
}

func (a *App) handleVulns(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Vulns.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	if len(cfg.Vulns.Products) == 0 {
		writeJSON(w, r, http.StatusOK, 300, map[string]any{"enabled": true, "needs_products": true})
		return
	}
	e := a.feedEntry("nvd:vulns")
	e["enabled"] = true
	if v, ok := a.threats.get("nvd:vulns").Data.(VulnData); ok {
		e["data"] = v
	}
	writeJSON(w, r, http.StatusOK, 300, e)
}
