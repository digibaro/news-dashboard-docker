package main

import (
	"strings"
	"testing"
	"time"
)

func TestStreetMatches(t *testing.T) {
	for _, c := range []struct {
		title, street string
		want          bool
	}{
		{"Ambulance met spoed naar Koningskade in 's-Gravenhage", "Koningskade", true},
		{"Ambulance met spoed naar Koningskade in 's-Gravenhage", "koningskade", true},
		{"Brandgerucht bij Architect Duikerstraat in 'S-Gravenhage", "architect duikerstraat", true},
		{"Brandgerucht bij Architect Duikerstraat in 'S-Gravenhage", "Duikerstraat", true},
		{"Verkeersongeval op Escamplaan in 's-Gravenhage", "Escamp", false},      // whole words only
		{"Verkeersongeval op Escamplaan in 's-Gravenhage", "Koningskade", false}, // other street
		{"Ambulance naar Rue de Genève in Utrecht", "rue de geneve", true},       // accents
		{"Ambulance naar 1e Oosterparkstraat in Amsterdam", "1e Oosterparkstraat", true},
		{"Ambulance naar 2e Oosterparkstraat in Amsterdam", "1e Oosterparkstraat", false},
		{"P2000 Melding Lunterenstraat, 'S-Gravenhage", "Lunterenstraat", true},
		{"anything", "  ", false},
	} {
		if got := streetMatches(c.title, c.street); got != c.want {
			t.Errorf("streetMatches(%q, %q) = %v, want %v", c.title, c.street, got, c.want)
		}
	}
}

func TestCleanRules(t *testing.T) {
	in := []AlarmRule{
		{City: "den-haag", Street: "  Koningskade  "},
		{City: "Den-Haag", Street: "koningskade"}, // duplicate after folding
		{City: "den haag", Street: "Spui"},        // not a slug
		{City: "lifeliner", Street: "Spui"},       // not a city
		{City: "utrecht", Street: "ab"},           // too short
		{City: "utrecht", Street: "Oudegracht", Spoed: true},
	}
	got := cleanRules(in)
	if len(got) != 2 || got[0].Street != "Koningskade" || got[0].City != "den-haag" || got[1].City != "utrecht" || !got[1].Spoed {
		t.Fatalf("cleanRules = %+v", got)
	}
	var many []AlarmRule
	for _, s := range []string{"Aaa", "Bbb", "Ccc", "Ddd", "Eee", "Fff", "Ggg", "Hhh", "Iii", "Jjj", "Kkk", "Lll"} {
		many = append(many, AlarmRule{City: "utrecht", Street: s})
	}
	if n := len(cleanRules(many)); n != maxAlarmRules {
		t.Fatalf("cleanRules kept %d rules, want %d", n, maxAlarmRules)
	}
}

func TestMatchAlarms(t *testing.T) {
	now := time.Now()
	items := []Alarm{
		{Title: "Ambulance met spoed naar Koningskade in 's-Gravenhage", Urgency: "spoed", Time: now, service: "ambulance", id: "a1"},
		{Title: "Ambulance naar Koningskade in 's-Gravenhage", Urgency: "gepland", Time: now, service: "ambulance", id: "a2"},
		{Title: "Brandgerucht bij Koningskade in 's-Gravenhage", Urgency: "geen spoed", Time: now.Add(-8 * time.Hour), service: "brandweer", id: "a3"},
		{Title: "Verkeersongeval op Escamplaan in 's-Gravenhage", Urgency: "spoed", Time: now, service: "politie", id: "a4"},
	}
	since := now.Add(-6 * time.Hour)
	all := matchAlarms(items, "den-haag", []AlarmRule{{City: "den-haag", Street: "Koningskade"}}, since)
	if len(all) != 2 || all[0].ID != "a1" || all[1].ID != "a2" || all[0].Street != "Koningskade" || all[0].Service != "ambulance" {
		t.Fatalf("all = %+v", all)
	}
	if spoed := matchAlarms(items, "den-haag", []AlarmRule{{City: "den-haag", Street: "Koningskade", Spoed: true}}, since); len(spoed) != 1 || spoed[0].ID != "a1" {
		t.Fatalf("spoed = %+v", spoed)
	}
	if other := matchAlarms(items, "den-haag", []AlarmRule{{City: "utrecht", Street: "Koningskade"}}, since); len(other) != 0 {
		t.Fatalf("a rule for another city matched: %+v", other)
	}
}

func TestStickyPayload(t *testing.T) {
	m := pushMsg{Title: [2]string{"Alarmering: Spui", ""}, Body: [2]string{"x", ""}, Tag: "aw-1", Sticky: true}
	if s := string(m.payload("nl")); !strings.Contains(s, `"sticky":"1"`) {
		t.Fatalf("payload %s", s)
	}
	m.Sticky = false
	if s := string(m.payload("en")); strings.Contains(s, "sticky") {
		t.Fatalf("payload %s", s)
	}
}
