// Nieuwsdashboard: a single-binary Dutch news, weather and threat dashboard.
//
// main.go   — config, app lifecycle, HTTP server and API handlers
// feeds.go  — outbound fetcher, scheduler, feed parser, news cache
// panels.go — side-panel data: weather, geocoding, threat intelligence, advisories
package main

import (
	"bytes"
	"compress/gzip"
	"container/list"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // decoders for the image proxy
	"image/jpeg"
	"image/png"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	_ "time/tzdata" // scratch/distroless images have no zoneinfo

	"gopkg.in/yaml.v3"
)

//go:embed web/index.html
var indexHTML []byte

var version = "dev"

// ---------------------------------------------------------------------------
// Config

// Duration is a time.Duration that unmarshals from strings like "10m".
type Duration time.Duration

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q", n.Line, s)
	}
	*d = Duration(v)
	return nil
}

func (d Duration) D() time.Duration { return time.Duration(d) }

type Location struct {
	Name    string  `yaml:"name" json:"name"`
	Lat     float64 `yaml:"lat" json:"lat"`
	Lon     float64 `yaml:"lon" json:"lon"`
	Region  string  `yaml:"region" json:"region,omitempty"`   // province, to match weather warnings
	Country string  `yaml:"country" json:"country,omitempty"` // ISO code, e.g. NL
}

type Category struct {
	ID    string `yaml:"id" json:"id"`
	Name  string `yaml:"name" json:"name"`
	Short string `yaml:"short" json:"short,omitempty"` // chip label
	// English interface: optional translations (fall back to the Dutch text)
	NameEN  string `yaml:"name_en" json:"name_en,omitempty"`
	ShortEN string `yaml:"short_en" json:"short_en,omitempty"`
}

// Preset is a named group of sources offered on the first visit and in the settings.
type Preset struct {
	ID          string   `yaml:"id" json:"id"`
	Name        string   `yaml:"name" json:"name"`
	Description string   `yaml:"description" json:"description,omitempty"`
	NameEN      string   `yaml:"name_en" json:"name_en,omitempty"`
	DescEN      string   `yaml:"description_en" json:"description_en,omitempty"`
	Sources     []string `yaml:"sources" json:"sources"`
	Region      bool     `yaml:"region" json:"region,omitempty"` // add the sources whose region matches the visitor's province
}

type Source struct {
	ID             string   `yaml:"id" json:"id"`
	Name           string   `yaml:"name" json:"name"`
	Category       string   `yaml:"category" json:"category"`
	URL            string   `yaml:"url" json:"-"`
	Homepage       string   `yaml:"homepage" json:"homepage,omitempty"`
	Lang           string   `yaml:"lang" json:"lang,omitempty"`
	Region         string   `yaml:"region" json:"region,omitempty"` // province, for the "Mijn regio" preset
	Type           string   `yaml:"type" json:"-"`                  // rss|atom|rdf|json; empty = auto-detect
	DefaultEnabled bool     `yaml:"default_enabled" json:"default_enabled"`
	Enabled        *bool    `yaml:"enabled" json:"-"` // nil = true
	Interval       Duration `yaml:"interval" json:"-"`
	MaxAge         Duration `yaml:"max_age" json:"-"` // overrides cache.max_age, e.g. for low-volume sources
}

func (s Source) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// AdvisorySource is a security-advisory feed. Format "ncsc" parses NCSC-NL titles
// (id, version, [kans/schade]); "rss" is any RSS/Atom feed with keyword severity.
type AdvisorySource struct {
	ID       string   `yaml:"id" json:"id"`
	Name     string   `yaml:"name" json:"name"`
	URL      string   `yaml:"url" json:"-"`
	Homepage string   `yaml:"homepage" json:"homepage,omitempty"`
	Format   string   `yaml:"format" json:"-"`
	Enabled  *bool    `yaml:"enabled" json:"-"`
	Interval Duration `yaml:"interval" json:"-"`
}

func (s AdvisorySource) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

// OutageSource is a service status page. Format: statuspage (Atlassian Statuspage
// summary.json), rss (incidents in the last 24 h) or m365 (Microsoft 365 status page).
type OutageSource struct {
	ID       string `yaml:"id"`
	Name     string `yaml:"name"`
	URL      string `yaml:"url"`
	Homepage string `yaml:"homepage"`
	Format   string `yaml:"format"`
	Enabled  *bool  `yaml:"enabled"`
}

func (s OutageSource) IsEnabled() bool { return s.Enabled == nil || *s.Enabled }

type Config struct {
	Server struct {
		Listen         string   `yaml:"listen"`
		BasePath       string   `yaml:"base_path"`
		TrustedProxies []string `yaml:"trusted_proxies"`
		LogLevel       string   `yaml:"log_level"`
		Metrics        bool     `yaml:"metrics"` // expose /metrics (Prometheus text format)
	} `yaml:"server"`
	Fetch struct {
		UserAgent       string   `yaml:"user_agent"`
		DefaultInterval Duration `yaml:"default_interval"`
		Timeout         Duration `yaml:"timeout"`
		MaxConcurrent   int      `yaml:"max_concurrent"`
	} `yaml:"fetch"`
	Cache struct {
		MaxItemsPerSource int      `yaml:"max_items_per_source"`
		MaxAge            Duration `yaml:"max_age"`
		SnapshotPath      string   `yaml:"snapshot_path"`
	} `yaml:"cache"`
	Features struct {
		AllowCustomFeeds bool `yaml:"allow_custom_feeds"`
		ShowImages       bool `yaml:"show_images"`
		ProxyImages      bool `yaml:"proxy_images"`
		Geolocation      bool `yaml:"geolocation"`
	} `yaml:"features"`
	// Refresh: how often an open browser tab asks this server for new data, per panel.
	// Upstream fetching is set separately (fetch/weather/threats/... intervals).
	Refresh map[string]Duration `yaml:"refresh"`
	Keys    struct {
		AbusechAuthKey string `yaml:"abusech_auth_key"`
	} `yaml:"keys"`
	Weather struct {
		Location   Location          `yaml:"location"`
		Interval   Duration          `yaml:"interval"`
		Units      string            `yaml:"units"`
		MeteoAlarm map[string]string `yaml:"meteoalarm"` // country code (nl, be) -> Atom feed URL
	} `yaml:"weather"`
	Threats struct {
		Enabled       bool     `yaml:"enabled"`
		Interval      Duration `yaml:"interval"`       // ISC top ports / top IPs / infocon, Feodo
		DailyInterval Duration `yaml:"daily_interval"` // ISC 30-day summary
		CISAKEV       bool     `yaml:"cisa_kev"`       // optional "actief misbruikte kwetsbaarheden"
	} `yaml:"threats"`
	Advisories []AdvisorySource `yaml:"advisories"`
	Alerts     struct {
		NCTV struct {
			Enabled  bool     `yaml:"enabled"`
			URL      string   `yaml:"url"`
			Interval Duration `yaml:"interval"`
		} `yaml:"nctv"`
		KNMI bool `yaml:"knmi"` // KNMI code in the top bar (from the MeteoAlarm NL feed)
	} `yaml:"alerts"`
	Traffic struct {
		Enabled  bool     `yaml:"enabled"`
		Interval Duration `yaml:"interval"`
		URL      string   `yaml:"url"`       // NDW DATEX II situation publication (.xml or .xml.gz)
		VILDBase string   `yaml:"vild_base"` // where VILD<version>.zip location tables live
	} `yaml:"traffic"`
	Alarms struct {
		Enabled  bool     `yaml:"enabled"`
		City     string   `yaml:"city"`     // default city slug; visitors can pick their own
		Base     string   `yaml:"base"`     // feed base: <base>/<city>.xml
		Interval Duration `yaml:"interval"` // cache per city
		// top bar: alerts per service in the last hour for an area of one or more cities
		Counts struct {
			Enabled  bool     `yaml:"enabled"`
			Label    string   `yaml:"label"`
			Cities   []string `yaml:"cities"`
			Interval Duration `yaml:"interval"`
		} `yaml:"counts"`
	} `yaml:"alarms"`
	Outages struct {
		Enabled   bool           `yaml:"enabled"`
		Interval  Duration       `yaml:"interval"`
		Providers []OutageSource `yaml:"providers"`
	} `yaml:"outages"`
	Categories []Category `yaml:"categories"`
	Presets    []Preset   `yaml:"presets"`
	Sources    []Source   `yaml:"sources"`

	trusted []netip.Prefix
}

