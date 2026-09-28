package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseNLAlerts(t *testing.T) {
	body := `{"data":[
	 {"id":"aaf5f94b681c","message":"Brand met veel rook in Leeuwarden. Blijf uit de rook! *** Dutch Public Warning System. Fire with a lot of smoke in Leeuwarden.","type":"alert",
	  "start_at":"2026-09-20T03:10:31Z","stop_at":"2026-09-20T05:10:31Z","area":["53.0,5.0 53.0,6.0 54.0,6.0 54.0,5.0"]},
	 {"id":"b2","message":"NL-Alert ingetrokken voor brand in Zwaag.","type":"alert","start_at":"2026-09-21T05:13:53Z","area":[]},
	 {"id":"bad id!","message":"x","type":"alert","start_at":"2026-09-21T05:13:53Z"},
	 {"id":"t1","message":"Test","type":"test","start_at":"2026-09-21T05:13:53Z"}]}`
	list, err := parseNLAlerts([]byte(body))
	if err != nil || len(list) != 2 {
		t.Fatalf("got %d alerts, err %v", len(list), err)
	}
	if list[0].ID != "b2" || !list[0].Withdrawn {
		t.Errorf("newest first and withdrawn detected: %+v", list[0])
	}
	a := list[1]
	if a.Text != "Brand met veel rook in Leeuwarden. Blijf uit de rook!" || a.TextEN != "Fire with a lot of smoke in Leeuwarden." {
		t.Errorf("Dutch/English split: %q / %q", a.Text, a.TextEN)
	}
	if !a.inArea(53.2, 5.8) || a.inArea(52.1, 5.1) {
		t.Error("point in polygon")
	}
	if !a.active(time.Date(2026, 9, 20, 4, 0, 0, 0, time.UTC)) || a.active(time.Date(2026, 9, 20, 6, 0, 0, 0, time.UTC)) {
		t.Error("active window")
	}
	if _, err := parseNLAlerts([]byte(`{"message":"x"}`)); err == nil {
		t.Error("a response without data must fail")
	}
}

func TestParseFuelPrices(t *testing.T) {
	row := func(slug, name, price, change, arrow string) string {
		return `<div><div><a class="_root_3zm9c_1" href="/tanken/brandstofprijzen/product/` + slug + `" title="` + name + `">` + name + `</a></div>` +
			`<div class="_mobileLabelColumn">GLA <sup>*</sup></div><div><span class="_root_170bn_1" data-sentry-component="Price">€` + " " + price + `</span></div>` +
			`<div>Verschil <sup>**</sup></div><div><span class="_root_rojly_1 _icon-` + arrow + `_1y0x3_223"></span>` + change + `</div></div>`
	}
	page := `<html>` + row("euro95", "Euro95", "2,729", "1,2", "pijl-omlaag") + row("diesel", "Diesel", "2,788", "0,0", "pijl-linksrechts") +
		row("lpg", "LPG", "1,265", "+0,4", "pijl-omhoog") + row("super", "Super", "2,939", "0,0", "pijl-linksrechts") +
		`<div class="_tableFooter">Datum overzicht <!-- -->27 september 2026<br/>* GLA</div></html>`
	d, err := parseFuelPrices([]byte(page))
	if err != nil {
		t.Fatal(err)
	}
	if d.Date != "2026-09-27" || len(d.Prices) != 3 {
		t.Fatalf("date %q, %d prices", d.Date, len(d.Prices))
	}
	want := []FuelPrice{{"euro95", "Euro95 (E10)", 2.729, -1.2}, {"diesel", "Diesel", 2.788, 0}, {"lpg", "LPG", 1.265, 0.4}}
	for i, w := range want {
		if d.Prices[i] != w {
			t.Errorf("price %d: %+v, want %+v", i, d.Prices[i], w)
		}
	}
	if _, err := parseFuelPrices([]byte(`<html>onderhoud</html>`)); err == nil {
		t.Error("a page without prices must fail")
	}
}

func TestWasteParsers(t *testing.T) {
	now := time.Date(2026, 9, 27, 10, 0, 0, 0, amsterdam)
	op, err := parseOpzetStreams([]byte(`[{"title":"GFT","menu_title":"GFT","ophaaldatum":null},{"title":"Papier","menu_title":"Papier","ophaaldatum":"2026-10-06"},
		{"title":"Rest","menu_title":"Rest","ophaaldatum":"2026-10-02"},{"title":"Oud","menu_title":"Oud","ophaaldatum":"2026-09-01"}]`))
	if err != nil {
		t.Fatal(err)
	}
	got := sortPickups(op, now)
	cycle := sortPickups([]WastePickup{{"PMD", "2026-10-05"}, {"Restafval", "2026-10-07"}, {"PMD", "2026-10-19"}, {"GFT", "2026-10-12"},
		{"Restafval", "2026-10-21"}, {"GFT", "2026-10-26"}, {"Oud", "2026-09-01"}}, now)
	if len(cycle) != 3 || cycle[0] != (WastePickup{"PMD", "2026-10-05"}) || cycle[1] != (WastePickup{"Restafval", "2026-10-07"}) || cycle[2] != (WastePickup{"GFT", "2026-10-12"}) {
		t.Errorf("only the next date per type: %+v", cycle)
	}
	if len(got) != 2 || got[0] != (WastePickup{"Rest", "2026-10-02"}) || got[1].Type != "Papier" {
		t.Errorf("opzet: %+v", got)
	}
	ics := "BEGIN:VCALENDAR\r\nBEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20260929\r\nSUMMARY:Plastic\\, blik en\r\n  drinkpakken\r\nEND:VEVENT\r\n" +
		"BEGIN:VEVENT\r\nDTSTART:20261001T070000Z\r\nSUMMARY:GFT\r\nEND:VEVENT\r\nBEGIN:VEVENT\r\nDTSTART;VALUE=DATE:20270601\r\nSUMMARY:Ver weg\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	ic, err := parseWasteICS([]byte(ics), now)
	if err != nil || len(ic) != 2 || ic[0] != (WastePickup{"Plastic, blik en drinkpakken", "2026-09-29"}) || ic[1].Date != "2026-10-01" {
		t.Errorf("ics: %+v %v", ic, err)
	}
	if _, err := parseWasteICS([]byte("<html>"), now); err == nil {
		t.Error("non-calendar must fail")
	}
	for body, want := range map[string]string{
		`{"entity_id":"sensor.afval_rest","state":"2026-10-02","attributes":{"friendly_name":"Restafval"}}`:                     "Restafval 2026-10-02",
		`{"entity_id":"sensor.gft","state":"02-10-2026","attributes":{"friendly_name":"GFT"}}`:                                  "GFT 2026-10-02",
		`{"entity_id":"sensor.papier","state":"Morgen","attributes":{"friendly_name":"Papier","Sort-date":20260928}}`:           "Papier 2026-09-28",
		`{"entity_id":"sensor.pmd","state":"over 3 dagen","attributes":{"friendly_name":"PMD","days_until_collection_date":3}}`: "PMD 2026-09-30",
		`{"entity_id":"sensor.glas","state":"vandaag","attributes":{}}`:                                                         "sensor.glas 2026-09-27",
	} {
		p, ok := parseHAWasteState([]byte(body), now)
		if !ok || p.Type+" "+p.Date != want {
			t.Errorf("home assistant %s: %+v %v, want %s", body, p, ok, want)
		}
	}
	if _, ok := parseHAWasteState([]byte(`{"entity_id":"sensor.x","state":"unknown","attributes":{}}`), now); ok {
		t.Error("an unknown state has no date")
	}
}

