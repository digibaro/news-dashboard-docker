package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseEnergyMix(t *testing.T) {
	body := `{"unix_seconds":[1000,1900,2800],"production_types":[
		{"name":"Wind offshore","data":[100,3000,null]},{"name":"Wind onshore","data":[50,1000,null]},
		{"name":"Solar","data":[0,500,null]},{"name":"Fossil gas","data":[900,1500,null]},
		{"name":"Fossil hard coal","data":[0,0,null]},{"name":"Cross border electricity trading","data":[5,-100,null]},
		{"name":"Load","data":[1,7000,null]},{"name":"Renewable share of generation","data":[1,75,null]}]}`
	m, err := parseEnergyMix([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if m.At.Unix() != 1900 || m.Share != 75 || m.TotalMW != 6000 || m.LoadMW != 7000 {
		t.Errorf("mix: %+v", m)
	}
	if len(m.Sources) != 3 || m.Sources[0] != (MixSource{"wind", 4000}) || m.Sources[1].Key != "gas" || m.Sources[2].Key != "solar" {
		t.Errorf("sources (largest first, no zero coal, no trading): %+v", m.Sources)
	}
	if _, err := parseEnergyMix([]byte(`{"unix_seconds":[1],"production_types":[{"name":"Load","data":[5]}]}`)); err == nil {
		t.Error("no generation should fail")
	}

	f, err := parseRenForecast([]byte(`{"unix_seconds":[3600,4500,5400,6300,7200],"ren_share":[40,50,60,null,80]}`))
	if err != nil || len(f) != 2 || f[0] != (MixPoint{3600, 50}) || f[1] != (MixPoint{7200, 80}) {
		t.Errorf("hourly forecast: %+v %v", f, err)
	}
}

func TestParseAGSI(t *testing.T) {
	g, err := parseAGSI([]byte(`{"data":[{"name":"Netherlands","gasDayStart":"2026-10-20","full":"81.234","trend":"0.31","gasInStorage":"112.45"}]}`))
	if err != nil || g.Full != 81.2 || g.Trend != 0.31 || g.TWh != 112.5 || g.Day != "2026-10-20" {
		t.Errorf("agsi: %+v %v", g, err)
	}
	if _, err := parseAGSI([]byte(`{"data":[],"message":"Invalid or missing API key"}`)); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Errorf("missing key: %v", err)
	}
}

func TestParseGridArea(t *testing.T) {
	body := `{"serviceArea":{"id":"CM60","name":"Utrecht Lage Weide","rnb":"Stedin","queueWithdrawal":"28.1 MW","queueInjection":"-",
		"uniqueRequestsWithdrawal":"49","uniqueRequestsInjection":"-","yearSolvedWithdrawal":"2034","yearSolvedInjection":"-",
		"tennetWithdrawalMax":3,"tennetInjectionMax":0,"tennetYearSolvedWithdrawal":"2035","congestionUrl":"javascript:alert(1)",
		"projects":[{"name":"B","gridOperator":"TenneT","dateString":"2031","year":"2031"},{"name":"A","gridOperator":"Stedin","dateString":"2028","year":"2028"}]}}`
	g, err := parseGridArea([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if g.Name != "Utrecht Lage Weide" || g.Operator != "Stedin" || g.Withdrawal.Queue != "28.1 MW" || g.Withdrawal.Requests != "49" ||
		g.Withdrawal.Solved != "2034" || g.Withdrawal.TenneT != 3 || g.Injection.Queue != "" || g.Injection.TenneT != 0 || g.URL != "" {
		t.Errorf("area: %+v", g)
	}
	if len(g.Projects) != 2 || g.Projects[0].Name != "A" {
		t.Errorf("projects by year: %+v", g.Projects)
	}
	if _, err := parseGridArea([]byte(`{"serviceArea":null}`)); err != errGridNot {
		t.Errorf("null area: %v", err)
	}
}

const sruPage = `<?xml version="1.0"?><sru:searchRetrieveResponse xmlns:sru="http://docs.oasis-open.org/ns/search-ws/sruResponse"
 xmlns:dcterms="http://purl.org/dc/terms/" xmlns:w="http://standaarden.overheid.nl/wetgeving/"><sru:numberOfRecords>%d</sru:numberOfRecords><sru:records>
<sru:record><sru:recordData><w:meta><dcterms:identifier>gmb-2026-1</dcterms:identifier><dcterms:title>Verleende omgevingsvergunning, dakkapel, Teststraat 1</dcterms:title>
<dcterms:type>omgevingsvergunning</dcterms:type><dcterms:available>2026-10-20</dcterms:available>
<w:locatiepunt>52.1600 4.4900</w:locatiepunt></w:meta></sru:recordData></sru:record>
<sru:record><sru:recordData><w:meta><dcterms:identifier>gmb-2026-2</dcterms:identifier><dcterms:title>Ver weg</dcterms:title>
<dcterms:available>2026-10-21</dcterms:available><w:locatiepunt>52.2000 4.5500</w:locatiepunt></w:meta></sru:recordData></sru:record>
<sru:record><sru:recordData><w:meta><dcterms:identifier>gmb-2026-3</dcterms:identifier><dcterms:title>Zonder locatie</dcterms:title>
<dcterms:available>2026-10-21</dcterms:available></w:meta></sru:recordData></sru:record>
<sru:record><sru:recordData><w:meta><dcterms:identifier>gmb-2026-4</dcterms:identifier><dcterms:title>Twee adressen</dcterms:title>
<dcterms:available>2026-10-22</dcterms:available><w:locatiepunt>52.3000 4.9000</w:locatiepunt><w:locatiepunt>52.1610 4.4900</w:locatiepunt></w:meta></sru:recordData></sru:record>
</sru:records></sru:searchRetrieveResponse>`

func TestNotices(t *testing.T) {
	recs, total, err := parseNoticePage([]byte(fmt.Sprintf(sruPage, 4)))
	if err != nil || total != 4 || len(recs) != 3 {
		t.Fatalf("page: %d records of %d, %v", len(recs), total, err)
	}
	near := nearbyNotices(recs, 52.16, 4.49, 500)
	if len(near) != 2 || near[0].Title != "Twee adressen" || near[0].M != 110 || near[1].M != 0 || near[1].Type != "omgevingsvergunning" ||
		near[1].URL != "https://zoek.officielebekendmakingen.nl/gmb-2026-1.html" {
		t.Errorf("nearby (newest first, nearest point counts): %+v", near)
	}

	var sruCalls int
	sru := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sruCalls++
		if q := r.URL.Query().Get("query"); !strings.Contains(q, `w.ligtInGemeente=="Leiden"`) {
			t.Errorf("query: %s", q)
		}
		fmt.Fprintf(w, sruPage, 4)
	}))
	defer sru.Close()
	pdok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("q"), "9999ZZ") {
			io.WriteString(w, `{"response":{"docs":[]}}`)
			return
		}
		io.WriteString(w, `{"response":{"docs":[{"gemeentenaam":"Leiden","straatnaam":"Teststraat","centroide_ll":"POINT(4.49 52.16)"}]}}`)
	}))
	defer pdok.Close()
	defer func(u string) { pdokURL = u }(pdokURL)
	pdokURL = pdok.URL
	a := newTestApp(t, validConfig)
	a.cfg.Notices.URL = sru.URL
	h := a.routes("/")
	var r struct {
		Gemeente string
		Items    []Notice
	}
	b := get(h, "GET", "/api/notices?postcode=2311ez&number=1", nil).Body.String()
	if json.Unmarshal([]byte(b), &r); r.Gemeente != "Leiden" || len(r.Items) != 2 {
		t.Errorf("notices: %s", b)
	}
	get(h, "GET", "/api/notices?postcode=2311EZ&number=1", nil)
	if sruCalls != 1 {
		t.Errorf("municipality cached: %d SRU calls", sruCalls)
	}
	if b := get(h, "GET", "/api/notices?postcode=9999ZZ&number=1", nil).Body.String(); !strings.Contains(b, `"not_found":true`) {
		t.Errorf("unknown address: %s", b)
	}
	if c := get(h, "GET", "/api/notices?postcode=abc&number=1", nil).Code; c != http.StatusBadRequest {
		t.Errorf("bad postcode: %d", c)
	}
}