func defaultConfig() *Config {
	c := &Config{}
	c.Server.Listen = "127.0.0.1:8080"
	c.Server.BasePath = "/"
	c.Server.TrustedProxies = []string{"127.0.0.1", "::1"}
	c.Server.LogLevel = "info"
	c.Fetch.UserAgent = "Nieuwsdashboard/1.0"
	c.Fetch.DefaultInterval = Duration(10 * time.Minute)
	c.Fetch.Timeout = Duration(10 * time.Second)
	c.Fetch.MaxConcurrent = 6
	c.Cache.MaxItemsPerSource = 50
	c.Cache.MaxAge = Duration(72 * time.Hour)
	c.Features.Geolocation = true
	c.Weather.Location = Location{Name: "Utrecht", Lat: 52.09, Lon: 5.12, Region: "Utrecht", Country: "NL"}
	c.Weather.Interval = Duration(15 * time.Minute)
	c.Weather.Units = "metric"
	c.Alerts.NCTV.Enabled = true
	c.Alerts.NCTV.URL = "https://www.nctv.nl/onderwerpen/d/dtn"
	c.Alerts.NCTV.Interval = Duration(6 * time.Hour)
	c.Alerts.KNMI = true
	c.Traffic.Enabled = true
	c.Traffic.Interval = Duration(5 * time.Minute)
	c.Traffic.URL = "https://opendata.ndw.nu/actueel_beeld.xml.gz"
	c.Traffic.VILDBase = "https://opendata.ndw.nu/"
	c.Alarms.Enabled = true
	c.Alarms.City = "utrecht"
	c.Alarms.Base = "https://zwaailicht.nl/feed/meldingen/"
	c.Alarms.Interval = Duration(2 * time.Minute)
	c.Alarms.Counts.Enabled = true
	c.Alarms.Counts.Label = "Den Haag"
	c.Alarms.Counts.Cities = []string{"den-haag"}
	c.Alarms.Counts.Interval = Duration(3 * time.Minute)
	c.Refresh = map[string]Duration{}
	for k, v := range defaultRefresh {
		c.Refresh[k] = Duration(v)
	}
	c.Outages.Enabled = true
	c.Outages.Interval = Duration(10 * time.Minute)
	c.Threats.Enabled = true
	c.Threats.Interval = Duration(15 * time.Minute)
	c.Threats.DailyInterval = Duration(time.Hour)
	c.Weather.MeteoAlarm = map[string]string{
		"nl": "https://feeds.meteoalarm.org/feeds/meteoalarm-legacy-atom-netherlands",
		"be": "https://feeds.meteoalarm.org/feeds/meteoalarm-legacy-atom-belgium",
	}
	return c
}

func loadConfig(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseConfig(b)
}

func parseConfig(b []byte) (*Config, error) {
	c := defaultConfig()
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // typos in config.yaml are errors, not silently ignored
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.applyEnv()
	if err := c.validate(); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	return c, nil
}

func (c *Config) applyEnv() {
	set := func(dst *string, key string) {
		if v, ok := os.LookupEnv(key); ok && v != "" {
			*dst = v
		}
	}
	set(&c.Server.Listen, "NDB_LISTEN")
	set(&c.Server.BasePath, "NDB_BASE_PATH")
	set(&c.Server.LogLevel, "NDB_LOG_LEVEL")
	set(&c.Fetch.UserAgent, "NDB_USER_AGENT")
	set(&c.Cache.SnapshotPath, "NDB_SNAPSHOT_PATH")
	set(&c.Keys.AbusechAuthKey, "ABUSECH_AUTH_KEY")
	if v := os.Getenv("NDB_TRUSTED_PROXIES"); v != "" { // e.g. the Docker gateway range
		c.Server.TrustedProxies = strings.Split(strings.ReplaceAll(v, " ", ""), ",")
	}
	if v := os.Getenv("NDB_METRICS"); v != "" {
		c.Server.Metrics = v == "1" || strings.EqualFold(v, "true")
	}
}

var idRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)

// defaultRefresh: browser refresh intervals per panel (config.yaml refresh:).
var defaultRefresh = map[string]time.Duration{
	"news": 5 * time.Minute, "weather": 15 * time.Minute, "alerts": 3 * time.Minute,
	"traffic": 5 * time.Minute, "alarms": 2 * time.Minute, "threats": 15 * time.Minute,
	"advisories": 30 * time.Minute, "outages": 10 * time.Minute, "ap": 30 * time.Minute,
	"health": 30 * time.Minute,
}