func TestComputeTrending(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	var items []Item
	add := func(src, title string, age time.Duration) {
		items = append(items, Item{Source: src, Title: title, Published: now.Add(-age)})
	}
	for i, s := range []string{"nos", "nu", "rtl", "ad", "nrc"} {
		add(s, "Storm Ciarán raast over Nederland", time.Duration(i)*20*time.Minute)
	}
	for i, s := range []string{"nos", "nu", "rtl", "ad"} { // always in the news: no burst
		add(s, "Kabinet "+[]string{"twijfelt", "praat", "beslist", "wacht"}[i], time.Duration(i)*20*time.Minute)
		for d := 0; d < 12; d++ {
			add(s, "Kabinet over begroting", time.Duration(4+d*4)*time.Hour)
		}
	}
	add("nos", "Enkele melding over treinen", time.Hour)
	terms := computeTrending(items, now, 8)
	if len(terms) == 0 || terms[0].Term != "Storm Ciarán" || terms[0].Sources != 5 {
		t.Fatalf("trending: %+v", terms)
	}
	for _, tt := range terms {
		if strings.EqualFold(tt.Term, "storm") || strings.EqualFold(tt.Term, "ciarán") || strings.EqualFold(tt.Term, "kabinet") || strings.Contains(tt.Term, "treinen") {
			t.Errorf("unexpected term %q in %+v", tt.Term, terms)
		}
	}
}

// decryptPush is the browser side of RFC 8291, to check encryptPush.
func decryptPush(t *testing.T, body []byte, ua *ecdh.PrivateKey, auth []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	asPub, ct := body[21:21+idlen], body[21+idlen:]
	if rs != 4096 || idlen != 65 {
		t.Fatalf("header rs=%d idlen=%d", rs, idlen)
	}
	as, err := ecdh.P256().NewPublicKey(asPub)
	if err != nil {
		t.Fatal(err)
	}
	secret, _ := ua.ECDH(as)
	ikm, _ := hkdf.Key(sha256.New, secret, auth, "WebPush: info\x00"+string(ua.PublicKey().Bytes())+string(asPub), 32)
	cek, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Key(sha256.New, ikm, salt, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	if plain[len(plain)-1] != 2 {
		t.Fatal("missing last-record delimiter")
	}
	return plain[:len(plain)-1]
}

func TestPushEndToEnd(t *testing.T) {
	key, err := genVAPIDKey()
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	auth := make([]byte, 16)
	rand.Read(auth)

	var gotHdr http.Header
	var gotBody []byte
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHdr = r.Header
		gotBody, _ = io.ReadAll(r.Body)
		if strings.HasSuffix(r.URL.Path, "/gone") {
			w.WriteHeader(http.StatusGone)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	defer svc.Close()

	cfgY := validConfig + "push: { enabled: true, subject: \"mailto:test@example.nl\", vapid_private_key: \"" + key + "\" }\n"
	a := newTestApp(t, cfgY)
	a.push = newPushHub()
	if err := a.push.setKey(key); err != nil {
		t.Fatal(err)
	}
	sub := &pushSub{Endpoint: svc.URL + "/push/abc", P256dh: ua.PublicKey().Bytes(), Auth: auth, Topics: []string{"quakes"}, Lang: "en"}
	m := pushMsg{Topic: "quakes", Tag: "q1", Title: [2]string{"Aardbeving", "Earthquake"}, Body: [2]string{"nl", "en"}, URL: "#panel-quakes"}
	if gone, err := a.sendPush(context.Background(), sub, m); err != nil || gone {
		t.Fatalf("send: gone=%v err=%v", gone, err)
	}
	if gotHdr.Get("Content-Encoding") != "aes128gcm" || gotHdr.Get("TTL") == "" || len(gotHdr.Get("Topic")) > 32 {
		t.Errorf("headers: %v", gotHdr)
	}
	var payload map[string]string
	if err := json.Unmarshal(decryptPush(t, gotBody, ua, auth), &payload); err != nil || payload["title"] != "Earthquake" || payload["url"] != "#panel-quakes" {
		t.Errorf("payload %v %v", payload, err)
	}
	// VAPID: the JWT verifies with the public key in k=
	authz := gotHdr.Get("Authorization")
	tok, k, _ := strings.Cut(strings.TrimPrefix(authz, "vapid t="), ", k=")
	parts := strings.Split(tok, ".")
	pubBytes, _ := base64.RawURLEncoding.DecodeString(k)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), pubBytes)
	if err != nil || len(parts) != 3 {
		t.Fatalf("vapid header %q: %v", authz, err)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Error("VAPID signature does not verify")
	}
	claims, _ := base64.RawURLEncoding.DecodeString(parts[1])
	if !bytes.Contains(claims, []byte(`"aud":"`+svc.URL+`"`)) || !bytes.Contains(claims, []byte("mailto:test@example.nl")) {
		t.Errorf("claims %s", claims)
	}
	// 410 Gone drops the subscription
	a.push.subs[svc.URL+"/push/gone"] = &pushSub{Endpoint: svc.URL + "/push/gone", P256dh: ua.PublicKey().Bytes(), Auth: auth, Topics: []string{"quakes"}}
	a.broadcast(context.Background(), m)
	if _, ok := a.push.subs[svc.URL+"/push/gone"]; ok {
		t.Error("a gone subscription must be removed")
	}
}