func TestGridHandler(t *testing.T) {
	var calls []string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		body, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/find" && strings.Contains(string(body), `"postalCode6":"3511AB"`):
			io.WriteString(w, `{"serviceArea":{"id":"CM60"}}`)
		case r.URL.Path == "/find":
			io.WriteString(w, `{"serviceArea":null}`)
		case r.URL.Path == "/get" && strings.Contains(string(body), `"id":"CM60"`):
			io.WriteString(w, `{"serviceArea":{"id":"CM60","name":"Utrecht Lage Weide","rnb":"Stedin","queueWithdrawal":"28.1 MW","tennetWithdrawalMax":3}}`)
		}
	}))
	defer up.Close()
	a := newTestApp(t, validConfig)
	a.cfg.Energy.GridURL = up.URL
	h := a.routes("/")
	b := get(h, "GET", "/api/grid?pc=3511+ab", nil).Body.String()
	if !strings.Contains(b, `"name":"Utrecht Lage Weide"`) || !strings.Contains(b, `"queue":"28.1 MW"`) {
		t.Errorf("grid: %s", b)
	}
	get(h, "GET", "/api/grid?pc=3511AB", nil)
	if len(calls) != 2 || calls[0] != "POST /find" || calls[1] != "POST /get" {
		t.Errorf("cached lookups: %v", calls)
	}
	if b := get(h, "GET", "/api/grid?pc=1000AA", nil).Body.String(); !strings.Contains(b, `"not_found":true`) {
		t.Errorf("unknown postcode: %s", b)
	}
	if c := get(h, "GET", "/api/grid?pc=12", nil).Code; c != http.StatusBadRequest {
		t.Errorf("bad postcode: %d", c)
	}
}