func (c *Config) validate() error {
	for k, v := range c.Refresh {
		if _, ok := defaultRefresh[k]; !ok {
			return fmt.Errorf("refresh.%s: unknown panel (known: news, weather, alerts, traffic, alarms, threats, advisories, outages, ap, health)", k)
		}
		if v.D() < time.Minute || v.D() > 24*time.Hour {
			return fmt.Errorf("refresh.%s: %s is outside 1m..24h", k, v.D())
		}
	}
	var errs []string
	fail := func(format string, a ...any) { errs = append(errs, fmt.Sprintf(format, a...)) }

	if c.Server.Listen == "" {
		fail("server.listen is empty")
	}
	bp := "/" + strings.Trim(c.Server.BasePath, "/") + "/"
	if bp == "//" {
		bp = "/"
	}
	c.Server.BasePath = bp
	if parseLevel(c.Server.LogLevel) == nil {
		fail("server.log_level %q must be debug, info, warn or error", c.Server.LogLevel)
	}
	c.trusted = nil
	for _, p := range c.Server.TrustedProxies {
		if pr, err := netip.ParsePrefix(p); err == nil {
			c.trusted = append(c.trusted, pr.Masked())
		} else if ip, err := netip.ParseAddr(p); err == nil {
			c.trusted = append(c.trusted, netip.PrefixFrom(ip.Unmap(), ip.Unmap().BitLen()))
		} else {
			fail("server.trusted_proxies: %q is not an IP or CIDR", p)
		}
	}
	if strings.TrimSpace(c.Fetch.UserAgent) == "" {
		fail("fetch.user_agent is empty")
	}
	if c.Fetch.DefaultInterval.D() < time.Minute {
		fail("fetch.default_interval must be at least 1m")
	}
	if t := c.Fetch.Timeout.D(); t < time.Second || t > time.Minute {
		fail("fetch.timeout must be between 1s and 1m")
	}
	if c.Fetch.MaxConcurrent < 1 || c.Fetch.MaxConcurrent > 32 {
		fail("fetch.max_concurrent must be between 1 and 32")
	}
	if c.Cache.MaxItemsPerSource < 1 || c.Cache.MaxItemsPerSource > 500 {
		fail("cache.max_items_per_source must be between 1 and 500")
	}
	if c.Cache.MaxAge.D() < time.Hour {
		fail("cache.max_age must be at least 1h")
	}
	l := c.Weather.Location
	if l.Lat < -90 || l.Lat > 90 || l.Lon < -180 || l.Lon > 180 {
		fail("weather.location lat/lon out of range")
	}
	if c.Weather.Interval.D() < 5*time.Minute {
		fail("weather.interval must be at least 5m")
	}
	if c.Weather.Units != "metric" {
		fail("weather.units: only \"metric\" is supported")
	}
	for cc, u := range c.Weather.MeteoAlarm {
		if cc != "nl" && cc != "be" {
			fail("weather.meteoalarm: unsupported country %q (use nl or be)", cc)
		}
		if u != "" && !isHTTPURL(u) {
			fail("weather.meteoalarm.%s: must be an http(s) URL", cc)
		}
	}

	if c.Threats.Interval.D() < 15*time.Minute {
		fail("threats.interval must be at least 15m (SANS ISC asks clients not to poll more often)")
	}
	if c.Threats.DailyInterval.D() < time.Hour {
		fail("threats.daily_interval must be at least 1h")
	}
	if c.Alerts.NCTV.Enabled && (!isHTTPURL(c.Alerts.NCTV.URL) || c.Alerts.NCTV.Interval.D() < time.Hour) {
		fail("alerts.nctv: url must be http(s) and interval at least 1h")
	}
	if c.Traffic.Enabled && (!isHTTPURL(c.Traffic.URL) || !isHTTPURL(c.Traffic.VILDBase) || c.Traffic.Interval.D() < 2*time.Minute) {
		fail("traffic: url and vild_base must be http(s), interval at least 2m")
	}
	if c.Alarms.Enabled && (!citySlugRe.MatchString(c.Alarms.City) || !isHTTPURL(c.Alarms.Base) || c.Alarms.Interval.D() < time.Minute) {
		fail("alarms: city must be a slug like den-haag, base an http(s) URL, interval at least 1m")
	}
	if cc := c.Alarms.Counts; cc.Enabled {
		// a busy feed covers little time (the national ambulance feed ~17 minutes), so polls stay frequent
		if cc.Interval.D() < time.Minute || cc.Interval.D() > 10*time.Minute {
			fail("alarms.counts.interval must be between 1m and 10m")
		}
		if strings.TrimSpace(cc.Label) == "" || len(cc.Cities) == 0 || len(cc.Cities) > 20 {
			fail("alarms.counts: needs a label and 1 to 20 cities")
		}
		for _, city := range cc.Cities {
			if !citySlugRe.MatchString(city) {
				fail("alarms.counts.cities: %q is not a city slug (e.g. den-haag)", city)
			}
		}
	}
	if c.Outages.Interval.D() < 5*time.Minute {
		fail("outages.interval must be at least 5m")
	}
	outIDs := map[string]bool{}
	for i, s := range c.Outages.Providers {
		where := fmt.Sprintf("outages.providers[%d] (%s)", i, s.ID)
		if !idRe.MatchString(s.ID) || outIDs[s.ID] {
			fail("%s: invalid or duplicate id", where)
		}
		outIDs[s.ID] = true
		if strings.TrimSpace(s.Name) == "" || !isHTTPURL(s.URL) {
			fail("%s: needs a name and an http(s) url", where)
		}
		if s.Format != "statuspage" && s.Format != "rss" && s.Format != "m365" {
			fail("%s: format must be statuspage, rss or m365", where)
		}
	}
	advIDs := map[string]bool{}
	for i, s := range c.Advisories {
		where := fmt.Sprintf("advisories[%d] (%s)", i, s.ID)
		if !idRe.MatchString(s.ID) || advIDs[s.ID] {
			fail("%s: invalid or duplicate id", where)
		}
		advIDs[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			fail("%s: name is empty", where)
		}
		if s.IsEnabled() && !isHTTPURL(s.URL) {
			fail("%s: url must be an absolute http(s) URL", where)
		}
		if s.Format != "ncsc" && s.Format != "rss" {
			fail("%s: format must be ncsc or rss", where)
		}
		if s.Interval != 0 && s.Interval.D() < 5*time.Minute {
			fail("%s: interval must be at least 5m", where)
		}
	}

	cats := map[string]bool{}
	for i, cat := range c.Categories {
		switch {
		case !idRe.MatchString(cat.ID):
			fail("categories[%d]: invalid id %q", i, cat.ID)
		case cats[cat.ID]:
			fail("categories: duplicate id %q", cat.ID)
		case strings.TrimSpace(cat.Name) == "":
			fail("categories[%d]: name is empty", i)
		}
		cats[cat.ID] = true
	}
	ids := map[string]bool{}
	for i, s := range c.Sources {
		where := fmt.Sprintf("sources[%d] (%s)", i, s.ID)
		if !idRe.MatchString(s.ID) {
			fail("%s: invalid id, use lowercase letters, digits and dashes", where)
		}
		if ids[s.ID] {
			fail("%s: duplicate id", where)
		}
		ids[s.ID] = true
		if strings.TrimSpace(s.Name) == "" {
			fail("%s: name is empty", where)
		}
		if !cats[s.Category] {
			fail("%s: unknown category %q", where, s.Category)
		}
		if s.IsEnabled() && !isHTTPURL(s.URL) {
			fail("%s: url must be an absolute http(s) URL", where)
		}
		if s.Homepage != "" && !isHTTPURL(s.Homepage) {
			fail("%s: homepage must be an absolute http(s) URL", where)
		}
		if s.Interval != 0 && s.Interval.D() < time.Minute {
			fail("%s: interval must be at least 1m", where)
		}
		if s.MaxAge != 0 && (s.MaxAge.D() < time.Hour || s.MaxAge.D() > 365*24*time.Hour) {
			fail("%s: max_age must be between 1h and 8760h", where)
		}
		switch s.Type {
		case "", "rss", "atom", "rdf", "json":
		default:
			fail("%s: type must be rss, atom, rdf or json", where)
		}
	}
	presetIDs := map[string]bool{}
	for i, p := range c.Presets {
		where := fmt.Sprintf("presets[%d] (%s)", i, p.ID)
		if !idRe.MatchString(p.ID) || presetIDs[p.ID] {
			fail("%s: invalid or duplicate id", where)
		}
		presetIDs[p.ID] = true
		if strings.TrimSpace(p.Name) == "" {
			fail("%s: name is empty", where)
		}
		if len(p.Sources) == 0 && !p.Region {
			fail("%s: needs sources or region: true", where)
		}
		for _, id := range p.Sources {
			if src, ok := c.sourceByID(id); !ok || !src.IsEnabled() {
				fail("%s: unknown or disabled source %q", where, id)
			}
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func (c *Config) sourceByID(id string) (Source, bool) {
	for _, s := range c.Sources {
		if s.ID == id && s.IsEnabled() {
			return s, true
		}
	}
	return Source{}, false
}

func (c *Config) maxAge(s Source) time.Duration {
	if s.MaxAge != 0 {
		return s.MaxAge.D()
	}
	return c.Cache.MaxAge.D()
}

func (c *Config) interval(s Source) time.Duration {
	if s.Interval != 0 {
		return s.Interval.D()
	}
	return c.Fetch.DefaultInterval.D()
}

func parseLevel(s string) *slog.Level {
	var l slog.Level
	switch strings.ToLower(s) {
	case "debug":
		l = slog.LevelDebug
	case "info", "":
		l = slog.LevelInfo
	case "warn", "warning":
		l = slog.LevelWarn
	case "error":
		l = slog.LevelError
	default:
		return nil
	}
	return &l
}

// ---------------------------------------------------------------------------
// App

type App struct {
	cfgPath string
	level   *slog.LevelVar
	started time.Time

	mu     sync.RWMutex
	cfg    *Config
	cfgMod time.Time

	fetcher *Fetcher
	sched   *Scheduler
	news    *NewsCache
	wx      *weatherCaches
	threats *stateStore // threat panels and advisories
	geo     *geoCache
	images  *imageProxy
	metrics *httpMetrics
	vild    atomic.Pointer[vildTable] // NDW location table for road names
	alarms  *ttlCache[[]Alarm]        // P2000 alerts per city slug
	p2k     map[string]*p2kCounter    // national alerts per service, last hour
}

func (a *App) config() *Config {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.cfg
}

// applyConfig installs a validated config and reconciles the scheduler.
func (a *App) applyConfig(cfg *Config) {
	a.mu.Lock()
	old := a.cfg
	a.cfg = cfg
	a.mu.Unlock()
	a.level.Set(*parseLevel(cfg.Server.LogLevel))
	if old != nil {
		if old.Server.Listen != cfg.Server.Listen || old.Server.BasePath != cfg.Server.BasePath {
			slog.Warn("server.listen/base_path changed; restart required to apply")
		}
		if old.Fetch.MaxConcurrent != cfg.Fetch.MaxConcurrent {
			slog.Warn("fetch.max_concurrent changed; restart required to apply")
		}
	}

	var jobs []Job
	keep := map[string]bool{}
	for _, s := range cfg.Sources {
		if !s.IsEnabled() {
			continue
		}
		s := s
		keep[s.ID] = true
		jobs = append(jobs, Job{
			Key:      "news:" + s.ID,
			Sig:      s.URL + "|" + s.Type,
			Interval: cfg.interval(s),
			Run:      func(ctx context.Context) error { return a.fetchSource(ctx, s) },
		})
	}
	a.news.prune(keep)
	jobs = append(jobs, a.threatJobs(cfg)...)
	a.sched.Set(append(jobs, a.p2kJobs(cfg)...))
}

func (a *App) reload(reason string) {
	cfg, err := loadConfig(a.cfgPath)
	if err != nil {
		slog.Error("config reload failed, keeping previous config", "reason", reason, "err", err)
		return
	}
	if st, err := os.Stat(a.cfgPath); err == nil {
		a.mu.Lock()
		a.cfgMod = st.ModTime()
		a.mu.Unlock()
	}
	a.applyConfig(cfg)
	slog.Info("config reloaded", "reason", reason, "sources", len(cfg.Sources))
}

// watchConfig reloads on SIGHUP and when the file's mtime changes (checked every 60 s).
func (a *App) watchConfig(ctx context.Context) {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	t := time.NewTicker(60 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hup:
			a.reload("SIGHUP")
		case <-t.C:
			st, err := os.Stat(a.cfgPath)
			if err != nil {
				continue
			}
			a.mu.RLock()
			changed := !st.ModTime().Equal(a.cfgMod)
			a.mu.RUnlock()
			if changed {
				a.reload("file changed")
			}
		}
	}
}

func (a *App) snapshotLoop(ctx context.Context) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.saveSnapshot()
		}
	}
}

