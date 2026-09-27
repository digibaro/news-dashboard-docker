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
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
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
	a := newTestApp(t, validConfig)
	a.cfg.Waste.Providers = []string{other.URL, mine.URL}
	h := a.routes("/")
	if b := get(h, "GET", "/api/waste", nil).Body.String(); !strings.Contains(b, `"needs_address":true`) {
		t.Errorf("without a default address: %s", b)
	}
	b := get(h, "GET", "/api/waste?postcode=2522+aa&number=3&suffix=a", nil).Body.String()
	if !strings.Contains(b, `"type":"Rest"`) || !strings.Contains(b, `"own":true`) || strings.Contains(b, "GFT") || strings.Contains(b, "2522") {
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