func TestParseRecalls(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	rss := `<rss><channel>
<item><title>Veiligheidswaarschuwing peterselie krul 40g van ALDI (allergenen)</title><link>https://www.nvwa.nl/a</link><pubDate>Mon, 19 Oct 2026 15:29:00 GMT</pubDate></item>
<item><title>Veiligheidswaarschuwing Jumbo pulled chicken</title><link>https://www.nvwa.nl/b</link><pubDate>Tue, 20 Oct 2026 13:12:00 GMT</pubDate></item>
<item><title>Veiligheidswaarschuwing oud</title><link>https://www.nvwa.nl/c</link><pubDate>Tue, 20 Jan 2026 13:12:00 GMT</pubDate></item>
<item><title>Veiligheidswaarschuwing onveilig</title><link>http://www.nvwa.nl/d</link><pubDate>Tue, 20 Oct 2026 13:12:00 GMT</pubDate></item>
</channel></rss>`
	r, err := parseRecalls([]byte(rss), now)
	if err != nil || len(r) != 2 {
		t.Fatalf("recalls: %+v %v", r, err)
	}
	if r[0].Title != "Jumbo pulled chicken" || r[0].Allergen || r[1].Title != "Peterselie krul 40g van ALDI (allergenen)" || !r[1].Allergen {
		t.Errorf("titles and allergy: %+v", r)
	}
}