func (a *App) saveSnapshot() {
	path := a.config().Cache.SnapshotPath
	if path == "" {
		return
	}
	if err := a.news.saveSnapshot(path); err != nil {
		slog.Error("snapshot write failed", "path", path, "err", err)
		return
	}
	slog.Debug("snapshot written", "path", path)
}

func run(cfgPath string) error {
	cfg, err := loadConfig(cfgPath)
	if err != nil {
		return err
	}
	level := new(slog.LevelVar)
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level})))

	a := &App{cfgPath: cfgPath, level: level, started: time.Now(), news: newNewsCache(), sched: newScheduler(), wx: newWeatherCaches(),
		threats: newStateStore(), geo: newGeoCache(10000), metrics: newHTTPMetrics(),
		alarms: newTTLCache[[]Alarm](500), p2k: newP2KCounters()}
	if st, err := os.Stat(cfgPath); err == nil {
		a.cfgMod = st.ModTime()
	}
	a.images = newImageProxy(func() string { return a.config().Fetch.UserAgent })
	a.fetcher = newFetcher(cfg.Fetch.MaxConcurrent,
		func() string { return a.config().Fetch.UserAgent },
		func() time.Duration { return a.config().Fetch.Timeout.D() })
	if p := cfg.Cache.SnapshotPath; p != "" {
		if n, err := a.news.loadSnapshot(p, cfg); err != nil && !errors.Is(err, os.ErrNotExist) {
			slog.Warn("snapshot load failed", "path", p, "err", err)
		} else if err == nil {
			slog.Info("snapshot loaded", "path", p, "sources", n)
		}
	}
	a.applyConfig(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go a.sched.Loop(ctx)
	go a.watchConfig(ctx)
	if cfg.Cache.SnapshotPath != "" {
		go a.snapshotLoop(ctx)
	}

	srv := &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           a.routes(cfg.Server.BasePath),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	slog.Info("nieuwsdashboard started", "version", version, "listen", cfg.Server.Listen,
		"base_path", cfg.Server.BasePath, "sources", len(cfg.Sources))

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	slog.Info("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
	a.saveSnapshot()
	return nil
}

func main() {
	cfgPath := flag.String("config", envOr("NDB_CONFIG", "config.yaml"), "path to config.yaml (env NDB_CONFIG)")
	checkFeeds := flag.Bool("check-feeds", false, "fetch and parse every source, print a report and exit")
	only := flag.String("only", "", "with -check-feeds: comma-separated source ids to check")
	healthcheck := flag.Bool("healthcheck", false, "query /healthz on the local server and exit 0/1 (for Docker HEALTHCHECK)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	switch {
	case *showVersion:
		fmt.Println("nieuwsdashboard", version)
	case *healthcheck:
		os.Exit(runHealthcheck(*cfgPath))
	case *checkFeeds:
		cfg, err := loadConfig(*cfgPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(runCheckFeeds(cfg, *only))
	default:
		if err := run(*cfgPath); err != nil {
			fmt.Fprintln(os.Stderr, "fatal:", err)
			os.Exit(1)
		}
	}
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func runHealthcheck(cfgPath string) int {
	listen, base := envOr("NDB_LISTEN", "127.0.0.1:8080"), envOr("NDB_BASE_PATH", "/")
	if cfg, err := loadConfig(cfgPath); err == nil {
		listen, base = cfg.Server.Listen, cfg.Server.BasePath
	}
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	base = "/" + strings.Trim(base, "/") + "/"
	if base == "//" {
		base = "/"
	}
	c := &http.Client{Timeout: 3 * time.Second}
	resp, err := c.Get("http://" + net.JoinHostPort(host, port) + base + "healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// ---------------------------------------------------------------------------
// HTTP

// csp allows only this origin. Inline scripts are allowed by their SHA-256 hash
// (computed from the embedded page at startup), not by 'unsafe-inline'. Styles
// keep 'unsafe-inline' because the page uses computed style attributes.
var csp = buildCSP(indexHTML)

var inlineScriptRe = regexp.MustCompile(`(?s)<script>(.*?)</script>`)

func buildCSP(page []byte) string {
	var hashes []string
	for _, m := range inlineScriptRe.FindAllSubmatch(page, -1) {
		sum := sha256.Sum256(m[1])
		hashes = append(hashes, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}
	return "default-src 'self'; img-src 'self' https: data:; style-src 'self' 'unsafe-inline'; " +
		"script-src 'self' " + strings.Join(hashes, " ") + "; connect-src 'self'; worker-src 'self'; manifest-src 'self'; " +
		"frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "geolocation=(self), camera=(), microphone=(), payment=(), usb=(), "+
			"interest-cohort=(), browsing-topics=(), accelerometer=(), gyroscope=(), magnetometer=()")
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			h.Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) routes(basePath string) http.Handler {
	mux := http.NewServeMux()
	handle := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, a.metrics.count(pattern, h)) }
	idx := newStaticAsset(indexHTML, "text/html; charset=utf-8")
	handle("GET /{$}", idx.serve)
	for name, asset := range pwaAssets(idx.etag) {
		handle("GET /"+name, asset.serve)
	}
	handle("GET /api/img", a.handleImage)
	handle("GET /api/catalog", a.handleCatalog)
	handle("GET /api/news", a.handleNews)
	handle("GET /api/weather", a.handleWeather)
	handle("GET /api/geocode", a.handleGeocode)
	handle("GET /api/threats", a.handleThreats)
	handle("GET /api/advisories", a.handleAdvisories)
	handle("GET /api/alerts", a.handleAlerts)
	handle("GET /api/traffic", a.handleTraffic)
	handle("GET /api/outages", a.handleOutages)
	handle("GET /api/alarms", a.handleAlarms)
	handle("GET /healthz", a.handleHealth)
	handle("GET /metrics", a.handleMetrics)

	var h http.Handler = mux
	if basePath != "/" {
		prefix := strings.TrimSuffix(basePath, "/")
		inner := http.StripPrefix(prefix, mux)
		h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == prefix {
				http.Redirect(w, r, basePath, http.StatusMovedPermanently)
				return
			}
			inner.ServeHTTP(w, r)
		})
	}
	return securityHeaders(h)
}

// staticAsset holds a precomputed gzip variant and ETag of an embedded file.
type staticAsset struct {
	body, gz    []byte
	etag, ctype string
}

func newStaticAsset(b []byte, ctype string) *staticAsset {
	sum := sha256.Sum256(b)
	return &staticAsset{body: b, gz: gzipBytes(b), etag: `"` + hex.EncodeToString(sum[:8]) + `"`, ctype: ctype}
}

func (s *staticAsset) serve(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Type", s.ctype)
	h.Set("Cache-Control", "no-cache")
	h.Set("Vary", "Accept-Encoding")
	h.Set("ETag", s.etag)
	if etagMatch(r.Header.Get("If-None-Match"), s.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	body := s.body
	if acceptsGzip(r) {
		h.Set("Content-Encoding", "gzip")
		body = s.gz
	}
	h.Set("Content-Length", strconv.Itoa(len(body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

var gzPool = sync.Pool{New: func() any { w, _ := gzip.NewWriterLevel(io.Discard, gzip.DefaultCompression); return w }}

func gzipBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzPool.Get().(*gzip.Writer)
	zw.Reset(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	gzPool.Put(zw)
	return buf.Bytes()
}

func acceptsGzip(r *http.Request) bool {
	for _, part := range strings.Split(r.Header.Get("Accept-Encoding"), ",") {
		enc, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(enc), "gzip") {
			q, ok := strings.CutPrefix(strings.ReplaceAll(params, " ", ""), "q=")
			if !ok {
				return true
			}
			v, err := strconv.ParseFloat(q, 64)
			return err == nil && v > 0
		}
	}
	return false
}

func etagMatch(header, etag string) bool {
	if header == "" {
		return false
	}
	want := strings.TrimPrefix(etag, "W/")
	for _, t := range strings.Split(header, ",") {
		t = strings.TrimSpace(t)
		if t == "*" || strings.TrimPrefix(t, "W/") == want {
			return true
		}
	}
	return false
}

// writeJSON writes v with a weak ETag, conditional 304, Cache-Control and optional gzip.
func writeJSON(w http.ResponseWriter, r *http.Request, status int, maxAge int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sum := sha256.Sum256(b)
	etag := `W/"` + hex.EncodeToString(sum[:8]) + `"`
	h := w.Header()
	h.Set("Content-Type", "application/json; charset=utf-8")
	if maxAge > 0 {
		h.Set("Cache-Control", "max-age="+strconv.Itoa(maxAge))
	} else {
		h.Set("Cache-Control", "no-store")
	}
	h.Set("ETag", etag)
	h.Set("Vary", "Accept-Encoding")
	if status == http.StatusOK && etagMatch(r.Header.Get("If-None-Match"), etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if len(b) > 512 && acceptsGzip(r) {
		b = gzipBytes(b)
		h.Set("Content-Encoding", "gzip")
	}
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(b)
	}
}

func writeError(w http.ResponseWriter, r *http.Request, status int, msg string) {
	writeJSON(w, r, status, 0, map[string]string{"error": msg})
}

// clientIP returns the caller's address, honouring X-Forwarded-For only from trusted proxies.
func (a *App) clientIP(r *http.Request) netip.Addr {
	ap, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}
	}
	ip := ap.Addr().Unmap()
	trusted := func(ip netip.Addr) bool {
		for _, p := range a.config().trusted {
			if p.Contains(ip) {
				return true
			}
		}
		return false
	}
	if !trusted(ip) {
		return ip
	}
	// Walk X-Forwarded-For right to left, skipping trusted hops.
	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(hops) - 1; i >= 0; i-- {
		h, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			break
		}
		h = h.Unmap()
		if !trusted(h) {
			return h
		}
		ip = h
	}
	return ip
}

// ---------------------------------------------------------------------------
// API handlers

func (a *App) handleCatalog(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	sources := make([]Source, 0, len(cfg.Sources))
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			sources = append(sources, s)
		}
	}
	advisories := []AdvisorySource{}
	for _, s := range cfg.Advisories {
		if s.IsEnabled() {
			advisories = append(advisories, s)
		}
	}
	writeJSON(w, r, http.StatusOK, 60, map[string]any{
		"version":    version,
		"categories": cfg.Categories,
		"sources":    sources,
		"features": map[string]bool{
			"allow_custom_feeds": cfg.Features.AllowCustomFeeds,
			"show_images":        cfg.Features.ShowImages,
		},
		"weather_location": cfg.Weather.Location,
		"threats":          cfg.Threats.Enabled,
		"traffic":          cfg.Traffic.Enabled,
		"outages":          cfg.Outages.Enabled,
		"alarms":           map[string]any{"enabled": cfg.Alarms.Enabled, "city": cfg.Alarms.City},
		"alerts":           map[string]bool{"nctv": cfg.Alerts.NCTV.Enabled, "knmi": cfg.Alerts.KNMI},
		"presets":          cfg.Presets,
		"advisories":       advisories,
		"refresh":          refreshSeconds(cfg.Refresh),
	})
}