func TestPushHTTP(t *testing.T) {
	key, _ := genVAPIDKey()
	a := newTestApp(t, validConfig+"push: { enabled: true, subject: \"mailto:t@example.nl\", vapid_private_key: \""+key+"\" }\n")
	a.push = newPushHub()
	if err := a.push.setKey(key); err != nil {
		t.Fatal(err)
	}
	h := a.routes("/")
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	sub := func(endpoint string) string {
		b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": b64url(ua.PublicKey().Bytes()), "auth": b64url(make([]byte, 16))},
			"topics": []string{"quakes", "nctv", "bogus"}, "lang": "en", "lat": 52.0712, "lon": 4.3009})
		return string(b)
	}
	post := func(path, body string, hdr map[string]string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Host = "dash.example"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	ok := map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "same-origin"}
	fcm := "https://fcm.googleapis.com/fcm/send/abc123"
	if rec := post("/api/push/subscribe", sub(fcm), ok); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"topics":["quakes","nctv"]`) {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	s := a.push.subs[fcm]
	if s == nil || s.Lang != "en" || s.Lat == nil || *s.Lat != 52.07 {
		t.Errorf("stored subscription: %+v", s)
	}
	for name, c := range map[string]struct {
		path, body string
		hdr        map[string]string
		code       int
	}{
		"cross-site":         {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site"}, 403},
		"foreign origin":     {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/json", "Origin": "https://evil.example"}, 403},
		"same origin header": {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/json", "Origin": "https://dash.example"}, 200},
		"form post":          {"/api/push/subscribe", sub(fcm), map[string]string{"Content-Type": "application/x-www-form-urlencoded", "Sec-Fetch-Site": "same-origin"}, 415},
		"unknown service":    {"/api/push/subscribe", sub("https://evil.example/push"), ok, 400},
		"internal service":   {"/api/push/subscribe", sub("https://127.0.0.1/push"), ok, 400},
		"unknown field":      {"/api/push/subscribe", `{"endpoint":"x","evil":1}`, ok, 400},
		"too large":          {"/api/push/subscribe", `{"endpoint":"` + strings.Repeat("a", 5000) + `"}`, ok, 400},
		"test unknown":       {"/api/push/test", `{"endpoint":"https://fcm.googleapis.com/other"}`, ok, 404},
		"post elsewhere":     {"/api/news", `{}`, ok, 405},
	} {
		if rec := post(c.path, c.body, c.hdr); rec.Code != c.code {
			t.Errorf("%s: %d, want %d (%s)", name, rec.Code, c.code, rec.Body)
		}
	}
	if rec := post("/api/push/unsubscribe", `{"endpoint":"`+fcm+`"}`, ok); rec.Code != 200 || a.push.subs[fcm] != nil {
		t.Errorf("unsubscribe: %d", rec.Code)
	}
	info := get(h, "GET", "/api/push", nil).Body.String()
	if !strings.Contains(info, `"enabled":true`) || !strings.Contains(info, `"key":"`) {
		t.Errorf("push info: %s", info)
	}
	for _, e := range []string{"https://fcm.googleapis.com/fcm/send/x", "https://updates.push.services.mozilla.com/wpush/v2/x", "https://web.push.apple.com/x", "https://db5p.notify.windows.com/w/?token=x"} {
		if !validPushEndpoint(e) {
			t.Errorf("%s should be allowed", e)
		}
	}
	for _, e := range []string{"http://fcm.googleapis.com/x", "https://fcm.googleapis.com.evil.example/x", "https://evilpush.apple.com.example/x", "https://fcm.googleapis.com:8443/x", "https://user@fcm.googleapis.com/x"} {
		if validPushEndpoint(e) {
			t.Errorf("%s should be refused", e)
		}
	}
}

func TestPushConfigValidation(t *testing.T) {
	if _, err := parseConfig([]byte(validConfig + "push: { enabled: true, subject: \"mailto:t@example.nl\" }\n")); err == nil || !strings.Contains(err.Error(), "-gen-vapid") {
		t.Errorf("push without key: %v", err)
	}
	key, _ := genVAPIDKey()
	if _, err := parseConfig([]byte(validConfig + "push: { enabled: true, subject: \"t@example.nl\", vapid_private_key: \"" + key + "\" }\n")); err == nil {
		t.Error("subject must be mailto: or https")
	}
	if _, err := parseConfig([]byte(validConfig + "waste: { enabled: true, provider: opzet, postcode: \"2522 aa\", number: 3 }\n")); err != nil {
		t.Errorf("waste opzet: %v", err)
	}
	if _, err := parseConfig([]byte(validConfig + "waste: { enabled: true, provider: home_assistant, home_assistant: { url: \"http://ha.local:8123\", token: \"x\", entities: [\"sensor.afval; rm\"] } }\n")); err == nil {
		t.Error("an invalid entity id must fail")
	}
}

