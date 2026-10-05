package main

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Alarmeringen-wachter: visitors watch specific streets ("Koningskade in Den Haag").
// P2000 titles from Zwaailicht.nl name the street and the place ("Ambulance met spoed
// naar Koningskade in 's-Gravenhage"), so a street matches when its words appear in the
// title. Matching runs on the server against the whole city feed (the panel shows only
// two alerts per service): GET /api/alarmwatch for an open page, and the push watcher
// for devices with the topic "alarmwatch".

type AlarmRule struct {
	City   string `json:"city"`            // Zwaailicht slug, e.g. den-haag
	Street string `json:"street"`          // as the visitor typed it
	Spoed  bool   `json:"spoed,omitempty"` // only urgent alerts
}

type AlarmHit struct {
	ID      string    `json:"id"`
	Title   string    `json:"title"`
	URL     string    `json:"url"`
	Time    time.Time `json:"time"`
	Urgency string    `json:"urgency,omitempty"`
	Service string    `json:"service"`
	Detail  string    `json:"detail,omitempty"`
	City    string    `json:"city"`
	Street  string    `json:"street"`
}

const maxAlarmRules = 10

var foldAccents = strings.NewReplacer("á", "a", "à", "a", "ä", "a", "â", "a", "é", "e", "è", "e", "ë", "e", "ê", "e",
	"í", "i", "ì", "i", "ï", "i", "î", "i", "ó", "o", "ò", "o", "ö", "o", "ô", "o", "ú", "u", "ù", "u", "ü", "u", "û", "u", "ç", "c", "ñ", "n", "ÿ", "y")

// foldWords: lower case, no accents, letters and digits only, single spaces, padded with
// spaces so " koningskade " only matches whole words.
func foldWords(s string) string {
	s = foldAccents.Replace(strings.ToLower(s))
	var b strings.Builder
	b.WriteByte(' ')
	space := true
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	if !space {
		b.WriteByte(' ')
	}
	return b.String()
}

func streetMatches(title, street string) bool {
	fs := foldWords(street)
	return len(fs) > 3 && strings.Contains(foldWords(title), fs)
}

func (r AlarmRule) valid() bool {
	n := len([]rune(strings.TrimSpace(r.Street)))
	return citySlugRe.MatchString(r.City) && r.City != "lifeliner" && n >= 3 && n <= 60
}

func cleanRules(in []AlarmRule) []AlarmRule {
	var out []AlarmRule
	for _, r := range in {
		r.Street = strings.Join(strings.Fields(r.Street), " ")
		r.City = strings.ToLower(strings.TrimSpace(r.City))
		if r.valid() && !slices.ContainsFunc(out, func(o AlarmRule) bool { return o.City == r.City && foldWords(o.Street) == foldWords(r.Street) }) {
			out = append(out, r)
		}
		if len(out) == maxAlarmRules {
			break
		}
	}
	return out
}

// matchAlarms: the alerts of one city feed that match one of the rules for that city.
func matchAlarms(items []Alarm, city string, rules []AlarmRule, since time.Time) []AlarmHit {
	var out []AlarmHit
	for _, it := range items {
		if it.Time.Before(since) {
			continue
		}
		for _, r := range rules {
			if r.City != city || (r.Spoed && it.Urgency != "spoed") || !streetMatches(it.Title, r.Street) {
				continue
			}
			out = append(out, AlarmHit{ID: it.id, Title: it.Title, URL: it.URL, Time: it.Time, Urgency: it.Urgency, Service: it.service, Detail: it.Detail, City: city, Street: r.Street})
			break
		}
	}
	return out
}