func refreshSeconds(m map[string]Duration) map[string]int {
	out := make(map[string]int, len(defaultRefresh))
	for k, d := range defaultRefresh {
		out[k] = int(d.Seconds())
	}
	for k, d := range m {
		out[k] = int(d.D().Seconds())
	}
	return out
}

func (a *App) handleNews(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	q := r.URL.Query()
	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.Split(q.Get("sources"), ",") {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] || len(ids) >= 300 {
			continue
		}
		if _, ok := cfg.sourceByID(id); ok {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if q.Get("sources") == "" {
		for _, s := range cfg.Sources {
			if s.IsEnabled() && s.DefaultEnabled {
				ids = append(ids, s.ID)
			}
		}
	}
	limit := 60
	if v, err := strconv.Atoi(q.Get("limit")); err == nil {
		limit = min(max(v, 1), 500)
	}
	since, err := parseSince(q.Get("since"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "since: use RFC 3339 or unix seconds")
		return
	}
	var lang map[string]string // nil = no story grouping
	if q.Get("group") != "0" {
		lang = map[string]string{}
		for _, s := range cfg.Sources {
			lang[s.ID] = s.Lang
		}
	}
	lists, status := a.news.collect(ids)
	items := mergeItems(lists, since, limit, lang)
	if cfg.Features.ProxyImages {
		items = a.images.rewrite(items)
	}
	writeJSON(w, r, http.StatusOK, 60, map[string]any{
		"items":  items,
		"status": status,
	})
}

func parseSince(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		return time.Unix(n, 0), nil
	}
	return time.Parse(time.RFC3339, s)
}