// RFC 8291 Appendix A: the exact encrypted body for the published keys and salt.
func TestRFC8291Vector(t *testing.T) {
	d := func(s string) []byte {
		b, err := base64.RawURLEncoding.DecodeString(s)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	as, err := ecdh.P256().NewPrivateKey(d("yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	body, err := encryptPush(d("BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4"), d("BTBZMqHH6r4Tts7J_aSIgg"),
		[]byte("When I grow up, I want to be a watermelon"), as, d("DGv6ra1nlYgDCS1FRnbzlw"))
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPTpK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if got := base64.RawURLEncoding.EncodeToString(body); got != want {
		t.Errorf("body\n got %s\nwant %s", got, want)
	}
}

func TestWasteVisitorAddress(t *testing.T) {
	var lookups int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lookups++
		io.WriteString(w, `[]`)
	}))
	defer other.Close()
	mine := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/rest/adressen/2522AA-3":
			io.WriteString(w, `[{"bagId":"0518200000640437","huisletter":"","huisnummerToevoeging":""},{"bagId":"0518200000640438","huisletter":"A","huisnummerToevoeging":""}]`)
		case "/rest/adressen/0518200000640438/afvalstromen":
			d := time.Now().In(amsterdam).AddDate(0, 0, 1).Format("2006-01-02")
			io.WriteString(w, `[{"title":"Rest","menu_title":"Rest","ophaaldatum":"`+d+`"},{"title":"GFT","ophaaldatum":null}]`)
		default:
			io.WriteString(w, `[]`)
		}
	}))
	defer mine.Close()
	pdok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"response":{"docs":[{"gemeentenaam":"Testgemeente"}]}}`)
	}))
	defer pdok.Close()
	defer func(u string) { pdokURL = u }(pdokURL)
	pdokURL = pdok.URL
	a := newTestApp(t, validConfig)
	a.cfg.Waste.Providers = []string{other.URL, mine.URL} // extra opzet calendars; the built-in list is then off
	h := a.routes("/")
	if b := get(h, "GET", "/api/waste", nil).Body.String(); !strings.Contains(b, `"needs_address":true`) {
		t.Errorf("without a default address: %s", b)
	}
	b := get(h, "GET", "/api/waste?postcode=2522+aa&number=3&suffix=a", nil).Body.String()
	if !strings.Contains(b, `"type":"Restafval"`) || !strings.Contains(b, `"own":true`) || strings.Contains(b, "GFT") || strings.Contains(b, "2522") {
		t.Errorf("visitor address: %s", b)
	}
	before := lookups
	get(h, "GET", "/api/waste?postcode=2522AA&number=3&suffix=A", nil) // cached: no new lookups
	if lookups != before {
		t.Error("the address must be cached")
	}
	if b := get(h, "GET", "/api/waste?postcode=9999ZZ&number=1", nil).Body.String(); !strings.Contains(b, `"not_found":true`) {
		t.Errorf("unknown address: %s", b)
	}
	for _, q := range []string{"postcode=12&number=3", "postcode=2522AA&number=0", "postcode=2522AA&number=3&suffix=<x>", "postcode=2522AA"} {
		if rec := get(h, "GET", "/api/waste?"+q, nil); rec.Code != 400 {
			t.Errorf("%s: %d", q, rec.Code)
		}
	}
}

func TestWasteLabels(t *testing.T) {
	for in, want := range map[string]string{"GREEN": "GFT", "PAPER": "Papier", "PACKAGES": "PMD", "pbd": "PMD", "rst": "Restafval", "BESTAFR": "BEST-tas",
		"Rolcontainer GFT en etensresten": "GFT", "Plastic, Metaal en Drankkartons": "PMD", "Oud papier & karton": "Papier", "Grijze container / Sortibak": "Restafval",
		"MOBILETRANSFERPOINT": "Milieustraat op wielen", "BULKYRESTWASTE": "Grofvuil", "MAAS": "Maas", "Recyclewagen": "Recyclewagen", "Plastic+": "Plastic"} {
		if got := wasteLabel(in); got != want {
			t.Errorf("wasteLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAmsterdamPickups(t *testing.T) {
	now := time.Date(2026, 9, 28, 10, 0, 0, 0, amsterdam) // Monday, ISO week 40 (even)
	got := amsterdamPickups([]amsterdamItem{
		{Code: "Rest", Name: "Rest", Days: "maandag, donderdag", Where: "Aan de stoep"},
		{Code: "Papier", Name: "Papier", Days: "woensdag", Frequency: "oneven weken"},
		{Code: "GFT", Name: "GFT", Days: "maandag, dinsdag", Where: "Container"}, // container: no pickups
		{Code: "GA", Name: "Grof afval", Days: "vrijdag", Frequency: "9-10, 23-10-26"},
	}, now)
	byType := map[string][]string{}
	slices.SortFunc(got, func(a, b WastePickup) int { return strings.Compare(a.Date, b.Date) })
	for _, p := range got {
		byType[p.Type] = append(byType[p.Type], p.Date)
	}
	if r := byType["Restafval"]; len(r) < 3 || r[0] != "2026-09-28" || r[1] != "2026-10-01" {
		t.Errorf("weekly: %v", r)
	}
	if p := byType["Papier"]; len(p) == 0 || p[0] != "2026-10-07" { // week 41 is odd
		t.Errorf("odd weeks: %v", p)
	}
	if len(byType["GFT"]) != 0 {
		t.Error("containers have no collection days")
	}
	if g := byType["Grof afval"]; len(g) != 2 || g[0] != "2026-10-09" || g[1] != "2026-10-23" {
		t.Errorf("explicit dates: %v", g)
	}
}

func TestWasteProviderConfig(t *testing.T) {
	c, err := parseConfig([]byte(validConfig))
	if err != nil {
		t.Fatal(err)
	}
	all := enabledWasteProviders(c)
	if len(all) < 45 || slices.ContainsFunc(all, func(p wasteProvider) bool { return p.App }) {
		t.Errorf("default: %d providers, app providers must be off", len(all))
	}
	c.Waste.AppProviders = true
	if n := len(enabledWasteProviders(c)); n != len(wasteProviders) {
		t.Errorf("with app providers: %d of %d", n, len(wasteProviders))
	}
	c.Waste.Providers = []string{"denhaag", "https://afval.example.nl"}
	if l := enabledWasteProviders(c); len(l) != 2 || l[0].ID != "opzet:afval.example.nl" || l[1].ID != "denhaag" {
		t.Errorf("explicit list: %+v", l)
	}
	for yaml, ok := range map[string]bool{
		"waste: { providers: [nosuchprovider] }":                                           false,
		"waste: { provider: omrin, postcode: \"9022CB\", number: 1 }":                      false, // app provider without app_providers
		"waste: { app_providers: true, provider: omrin, postcode: \"9022CB\", number: 1 }": true,
		"waste: { provider: opzet }":                                                       true, // 1.10.0 name
		"waste: { providers: [\"http://insecure.example\"] }":                              false,
	} {
		if _, err := parseConfig([]byte(validConfig + yaml + "\n")); (err == nil) != ok {
			t.Errorf("%s: err=%v, want ok=%v", yaml, err, ok)
		}
	}
}

// Discovery asks the municipality's own calendar and the regional ones, never
// another municipality's calendar.
func TestWasteDiscovery(t *testing.T) {
	var mu sync.Mutex
	asked := map[string]int{}
	cal := func(name string, knows bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			asked[name]++
			mu.Unlock()
			switch {
			case strings.HasSuffix(r.URL.Path, "/afvalstromen"):
				io.WriteString(w, `[{"title":"GREEN","ophaaldatum":"`+time.Now().In(amsterdam).AddDate(0, 0, 2).Format("2006-01-02")+`"}]`)
			case knows:
				io.WriteString(w, `[{"bagId":"123"}]`)
			default:
				io.WriteString(w, `[]`)
			}
		}))
	}
	own, other, regional := cal("own", false), cal("other", true), cal("regional", true)
	defer own.Close()
	defer other.Close()
	defer regional.Close()
	pdok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"response":{"docs":[{"gemeentenaam":"Eigenstad"}]}}`)
	}))
	defer pdok.Close()
	defer func(u string) { pdokURL = u }(pdokURL)
	pdokURL = pdok.URL
	a := newTestApp(t, validConfig)
	list := []wasteProvider{
		{ID: "other", Name: "Anderstad", Kind: "opzet", URL: other.URL, Gemeenten: []string{"Anderstad"}},
		{ID: "own", Name: "Eigenstad", Kind: "opzet", URL: own.URL, Gemeenten: []string{"Eigenstad"}},
		{ID: "regional", Name: "Regio", Kind: "opzet", URL: regional.URL},
	}
	p, pk, err := a.discoverWaste(context.Background(), wasteAddr{Postcode: "1234AB", Number: 1}, list)
	if err != nil || p.ID != "regional" || len(pk) != 1 || pk[0].Type != "GFT" {
		t.Fatalf("discovery: %v %+v %v", p.ID, pk, err)
	}
	if asked["other"] != 0 || asked["own"] != 1 {
		t.Errorf("asked: %v (another municipality's calendar must not be asked)", asked)
	}
}