// handleAlarmWatch: GET /api/alarmwatch?r=den-haag|Koningskade|1&r=... (city|street|only urgent).
func (a *App) handleAlarmWatch(w http.ResponseWriter, r *http.Request) {
	cfg := a.config()
	if !cfg.Alarms.Enabled {
		writeJSON(w, r, http.StatusOK, 60, map[string]any{"enabled": false})
		return
	}
	var rules []AlarmRule
	for _, s := range r.URL.Query()["r"] {
		f := strings.SplitN(s, "|", 3)
		if len(f) >= 2 {
			rules = append(rules, AlarmRule{City: f[0], Street: f[1], Spoed: len(f) == 3 && f[2] == "1"})
		}
	}
	rules = cleanRules(rules)
	if len(rules) == 0 {
		writeError(w, r, http.StatusBadRequest, "geen geldige straat en plaats")
		return
	}
	ip, ctx, ttl := a.clientIP(r), context.WithoutCancel(r.Context()), cfg.Alarms.Interval.D()
	hits, errs := []AlarmHit{}, map[string]string{}
	since := time.Now().Add(-6 * time.Hour)
	var cities []string
	for _, rl := range rules {
		if !slices.Contains(cities, rl.City) {
			cities = append(cities, rl.City)
		}
	}
	for _, c := range cities {
		items, _, _, err := a.alarms.get(c, ttl, func() ([]Alarm, error) {
			if !a.wx.limiter.allow(ip) {
				return nil, errRateLimited
			}
			return a.fetchAlarmFeed(ctx, c)
		})
		switch {
		case errors.Is(err, errCityNotFound):
			errs[c] = "plaats niet gevonden bij Zwaailicht.nl"
		case err != nil:
			errs[c] = "Zwaailicht.nl is niet bereikbaar"
		default:
			hits = append(hits, matchAlarms(items, c, rules, since)...)
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Time.After(hits[j].Time) })
	if len(hits) > 20 {
		hits = hits[:20]
	}
	writeJSON(w, r, http.StatusOK, 0, map[string]any{"enabled": true, "hits": hits, "errors": errs, "checked_at": time.Now().UTC().Truncate(time.Second)})
}

// alarmWatchPush: messages for new alerts (last 30 minutes) in the streets that push
// subscribers watch; one message per alert, sent only to the devices whose rules match.
func (a *App) alarmWatchPush(ctx context.Context, cfg *Config, now time.Time, first bool) []pushMsg {
	if !cfg.Alarms.Enabled {
		return nil
	}
	p := a.push
	byCity := map[string][]AlarmRule{}
	p.mu.Lock()
	for _, s := range p.subs {
		if !slices.Contains(s.Topics, "alarmwatch") {
			continue
		}
		for _, r := range s.Watch {
			byCity[r.City] = append(byCity[r.City], r)
		}
	}
	p.mu.Unlock()
	var hits []AlarmHit
	for c, rules := range byCity {
		items, _, _, err := a.alarms.get(c, cfg.Alarms.Interval.D(), func() ([]Alarm, error) { return a.fetchAlarmFeed(ctx, c) })
		if err == nil {
			hits = append(hits, matchAlarms(items, c, rules, now.Add(-30*time.Minute))...)
		}
	}
	var msgs []pushMsg
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, h := range hits {
		key := "aw:" + h.ID
		if _, seen := p.st.Alerts[key]; seen || h.ID == "" {
			continue
		}
		p.st.Alerts[key] = h.Time
		if first {
			continue
		}
		h := h
		when := h.Time.In(amsterdam).Format("15:04")
		msgs = append(msgs, pushMsg{Topic: "alarmwatch", Tag: "aw-" + h.ID, Urgent: h.Urgency == "spoed", Sticky: true, TTL: 2 * 3600, URL: h.URL,
			Title: [2]string{"Alarmering: " + h.Street, "Emergency alert: " + h.Street},
			Body:  [2]string{h.Title + " (" + when + ")", h.Title + " (" + when + ")"},
			For: func(s *pushSub) bool {
				return slices.ContainsFunc(s.Watch, func(r AlarmRule) bool {
					return r.City == h.City && (!r.Spoed || h.Urgency == "spoed") && streetMatches(h.Title, r.Street)
				})
			}})
	}
	return msgs
}