func (a *App) handleHealth(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	var ids []string
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			ids = append(ids, s.ID)
		}
	}
	_, status := a.news.collect(ids)
	ok := 0
	for _, s := range status {
		if s.OK {
			ok++
		}
	}
	writeJSON(w, r, http.StatusOK, 0, map[string]any{
		"status":     "ok",
		"version":    version,
		"started":    a.started.UTC().Truncate(time.Second),
		"sources_ok": ok,
		"sources":    status,
		"feeds":      a.otherFeeds(cfg), // threat intelligence and advisory sources
	})
}

// FeedStatus is the health of a non-news source (threat intelligence, advisories).
type FeedStatus struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Kind      string     `json:"kind"` // threat | advisory
	OK        bool       `json:"ok"`
	FetchedAt *time.Time `json:"fetched_at,omitempty"`
	Error     string     `json:"error,omitempty"`
	ErrSince  *time.Time `json:"error_since,omitempty"`
}

func (a *App) otherFeeds(cfg *Config) []FeedStatus {
	var out []FeedStatus
	add := func(key, name, kind string) {
		st := a.threats.get(key)
		fs := FeedStatus{ID: key, Name: name, Kind: kind, OK: st.Err == "" && !st.FetchedAt.IsZero(), Error: st.Err}
		if !st.FetchedAt.IsZero() {
			t := st.FetchedAt.UTC().Truncate(time.Second)
			fs.FetchedAt = &t
		}
		if st.Err != "" && !st.ErrSince.IsZero() {
			t := st.ErrSince.UTC().Truncate(time.Second)
			fs.ErrSince = &t
		}
		out = append(out, fs)
	}
	if cfg.Threats.Enabled {
		add("isc:infocon", "SANS ISC · Infocon", "threat")
		add("isc:topports", "SANS ISC · aangevallen poorten", "threat")
		add("isc:daily", "SANS ISC · 30-daagse trend", "threat")
		add("isc:topips", "SANS ISC · top bron-IP's", "threat")
		add("abusech:feodo", "abuse.ch Feodo Tracker", "threat")
		if cfg.Threats.CISAKEV {
			add("cisa:kev", "CISA KEV", "threat")
		}
	}
	for _, s := range cfg.Advisories {
		if s.IsEnabled() {
			add("adv:"+s.ID, s.Name, "advisory")
		}
	}
	if cfg.Alerts.NCTV.Enabled {
		add("nctv", "NCTV · dreigingsniveau", "alert")
	}
	if cfg.Alarms.Enabled && cfg.Alarms.Counts.Enabled {
		for _, city := range cfg.Alarms.Counts.Cities {
			add("p2k:"+city, "Zwaailicht · "+city+" (tellingen)", "alert")
		}
	}
	if cfg.Traffic.Enabled {
		add("ndw:traffic", "NDW · verkeer", "traffic")
	}
	if cfg.Outages.Enabled {
		for _, p := range cfg.Outages.Providers {
			if p.IsEnabled() {
				add("outage:"+p.ID, p.Name, "outage")
			}
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Metrics (opt-in: server.metrics / NDB_METRICS=1), Prometheus text format.

type httpMetrics struct {
	mu   sync.Mutex
	hits map[[2]string]uint64 // {route, status code} -> requests
}

func newHTTPMetrics() *httpMetrics { return &httpMetrics{hits: map[[2]string]uint64{}} }

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }

func (m *httpMetrics) count(route string, h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, code: http.StatusOK}
		h(sw, r)
		m.mu.Lock()
		m.hits[[2]string{route, strconv.Itoa(sw.code)}]++
		m.mu.Unlock()
	})
}

func promLabel(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func (a *App) handleMetrics(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Server.Metrics {
		http.NotFound(w, r)
		return
	}
	var b strings.Builder
	metric := func(name, help, typ string) { fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ) }
	metric("ndb_build_info", "Build version.", "gauge")
	fmt.Fprintf(&b, "ndb_build_info{version=%q} 1\n", promLabel(version))
	metric("ndb_start_time_seconds", "Process start time.", "gauge")
	fmt.Fprintf(&b, "ndb_start_time_seconds %d\n", a.started.Unix())

	var ids []string
	for _, s := range cfg.Sources {
		if s.IsEnabled() {
			ids = append(ids, s.ID)
		}
	}
	_, status := a.news.collect(ids)
	type row struct {
		id, kind      string
		ok            bool
		items, errors int
		last          *time.Time
	}
	var rows []row
	for _, id := range ids {
		s := status[id]
		rows = append(rows, row{id, "news", s.OK, s.Items, s.ErrorCount, s.FetchedAt})
	}
	for _, f := range a.otherFeeds(cfg) {
		rows = append(rows, row{f.ID, f.Kind, f.OK, -1, 0, f.FetchedAt})
	}
	metric("ndb_source_up", "1 if the last fetch of the source succeeded.", "gauge")
	for _, r := range rows {
		up := 0
		if r.ok {
			up = 1
		}
		fmt.Fprintf(&b, "ndb_source_up{source=\"%s\",kind=\"%s\"} %d\n", promLabel(r.id), r.kind, up)
	}
	metric("ndb_source_last_success_timestamp_seconds", "Time of the last successful fetch.", "gauge")
	for _, r := range rows {
		if r.last != nil {
			fmt.Fprintf(&b, "ndb_source_last_success_timestamp_seconds{source=\"%s\",kind=\"%s\"} %d\n", promLabel(r.id), r.kind, r.last.Unix())
		}
	}
	metric("ndb_source_items", "Items cached per news source.", "gauge")
	metric("ndb_source_consecutive_errors", "Consecutive failed fetches per news source.", "gauge")
	for _, r := range rows {
		if r.kind == "news" {
			fmt.Fprintf(&b, "ndb_source_items{source=\"%s\"} %d\n", promLabel(r.id), r.items)
			fmt.Fprintf(&b, "ndb_source_consecutive_errors{source=\"%s\"} %d\n", promLabel(r.id), r.errors)
		}
	}
	metric("ndb_http_requests_total", "HTTP requests by route and status code.", "counter")
	a.metrics.mu.Lock()
	keys := make([][2]string, 0, len(a.metrics.hits))
	for k := range a.metrics.hits {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i][0]+keys[i][1] < keys[j][0]+keys[j][1] })
	for _, k := range keys {
		fmt.Fprintf(&b, "ndb_http_requests_total{route=\"%s\",code=\"%s\"} %d\n", promLabel(k[0]), k[1], a.metrics.hits[k])
	}
	a.metrics.mu.Unlock()
	a.images.mu.Lock()
	imgBytes, imgN := a.images.bytes, a.images.ll.Len()
	a.images.mu.Unlock()
	a.geo.mu.Lock()
	geoN := a.geo.ll.Len()
	a.geo.mu.Unlock()
	metric("ndb_image_cache_bytes", "Bytes held by the image proxy cache (max 50 MB).", "gauge")
	fmt.Fprintf(&b, "ndb_image_cache_bytes %d\nndb_image_cache_entries %d\n", imgBytes, imgN)
	metric("ndb_geo_cache_entries", "Cached ip-api lookups (max 10 000).", "gauge")
	fmt.Fprintf(&b, "ndb_geo_cache_entries %d\n", geoN)
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	metric("go_goroutines", "Number of goroutines.", "gauge")
	fmt.Fprintf(&b, "go_goroutines %d\n", runtime.NumGoroutine())
	metric("go_memstats_heap_alloc_bytes", "Heap bytes in use.", "gauge")
	fmt.Fprintf(&b, "go_memstats_heap_alloc_bytes %d\n", ms.HeapAlloc)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, b.String())
}