// Reference values: NASA/JPL Horizons, astrometric J2000, 2026-09-28 20:00 UTC,
// and the moon's rise/set for Den Haag (19:40 and 11:30 local time).
func TestSkyAgainstHorizons(t *testing.T) {
	at := time.Date(2026, 9, 28, 20, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		k       string
		ra, dec float64
	}{{"jupiter", 141.44638, 15.74114}, {"saturn", 11.51069, 2.00016}, {"mars", 122.45225, 21.09359}, {"venus", 213.01399, -20.56331}} {
		ra, dec := planetRADec(c.k, at)
		if math.Abs(ra-c.ra) > 0.2 || math.Abs(dec-c.dec) > 0.2 {
			t.Errorf("%s: %.2f %.2f, JPL %.2f %.2f", c.k, ra, dec, c.ra, c.dec)
		}
	}
	s := computeSky(52.08, 4.30, time.Date(2026, 9, 28, 18, 0, 0, 0, amsterdam))
	near := func(p *time.Time, hm string) bool {
		if p == nil {
			return false
		}
		want, _ := time.ParseInLocation("2006-01-02 15:04", hm, amsterdam)
		return p.Sub(want).Abs() <= 6*time.Minute
	}
	if !near(s.Moon.Rise, "2026-09-28 19:40") || !near(s.Moon.Set, "2026-09-29 11:30") {
		t.Errorf("moon rise/set: %v %v", s.Moon.Rise, s.Moon.Set)
	}
	if !near(s.Sunset, "2026-09-28 19:27") || s.Dark == nil || s.Dawn == nil {
		t.Errorf("sun: %v %v %v", s.Sunset, s.Dark, s.Dawn)
	}
	names := []string{}
	for _, p := range s.Planets {
		names = append(names, p.Name)
	}
	if strings.Join(names, ",") != "Saturnus,Mars,Jupiter" {
		t.Errorf("planets tonight: %v", names)
	}
	if lv := auroraLevel(7.3); lv != 3 || auroraLevel(4.7) != 0 {
		t.Error("aurora levels")
	}
	kp, err := parseKpForecast([]byte(`[{"time_tag":"2026-09-28T18:00:00","kp":2.33,"observed":"predicted"},{"time_tag":"2026-09-28T21:00:00","kp":6.0,"observed":"predicted"},{"time_tag":"2026-09-29T09:00:00","kp":8,"observed":"predicted"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if m := maxKp(kp, time.Date(2026, 9, 28, 18, 40, 0, 0, time.UTC), time.Date(2026, 9, 29, 4, 27, 0, 0, time.UTC)); m == nil || *m != 6 {
		t.Errorf("max Kp tonight: %v", m)
	}
}

func TestThunderAndInsects(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	th := computeThunder([]int64{1, 2, 3}, []*float64{f(0), f(1.5), nil}, []*float64{f(100), f(800), f(2500)}, []int{3, 80, 3})
	if th.Level != 2 || th.Peak != 2 {
		t.Errorf("thunder: %+v", th)
	}
	if thunderRisk(0, 0, 96) != 3 || thunderRisk(0, 50, 3) != 0 {
		t.Error("thunder codes")
	}
	body := `{"hourly":{"time":["2026-06-10T12:00","2026-06-10T20:00","2026-06-11T12:00","2026-06-11T20:00","2026-01-10T12:00","2026-01-10T20:00"],
	 "temperature_2m":[20,19,4,10,6,3],"relative_humidity_2m":[85,80,60,90,90,90],"wind_speed_10m":[5,8,30,30,10,10]}}`
	d, err := parseInsects([]byte(body), time.Date(2026, 1, 1, 9, 0, 0, 0, amsterdam))
	if err != nil {
		t.Fatal(err)
	}
	// warm and humid June day: high; cold day: none; mild January day: ticks lowered in winter
	if len(d.Days) != 3 || d.Days[0].Date != "2026-06-10" || d.Days[0].Ticks != 3 || d.Days[0].Mosquito != 3 ||
		d.Days[1].Ticks != 0 || d.Days[1].Mosquito != 0 || d.Days[2].Date != "2026-01-10" || d.Days[2].Ticks != 0 {
		t.Errorf("insects: %+v", d.Days)
	}
}

func TestParseF1(t *testing.T) {
	sched := `{"MRData":{"RaceTable":{"season":"2026","Races":[
	 {"round":"15","raceName":"Azerbaijan Grand Prix","date":"2026-09-26","time":"11:00:00Z","Circuit":{"circuitName":"Baku City Circuit","Location":{"locality":"Baku","country":"Azerbaijan"}}},
	 {"round":"16","raceName":"Singapore Grand Prix","date":"2026-10-11","time":"12:00:00Z","Circuit":{"circuitName":"Marina Bay","Location":{"locality":"Marina Bay","country":"Singapore"}},
	  "Qualifying":{"date":"2026-10-10","time":"13:00:00Z"},"Sprint":{"date":"2026-10-10","time":"09:00:00Z"}}]}}}`
	next, season, err := parseF1Schedule([]byte(sched), time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC))
	if err != nil || season != "2026" || next == nil || next.Round != 16 || next.Place != "Marina Bay, Singapore" || len(next.Sessions) != 2 || next.Sessions[0].Name != "Sprint" {
		t.Fatalf("schedule: %+v %v", next, err)
	}
	last, err := parseF1Last([]byte(`{"MRData":{"RaceTable":{"Races":[{"round":"15","raceName":"Azerbaijan Grand Prix","date":"2026-09-26","time":"11:00:00Z",
	 "Results":[{"position":"1","Driver":{"givenName":"Kimi","familyName":"Antonelli"},"Constructor":{"name":"Mercedes"}},{"position":"2","Driver":{"givenName":"George","familyName":"Russell"},"Constructor":{"name":"Mercedes"}},
	 {"position":"4","Driver":{"givenName":"X","familyName":"Y"},"Constructor":{"name":"Z"}}]}]}}}`))
	if err != nil || len(last.Podium) != 2 || last.Podium[0].Driver != "Kimi Antonelli" {
		t.Errorf("last: %+v %v", last, err)
	}
	st, err := parseF1Standings([]byte(`{"MRData":{"StandingsTable":{"StandingsLists":[{"DriverStandings":[{"position":"1","points":"302","Driver":{"givenName":"Kimi","familyName":"Antonelli"},"Constructors":[{"name":"Mercedes"}]}]}]}}}`))
	if err != nil || len(st) != 1 || st[0].Points != 302 || st[0].Team != "Mercedes" {
		t.Errorf("standings: %+v %v", st, err)
	}
}

func TestSportEvents(t *testing.T) {
	a := newTestApp(t, validConfig+`sports:
  events:
    - { sport: athletics, name: "EK atletiek", start: "2026-08-10", end: "2026-08-16", keywords: ["EK atletiek"] }
    - { sport: athletics, name: "EK indoor", start: "2027-03-04", end: "2027-03-07" }
    - { sport: athletics, name: "WK atletiek", start: "2027-09-10", end: "2027-09-19" }
    - { sport: mtb, name: "WK mountainbike", start: "2026-09-20", end: "2026-09-27", keywords: ["WK mountainbike"] }
`)
	evs := a.sportEvents(a.cfg, time.Date(2026, 9, 28, 12, 0, 0, 0, amsterdam))
	var got []string
	for _, e := range evs {
		got = append(got, e.Sport+":"+e.Name+":"+e.Status)
	}
	if strings.Join(got, "|") != "mtb:WK mountainbike:done|athletics:EK indoor:upcoming" { // config order: f1, mtb, athletics
		t.Errorf("events: %v", got)
	}
	for _, bad := range []string{`sports: { events: [ { sport: f1, name: "x", start: "2026-01-01", end: "2026-01-02" } ] }`,
		`sports: { events: [ { sport: mtb, name: "x", start: "2026-01-05", end: "2026-01-02" } ] }`, `sports: { sports: [darts] }`} {
		if _, err := parseConfig([]byte(validConfig + bad + "\n")); err == nil {
			t.Errorf("must fail: %s", bad)
		}
	}
}

// A config.yaml from 1.14.0/1.14.1 with the removed Kwetsbaarheden panel still loads, with a warning.
func TestRemovedVulnsConfig(t *testing.T) {
	c, err := parseConfig([]byte(validConfig + "vulns:\n  enabled: true\n  products: [Fortinet]\n  days: 30\nkeys: { nvd_api_key: \"x\" }\nrefresh: { vulns: 60m, news: 5m }\n"))
	if err != nil {
		t.Fatalf("an old config must still load: %v", err)
	}
	warn, _ := configWarnings(c)
	if !slices.ContainsFunc(warn, func(w string) bool { return strings.Contains(w, "Kwetsbaarheden panel was removed") }) {
		t.Errorf("warnings: %v", warn)
	}
	if _, ok := refreshSeconds(c.Refresh)["vulns"]; ok {
		t.Error("refresh.vulns must not reach the browser")
	}
	a := newTestApp(t, validConfig)
	if rec := get(a.routes("/"), "GET", "/api/vulns", nil); rec.Code != 404 {
		t.Errorf("/api/vulns: %d", rec.Code)
	}
}

// testdata/burgernet-test-alerts.json: the four messages of Burgernet's test feed
// (AMBER Alert start and close, Vermist Kind Alert start and close).
func TestParseAmber(t *testing.T) {
	body, err := os.ReadFile("testdata/burgernet-test-alerts.json")
	if err != nil {
		t.Fatal(err)
	}
	list, err := parseAmber(body)
	if err != nil || len(list) != 2 {
		t.Fatalf("closing messages must be left out: %d alerts, %v", len(list), err)
	}
	amber, vka := list[0], list[1]
	if !amber.National || amber.Title != "Voornaam (leeftijd)" || amber.Kind != "Vermist" || amber.URL != "https://www.politie.nl/amberalert" ||
		amber.Image != "https://services.burgernet.nl/fototest/9999.jpg" || !strings.HasPrefix(amber.Text, "Laatst gezien") || amber.Sent.IsZero() {
		t.Errorf("amber: %+v", amber)
	}
	if vka.National || vka.Area != "Amsterdam" || vka.radiusKm != 5 {
		t.Errorf("vermist kind alert: %+v", vka)
	}
	// Amsterdam Centraal is within the 5 km circle, Den Haag is not; an AMBER Alert covers everyone
	if !vka.covers(52.3791, 4.9003) || vka.covers(52.0705, 4.3007) || !amber.covers(51.44, 5.47) {
		t.Error("covers")
	}
	if l, err := parseAmber([]byte(`[]`)); err != nil || len(l) != 0 {
		t.Errorf("empty feed: %v %v", l, err)
	}
	if _, err := parseAmber([]byte(`<html>`)); err == nil {
		t.Error("an HTML page must fail")
	}
	// the handler: the photo only via this server's image proxy, "near" per visitor
	a := newTestApp(t, validConfig+"features: { show_images: true, proxy_images: true }\n")
	a.threats.ok("burgernet:amber", list, "", "")
	h := a.routes("/")
	b := get(h, "GET", "/api/amber?lat=52.0705&lon=4.3007", nil).Body.String()
	if strings.Contains(b, "services.burgernet.nl") || !strings.Contains(b, `"image":"api/img?u=`) || strings.Count(b, `"near":true`) != 1 {
		t.Errorf("handler (Den Haag): %s", b)
	}
	if b := get(h, "GET", "/api/amber?lat=52.3791&lon=4.9003", nil).Body.String(); strings.Count(b, `"near":true`) != 2 {
		t.Errorf("handler (Amsterdam): %s", b)
	}
	a2 := newTestApp(t, validConfig+"features: { proxy_images: false }\n")
	a2.threats.ok("burgernet:amber", list, "", "")
	if b := get(a2.routes("/"), "GET", "/api/amber", nil).Body.String(); strings.Contains(b, `"image"`) {
		t.Errorf("without the image proxy no photo link: %s", b)
	}
}

func TestSatellite(t *testing.T) {
	caps := []byte(`<Layer><Name>rgb_geocolour</Name><Dimension name="time" default="2026-09-28T19:20:00Z" units="ISO8601" nearestValue="1">2024-09-23T00:00:00.000Z/2026-09-28T19:20:00.000Z/PT10M</Dimension></Layer>`)
	if tm, err := parseSatTime(caps); err != nil || !tm.Equal(time.Date(2026, 9, 28, 19, 20, 0, 0, time.UTC)) {
		t.Errorf("time: %v %v", tm, err)
	}
	if _, err := parseSatTime([]byte(`<ServiceException>no layer</ServiceException>`)); err == nil {
		t.Error("capabilities without a time must fail")
	}
	u := satMapURL("https://view.eumetsat.int/geoserver", "a:b,c:d", "image/png", "")
	if !strings.Contains(u, "styles=%2C&") || !strings.Contains(u, "transparent=true") || strings.Contains(u, "time=") {
		t.Errorf("overlay url %s", u)
	}
	jpeg, png := []byte("\xff\xd8\xff\xe0jpeg"), []byte("\x89PNG\r\n\x1a\nrest")
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		calls = append(calls, q.Get("request")+" "+q.Get("format"))
		switch {
		case q.Get("request") == "GetCapabilities" && r.URL.Path == "/mtg_fd/rgb_geocolour/ows":
			w.Write(caps)
		case q.Get("format") == "image/jpeg" && q.Get("time") == "2026-09-28T19:20:00Z":
			w.Write(jpeg)
		case q.Get("format") == "image/png":
			w.Write(png)
		default:
			w.Write([]byte(`<ServiceExceptionReport/>`))
		}
	}))
	defer srv.Close()
	a := newTestApp(t, validConfig)
	a.cfg.Satellite.URL = srv.URL
	if err := a.satelliteJob(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := a.satelliteJob(context.Background()); err != nil || len(calls) != 4 { // 2nd run: same time, only capabilities
		t.Fatalf("second run: %v, calls %v", err, calls)
	}
	h := a.routes("/")
	b := get(h, "GET", "/api/satellite", nil).Body.String()
	if !strings.Contains(b, `"image":"api/satellite/image?t=1790623200"`) || !strings.Contains(b, `"overlay":"api/satellite/overlay?t=`) || strings.Contains(b, srv.URL) {
		t.Errorf("handler: %s", b)
	}
	rec := get(h, "GET", "/api/satellite/image?t=1", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(rec.Body.Bytes(), jpeg) {
		t.Errorf("image: %d %v", rec.Code, rec.Header())
	}
	if rec := get(h, "GET", "/api/satellite/overlay", nil); rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("overlay: %d", rec.Code)
	}
	// an XML error instead of a JPEG is an error, the previous image stays
	a.cfg.Satellite.Layer = "mtg_fd:other"
	if err := a.satelliteJob(context.Background()); err == nil {
		t.Error("missing capabilities must fail")
	}
	if rec := get(h, "GET", "/api/satellite/image", nil); rec.Code != 200 {
		t.Error("the last image must stay available")
	}
	if b := get(h, "GET", "/api/satellite", nil).Body.String(); !strings.Contains(b, `"error"`) {
		t.Errorf("error not reported: %s", b)
	}
	a2 := newTestApp(t, validConfig+"satellite: { enabled: false }\n")
	if rec := get(a2.routes("/"), "GET", "/api/satellite/image", nil); rec.Code != 404 {
		t.Errorf("disabled: %d", rec.Code)
	}
	if _, err := parseConfig([]byte(validConfig + "satellite: { layer: \"x&request=evil\" }\n")); err == nil {
		t.Error("a layer with other characters must be rejected")
	}
}