func TestPhishNL(t *testing.T) {
	brands := phishBrands(nil)
	find := func(name string) phishBrand {
		for _, b := range brands {
			if b.name == name {
				return b
			}
		}
		t.Fatalf("no brand %s", name)
		return phishBrand{}
	}
	for host, want := range map[string]bool{
		"rabobank-veilig.example.com": true, "www.rabobank.nl": false, "login.rabobank.com": false, "xrabobankx.top": true,
	} {
		if got := find("Rabobank").matchHost(host); got != want {
			t.Errorf("Rabobank %s: %v", host, got)
		}
	}
	for host, want := range map[string]bool{"mijn-ing.example": true, "billing.example.com": false, "ing.nl": false, "shopping.ing-secure.top": true} {
		if got := find("ING").matchHost(host); got != want {
			t.Errorf("ING %s: %v", host, got)
		}
	}
	if !find("ING").title.MatchString("Inloggen | ING") || find("ING").title.MatchString("Suspected Phishing | billing") || find("ING").title.MatchString("ing") {
		t.Error("ING title: capitals and whole words only")
	}
	if !find("Belastingdienst").title.MatchString("Opgave cryptoactiva | belastingdienst") {
		t.Error("long names: any case")
	}

	ps := `[{"date":"2026-10-21T18:54:57.000Z","title":"Inloggen","url":"https://www.mijn.overheid.nl.berichtenbox.xyz.example.br/login"},
		{"date":"2026-10-20T10:00:00.000Z","title":"Suspected Phishing | Cloudflare","url":"https://billing.example.com/"},
		{"date":"2026-10-19T10:00:00.000Z","title":"ABN AMRO – Inloggen","url":"https://x.example.org/a"}]`
	sites, err := matchPhishStats([]byte(ps), brands)
	if err != nil || len(sites) != 2 || sites[0].Brand != "MijnOverheid" || sites[1].Brand != "ABN AMRO" {
		t.Errorf("phishstats: %+v %v", sites, err)
	}
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	op := matchOpenPhish([]byte("https://postnl-pakket.example.top/track\nhttps://example.com/\nnot a url\nhttps://x.example/digid\n"), brands, now)
	if len(op) != 1 || op[0].Brand != "PostNL" {
		t.Errorf("openphish (host only): %+v", op)
	}
	old := []PhishSite{{Brand: "Bunq", Host: "bunq-app.example[.]com", Seen: now.AddDate(0, 0, -100)}, {Brand: "PostNL", Host: "postnl-pakket.example[.]top", Seen: now.AddDate(0, 0, -2)}}
	m := mergePhish(old, append(sites, op...), now)
	if len(m) != 3 || m[0].Brand != "MijnOverheid" || !strings.HasSuffix(m[0].Host, "example[.]br") || m[1].Host != "postnl-pakket.example[.]top" ||
		!m[1].Seen.Equal(now.AddDate(0, 0, -2)) || m[2].Brand != "ABN AMRO" {
		t.Errorf("merge (90 days, first sighting, defanged): %+v", m)
	}
	q := phishStatsQuery("https://api.phishstats.info/api/phishing", brands)
	if !strings.Contains(q, "title%2Clike%2C~rabobank~") || !strings.Contains(q, "url%2Clike%2C~belastingdienst~") || strings.Contains(q, "~ing~") {
		t.Errorf("query: %s", q)
	}
}

func TestParseCBSMonths(t *testing.T) {
	body := `{"value":[{"Perioden":"2026MM08","X":3.3},{"Perioden":"2026KW02","X":4.2},{"Perioden":"2026MM07","X":3.9},{"Perioden":"2026JJ00","X":1},{"Perioden":"2026MM09","X":null}]}`
	p, err := parseCBSMonths([]byte(body), "X")
	if err != nil || len(p) != 2 || p[0] != (EconPoint{"2026-07", 3.9}) || p[1] != (EconPoint{"2026-08", 3.3}) {
		t.Errorf("cbs: %+v %v", p, err)
	}
}