// ---------------------------------------------------------------------------
// Image proxy (features.proxy_images): only URLs the server signed itself are
// fetched, every connection is checked against private ranges after DNS
// resolution (also after redirects), images become ≤ 320 px JPEG thumbnails,
// and results live in an in-memory LRU of at most 50 MB.

const (
	thumbWidth    = 320
	imgCacheBytes = 50 << 20
	imgMaxPixels  = 40_000_000
)

type imageProxy struct {
	allowIP func(string) bool // publicIP in production; tests may narrow it
	key     []byte
	client  *http.Client
	sem     chan struct{}
	limiter *rateLimiter
	ua      func() string

	mu    sync.Mutex
	ll    *list.List
	m     map[string]*list.Element
	bytes int
}

type imgEntry struct {
	url   string
	body  []byte
	ctype string
	etag  string
	err   bool // negative cache: failed recently
	at    time.Time
}

func newImageProxy(ua func() string) *imageProxy {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic(err)
	}
	p := &imageProxy{allowIP: publicIP}
	// Control runs for every outbound connection after DNS resolution (so also after
	// each redirect and on DNS rebinding): refuse anything that is not public.
	dialer := &net.Dialer{Timeout: 5 * time.Second, Control: func(network, address string, _ syscall.RawConn) error {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			return err
		}
		if !p.allowIP(host) {
			return fmt.Errorf("blocked non-public address %s", host)
		}
		return nil
	}}
	tr := &http.Transport{
		Proxy: nil, DialContext: dialer.DialContext, ForceAttemptHTTP2: true,
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		MaxIdleConns: 16, IdleConnTimeout: 30 * time.Second,
	}
	p.key, p.ua, p.sem, p.limiter = key, ua, make(chan struct{}, 4), newRateLimiter(240, 120)
	p.ll, p.m = list.New(), map[string]*list.Element{}
	p.client = &http.Client{Transport: tr, Timeout: 15 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > 3 {
			return errors.New("more than 3 redirects")
		}
		if req.URL.Scheme != "https" {
			return errors.New("redirect to non-https URL")
		}
		return nil
	}}
	return p
}

func (p *imageProxy) sign(u string) string {
	mac := hmac.New(sha256.New, p.key)
	mac.Write([]byte(u))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:16])
}

// rewrite replaces image URLs by signed proxy URLs (relative, so base_path works).
func (p *imageProxy) rewrite(items []Item) []Item {
	for i := range items {
		if u := items[i].Image; strings.HasPrefix(u, "https://") {
			items[i].Image = "api/img?u=" + base64.RawURLEncoding.EncodeToString([]byte(u)) + "&s=" + p.sign(u)
		}
	}
	return items
}

func (p *imageProxy) verify(enc, sig string) (string, bool) {
	b, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil || len(b) > 2048 {
		return "", false
	}
	u := string(b)
	if !hmac.Equal([]byte(sig), []byte(p.sign(u))) || !strings.HasPrefix(u, "https://") {
		return "", false
	}
	return u, true
}

func (p *imageProxy) cached(u string) (*imgEntry, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	el, ok := p.m[u]
	if !ok {
		return nil, false
	}
	e := el.Value.(*imgEntry)
	if e.err && time.Since(e.at) > 10*time.Minute {
		p.ll.Remove(el)
		delete(p.m, u)
		return nil, false
	}
	p.ll.MoveToFront(el)
	return e, true
}

func (p *imageProxy) store(e *imgEntry) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if old, ok := p.m[e.url]; ok {
		p.bytes -= len(old.Value.(*imgEntry).body)
		p.ll.Remove(old)
	}
	p.m[e.url] = p.ll.PushFront(e)
	p.bytes += len(e.body)
	for p.bytes > imgCacheBytes && p.ll.Len() > 1 {
		last := p.ll.Back()
		le := last.Value.(*imgEntry)
		p.bytes -= len(le.body)
		p.ll.Remove(last)
		delete(p.m, le.url)
	}
}