func TestWiki(t *testing.T) {
	for _, c := range []struct {
		term, title string
		want        bool
	}{{"Trump", "Donald Trump", true}, {"Poetin", "Vladimir Poetin", true}, {"Grand Prix", "Grand Prix Formule 1 van Spanje", false},
		{"Strafhof Historische", "Internationaal Strafhof", false}, {"Max Verstappen", "Max Verstappen", true}} {
		if got := wikiTitleFits(c.term, c.title); got != c.want {
			t.Errorf("fits(%q, %q) = %v", c.term, c.title, got)
		}
	}
	const base = "https://nl.wikipedia.org"
	std := `{"type":"standard","title":"Donald Trump","description":"president","extract":"Donald John Trump is een <b>Amerikaans</b> politicus.",
		"thumbnail":{"source":"https://upload.wikimedia.org/x.jpg"},"content_urls":{"desktop":{"page":"https://nl.wikipedia.org/wiki/Donald_Trump"}}}`
	s, err := parseWikiSummary([]byte(std), base)
	if err != nil || !s.Found || s.URL != "https://nl.wikipedia.org/wiki/Donald_Trump" || s.Thumb == "" || strings.Contains(s.Extract, "<b>") {
		t.Errorf("standard: %+v %v", s, err)
	}
	if _, err := parseWikiSummary([]byte(`{"type":"disambiguation","title":"Trump","extract":"Trump kan verwijzen naar:"}`), base); err != errWikiNotFound {
		t.Error("a disambiguation page is not a summary")
	}
	evil := strings.NewReplacer("https://nl.wikipedia.org/wiki", "https://evil.example/wiki", "upload.wikimedia.org", "evil.example").Replace(std)
	if s, _ := parseWikiSummary([]byte(evil), base); s.URL != "" || s.Thumb != "" {
		t.Errorf("foreign links must be dropped: %+v", s)
	}

	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch r.URL.Path {
		case "/api/rest_v1/page/summary/Trump":
			w.Write([]byte(`{"type":"disambiguation","title":"Trump","extract":"Trump kan verwijzen naar:"}`))
		case "/w/rest.php/v1/search/page":
			w.Write([]byte(`{"pages":[{"key":"Trump","title":"Trump"},{"key":"Melania_Trump_en_Barron","title":"Melania Trump en Barron"},{"key":"Donald_Trump","title":"Donald Trump"}]}`))
		case "/api/rest_v1/page/summary/Donald_Trump":
			w.Write([]byte(strings.ReplaceAll(std, base, "http://"+r.Host)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	a := newTestApp(t, validConfig+"features: { show_images: true, proxy_images: true }\n")
	a.cfg.Trending.Wikipedia.URL = srv.URL
	a.trend.terms, a.trend.at = []TrendTerm{{Term: "Trump", Sources: 8}, {Term: "Onbekend Woord", Sources: 4}}, time.Now()
	h := a.routes("/")
	b := get(h, "GET", "/api/wiki?term=trump", nil).Body.String()
	if !strings.Contains(b, `"title":"Donald Trump"`) || !strings.Contains(b, `"thumb":"api/img?u=`) || !strings.Contains(b, `"url":"`+srv.URL+`/wiki/Donald_Trump"`) {
		t.Errorf("lookup: %s", b)
	}
	n := len(hits)
	get(h, "GET", "/api/wiki?term=Trump", nil)
	if len(hits) != n {
		t.Error("the summary must be cached")
	}
	if b := get(h, "GET", "/api/wiki?term=Onbekend%20Woord", nil).Body.String(); !strings.Contains(b, `"found":false`) {
		t.Errorf("not found: %s", b)
	}
	if rec := get(h, "GET", "/api/wiki?term=Nederland", nil); rec.Code != 404 {
		t.Errorf("only trending terms may be looked up: %d", rec.Code)
	}
	a2 := newTestApp(t, validConfig+"trending: { wikipedia: { enabled: false } }\n")
	if b := get(a2.routes("/"), "GET", "/api/wiki?term=Trump", nil).Body.String(); !strings.Contains(b, `"enabled":false`) {
		t.Errorf("disabled: %s", b)
	}
}

func TestParseGCP(t *testing.T) {
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	body := `[
	 {"begin":"2026-09-28T09:00:00+00:00","end":null,"modified":"2026-09-28T11:00:00+00:00","external_desc":"Cloud Run <b>errors</b>","severity":"medium",
	  "status_impact":"SERVICE_DISRUPTION","uri":"incidents/abc","currently_affected_locations":[{"title":"Netherlands (europe-west4)","id":"europe-west4"}]},
	 {"begin":"2026-09-27T20:00:00+00:00","end":"2026-09-28T02:00:00+00:00","modified":"2026-09-28T03:00:00+00:00","external_desc":"BigQuery outage","severity":"high",
	  "status_impact":"SERVICE_OUTAGE","uri":"incidents/def","currently_affected_locations":[]},
	 {"begin":"2026-09-01T14:44:00+00:00","end":"2026-09-01T18:52:00+00:00","modified":"2026-09-10T21:20:16+00:00","external_desc":"Old incident","severity":"high",
	  "status_impact":"SERVICE_OUTAGE","uri":"incidents/old"},
	 {"begin":"2026-09-28T10:00:00+00:00","end":null,"modified":"2026-09-28T10:00:00+00:00","external_desc":"Planned change notice","severity":"low",
	  "status_impact":"SERVICE_INFORMATION","uri":"incidents/info"}]`
	v, err := parseGCP([]byte(body), now)
	if err != nil {
		t.Fatal(err)
	}
	d := v.(OutageData)
	if d.Status != "minor" || len(d.Incidents) != 3 {
		t.Fatalf("status %q, %d incidents: %+v", d.Status, len(d.Incidents), d.Incidents)
	}
	in := d.Incidents[0]
	if in.Title != "Cloud Run errors (europe-west4)" || in.URL != "https://status.cloud.google.com/incidents/abc" || in.Status != "verstoring" || in.Resolved {
		t.Errorf("ongoing: %+v", in)
	}
	if !d.Incidents[1].Resolved || d.Incidents[1].Status != "opgelost" || d.Incidents[2].Status != "informatie" {
		t.Errorf("resolved/info: %+v", d.Incidents[1:])
	}
	v, _ = parseGCP([]byte(`[{"end":"","modified":"2026-09-28T11:00:00+00:00","external_desc":"Global outage","severity":"high","status_impact":"SERVICE_OUTAGE","uri":"incidents/x"}]`), now)
	if v.(OutageData).Status != "major" {
		t.Error("an ongoing outage is major")
	}
	if v, _ := parseGCP([]byte(`[]`), now); v.(OutageData).Status != "ok" {
		t.Error("no incidents is ok")
	}
	if _, err := parseGCP([]byte(`<html>`), now); err == nil {
		t.Error("HTML must fail")
	}
}