func TestGoogleNewsTitles(t *testing.T) {
	raws := []rawItem{{Title: "Recordbestelling van 850 JSF-vliegtuigen - bnr.nl", Link: "https://news.google.com/rss/articles/abc", Summary: "<a>Recordbestelling</a> bnr.nl", Date: "Wed, 21 Oct 2026 10:00:00 GMT"},
		{Title: "Rutte - Schoof - debat", Link: "https://news.google.com/rss/articles/def", Date: "Wed, 21 Oct 2026 10:00:00 GMT"}}
	now := time.Date(2026, 10, 22, 0, 0, 0, 0, time.UTC)
	items := normalizeItems("bnr", "https://news.google.com/rss/search?q=site:bnr.nl", raws, normOpts{now: now, maxAge: 72 * time.Hour, maxItems: 10})
	if len(items) != 2 || items[0].Title != "Recordbestelling van 850 JSF-vliegtuigen" || items[0].Summary != "" || items[1].Title != "Rutte - Schoof" {
		t.Errorf("google news: %+v", items)
	}
	other := normalizeItems("x", "https://example.nl/feed", raws[:1], normOpts{now: now, maxAge: 72 * time.Hour, maxItems: 10})
	if other[0].Title != "Recordbestelling van 850 JSF-vliegtuigen - bnr.nl" {
		t.Errorf("other feeds keep their titles: %q", other[0].Title)
	}
}

func TestNewConfigValidation(t *testing.T) {
	for _, tc := range []struct{ yaml, want string }{
		{"notices: { radius: 50 }\n", "notices:"},
		{"notices: { days: 60 }\n", "notices:"},
		{"recalls: { url: \"http://x.nl/rss\" }\n", "recalls:"},
		{"energy: { mix_url: \"http://x\" }\n", "mix_url"},
		{"breaches: { phishstats_url: \"\", openphish_url: \"\" }\n", "phishstats_url"},
		{"breaches: { brands: [\"ab\"] }\n", "brands"},
		{"economy: { cbs_base: \"ftp://x\" }\n", "economy:"},
	} {
		if _, err := parseConfig([]byte(validConfig + tc.yaml)); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: %v", tc.yaml, err)
		}
	}
	cfg, err := parseConfig([]byte(validConfig + "keys: { agsi_api_key: k }\nrefresh: { recalls: 2h, notices: 30m }\n"))
	if err != nil || cfg.Keys.AGSIAPIKey != "k" || !cfg.Energy.Mix || !cfg.Recalls.Enabled || cfg.Notices.Radius != 500 {
		t.Errorf("defaults: %v", err)
	}
}

func TestEnergyAndBreachesExtras(t *testing.T) {
	a := newTestApp(t, validConfig)
	a.threats.ok("ec:mix", EnergyMix{Share: 61, Sources: []MixSource{{"wind", 5000}}, TotalMW: 8000, Forecast: []MixPoint{}}, "", "")
	a.threats.ok("phish:nl", PhishNL{Sites: []PhishSite{{Brand: "PostNL", Host: "postnl-x[.]top"}}}, "", "")
	h := a.routes("/")
	b := get(h, "GET", "/api/energy", nil).Body.String()
	if !strings.Contains(b, `"share":61`) || !strings.Contains(b, `"gas_storage":{"key":false}`) || !strings.Contains(b, `"grid":true`) {
		t.Errorf("energy: %s", b)
	}
	if b := get(h, "GET", "/api/breaches", nil).Body.String(); !strings.Contains(b, `"phishing_nl":{`) || !strings.Contains(b, `postnl-x[.]top`) {
		t.Errorf("breaches: %s", b)
	}
	a.threats.ok("nvwa:recalls", []Recall{{Title: "X", URL: "https://www.nvwa.nl/x"}}, "", "")
	if b := get(h, "GET", "/api/recalls", nil).Body.String(); !strings.Contains(b, `"title":"X"`) {
		t.Errorf("recalls: %s", b)
	}
}