func (p *imageProxy) get(ctx context.Context, u string, ip netip.Addr) (*imgEntry, error) {
	if e, ok := p.cached(u); ok {
		if e.err {
			return nil, errors.New("image unavailable")
		}
		return e, nil
	}
	if !p.limiter.allow(ip) {
		return nil, errRateLimited
	}
	select {
	case p.sem <- struct{}{}:
		defer func() { <-p.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	body, ctype, err := p.fetch(ctx, u)
	if err != nil {
		slog.Debug("image proxy", "url", u, "err", err)
		p.store(&imgEntry{url: u, err: true, at: time.Now()})
		return nil, err
	}
	sum := sha256.Sum256(body)
	e := &imgEntry{url: u, body: body, ctype: ctype, etag: `"` + hex.EncodeToString(sum[:8]) + `"`, at: time.Now()}
	p.store(e)
	return e, nil
}

func (p *imageProxy) fetch(ctx context.Context, u string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", p.ua())
	req.Header.Set("Accept", "image/avif,image/webp,image/jpeg,image/png,image/gif;q=0.8")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, "", shortErr(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxBody {
		return nil, "", errors.New("image larger than 5 MB")
	}
	return thumbnail(body)
}

// thumbnail turns JPEG/PNG/GIF into a ≤ 320 px wide JPEG (on white, so
// transparency does not turn black). WebP cannot be decoded by the standard
// library (x/image would be an extra dependency), so it is passed through
// unchanged up to 250 KB; larger WebP images get no thumbnail.
func thumbnail(body []byte) ([]byte, string, error) {
	switch ct := http.DetectContentType(body); ct {
	case "image/webp":
		if len(body) > 250<<10 {
			return nil, "", errors.New("webp too large to pass through")
		}
		return body, ct, nil
	case "image/jpeg", "image/png", "image/gif":
	default:
		return nil, "", fmt.Errorf("not an image (%s)", ct)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > imgMaxPixels {
		return nil, "", errors.New("image dimensions out of range")
	}
	if format == "jpeg" && cfg.Width <= thumbWidth && len(body) <= 80<<10 {
		return body, "image/jpeg", nil // already small
	}
	src, _, err := image.Decode(bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaleDown(src, thumbWidth), &jpeg.Options{Quality: 78}); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), "image/jpeg", nil
}

// scaleDown averages up to 4×4 source samples per destination pixel onto white.
func scaleDown(src image.Image, maxW int) *image.RGBA {
	b := src.Bounds()
	w, hh := b.Dx(), b.Dy()
	dw := min(w, maxW)
	dh := max(1, hh*dw/w)
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	fx, fy := float64(w)/float64(dw), float64(hh)/float64(dh)
	sx, sy := min(4, max(1, int(fx))), min(4, max(1, int(fy)))
	for y := 0; y < dh; y++ {
		for x := 0; x < dw; x++ {
			var r, g, bl, n uint32
			for j := 0; j < sy; j++ {
				for i := 0; i < sx; i++ {
					px := b.Min.X + int((float64(x)+(float64(i)+0.5)/float64(sx))*fx)
					py := b.Min.Y + int((float64(y)+(float64(j)+0.5)/float64(sy))*fy)
					cr, cg, cb, ca := src.At(min(px, b.Max.X-1), min(py, b.Max.Y-1)).RGBA()
					white := 0xffff - ca // premultiplied colour over white
					r, g, bl, n = r+cr+white, g+cg+white, bl+cb+white, n+1
				}
			}
			dst.SetRGBA(x, y, color.RGBA{uint8(r / n >> 8), uint8(g / n >> 8), uint8(bl / n >> 8), 0xff})
		}
	}
	return dst
}

func (a *App) handleImage(w http.ResponseWriter, r *http.Request) {
	if !a.config().Features.ProxyImages {
		http.NotFound(w, r)
		return
	}
	u, ok := a.images.verify(r.URL.Query().Get("u"), r.URL.Query().Get("s"))
	if !ok {
		http.Error(w, "invalid image signature", http.StatusForbidden)
		return
	}
	e, err := a.images.get(r.Context(), u, a.clientIP(r))
	if errors.Is(err, errRateLimited) {
		http.Error(w, "too many requests", http.StatusTooManyRequests)
		return
	}
	if err != nil {
		http.Error(w, "image unavailable", http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", e.ctype)
	h.Set("Cache-Control", "public, max-age=86400")
	h.Set("Content-Security-Policy", "default-src 'none'")
	h.Set("ETag", e.etag)
	if etagMatch(r.Header.Get("If-None-Match"), e.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	h.Set("Content-Length", strconv.Itoa(len(e.body)))
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.body)
	}
}

// ---------------------------------------------------------------------------
// Installable web app: manifest, icons (drawn at startup) and service worker.

// pwaAssets returns the manifest, icons and service worker. The icons repeat the
// favicon (three bars on a dark rounded square); the maskable one keeps the bars
// inside the 80 % safe zone.
func pwaAssets(indexETag string) map[string]*staticAsset {
	manifest, _ := json.Marshal(map[string]any{
		"name": "Nieuwsdashboard", "short_name": "Nieuws", "lang": "nl",
		"description": "Nieuws, weer en actuele cyberdreigingen op één pagina.",
		"start_url":   "./", "scope": "./", "display": "standalone",
		"background_color": "#000000", "theme_color": "#0f1115",
		"icons": []map[string]string{
			{"src": "icon-192.png", "sizes": "192x192", "type": "image/png", "purpose": "any"},
			{"src": "icon-512.png", "sizes": "512x512", "type": "image/png", "purpose": "any"},
			{"src": "icon-maskable.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable"},
		},
	})
	ver := version + "-" + strings.Trim(indexETag, `"`)
	return map[string]*staticAsset{
		"manifest.webmanifest": newStaticAsset(manifest, "application/manifest+json"),
		"icon-192.png":         newStaticAsset(drawIcon(192, false), "image/png"),
		"icon-512.png":         newStaticAsset(drawIcon(512, false), "image/png"),
		"icon-maskable.png":    newStaticAsset(drawIcon(512, true), "image/png"),
		"sw.js":                newStaticAsset([]byte(strings.Replace(serviceWorkerJS, "__VERSION__", ver, 1)), "text/javascript; charset=utf-8"),
	}
}

func drawIcon(size int, maskable bool) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	unit := float64(size) / 32 // icon is designed on a 32×32 grid
	cover := func(d float64) float64 { return math.Max(0, math.Min(1, 0.5-d*unit)) }
	bars := [][4]float64{{8, 11, 24, 11}, {8, 16, 24, 16}, {8, 21, 18, 21}}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			px, py := (float64(x)+0.5)/unit, (float64(y)+0.5)/unit
			bg := 1.0
			if !maskable { // rounded square, radius 6
				qx, qy := math.Abs(px-16)-10, math.Abs(py-16)-10
				d := math.Hypot(math.Max(qx, 0), math.Max(qy, 0)) + math.Min(math.Max(qx, qy), 0) - 6
				bg = cover(d)
			}
			if maskable { // shrink the bars into the safe zone
				px, py = 16+(px-16)/0.62, 16+(py-16)/0.62
			}
			fg := 0.0
			for _, b := range bars {
				bx, by := b[2]-b[0], b[3]-b[1]
				t := math.Max(0, math.Min(1, ((px-b[0])*bx+(py-b[1])*by)/(bx*bx+by*by)))
				d := math.Hypot(px-b[0]-bx*t, py-b[1]-by*t) - 1.25
				if maskable {
					d *= 0.62
				}
				fg = math.Max(fg, cover(d))
			}
			// bars (white) over background (#0f1115)
			c := func(bgc float64) uint8 { return uint8(math.Round(bgc*(1-fg) + 255*fg)) }
			img.SetNRGBA(x, y, color.NRGBA{c(0x0f), c(0x11), c(0x15), uint8(math.Round(255 * bg))})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

// serviceWorkerJS: network first with the last good response as offline
// fallback (marked with X-NDB-Offline), cache first for proxied thumbnails.
const serviceWorkerJS = `// Nieuwsdashboard service worker (served by the Go binary)
const CACHE = 'ndb-__VERSION__';
const MAX_IMAGES = 200;

self.addEventListener('install', (e) => {
  e.waitUntil(caches.open(CACHE)
    .then((c) => c.addAll(['./', 'manifest.webmanifest', 'icon-192.png']))
    .then(() => self.skipWaiting()));
});

self.addEventListener('activate', (e) => {
  e.waitUntil(caches.keys()
    .then((keys) => Promise.all(keys.filter((k) => k.startsWith('ndb-') && k !== CACHE).map((k) => caches.delete(k))))
    .then(() => self.clients.claim()));
});

function markOffline(resp) {
  const h = new Headers(resp.headers);
  h.set('X-NDB-Offline', '1');
  return new Response(resp.body, { status: resp.status, statusText: resp.statusText, headers: h });
}

async function trimImages(cache) {
  const keys = (await cache.keys()).filter((r) => new URL(r.url).pathname.endsWith('/api/img'));
  for (let i = 0; i < keys.length - MAX_IMAGES; i++) await cache.delete(keys[i]);
}

self.addEventListener('fetch', (e) => {
  const req = e.request;
  if (req.method !== 'GET') return;
  const url = new URL(req.url);
  const scope = new URL(self.registration.scope);
  if (url.origin !== scope.origin || !url.pathname.startsWith(scope.pathname)) return;
  const rel = url.pathname.slice(scope.pathname.length);
  if (rel === 'sw.js' || rel === 'healthz' || rel.startsWith('api/geocode')) return;

  if (rel === 'api/img') {
    e.respondWith(caches.open(CACHE).then(async (c) => {
      const hit = await c.match(req);
      if (hit) return hit;
      const r = await fetch(req);
      if (r.ok) { await c.put(req, r.clone()); trimImages(c); }
      return r;
    }));
    return;
  }

  e.respondWith(fetch(req).then((r) => {
    if (r.ok) { const copy = r.clone(); caches.open(CACHE).then((c) => c.put(req, copy)); }
    return r;
  }).catch(async () => {
    const c = await caches.open(CACHE);
    const hit = (await c.match(req)) || (req.mode === 'navigate' ? await c.match('./') : undefined);
    return hit ? markOffline(hit) : Response.error();
  }));
});
`
