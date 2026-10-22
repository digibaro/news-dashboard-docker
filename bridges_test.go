package main

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func bridgeXML(id string, lat, lon float64, start, end string) string {
	e := ""
	if end != "" {
		e = `<com:overallEndTime>` + end + `</com:overallEndTime>`
	}
	return `<sit:situation id="BMS01_` + id + `"><sit:situationRecord xsi:type="sit:GeneralNetworkManagement" id="x">` +
		`<sit:validity><com:validityStatus>definedByValidityTimeSpec</com:validityStatus><com:validityTimeSpecification>` +
		`<com:overallStartTime>` + start + `</com:overallStartTime>` + e + `</com:validityTimeSpecification></sit:validity>` +
		`<sit:locationReference xsi:type="loc:PointLocation"><loc:externalReferencing><loc:externalLocationCode>` + id + `</loc:externalLocationCode>` +
		`<loc:externalReferencingSystem>RIS-index</loc:externalReferencingSystem></loc:externalReferencing>` +
		`<loc:pointByCoordinates><loc:pointCoordinates><loc:latitude>` + ftoa(lat) + `</loc:latitude><loc:longitude>` + ftoa(lon) + `</loc:longitude></loc:pointCoordinates></loc:pointByCoordinates></sit:locationReference>` +
		`<sit:generalNetworkManagementType>bridgeSwingInOperation</sit:generalNetworkManagementType></sit:situationRecord></sit:situation>`
}

func ftoa(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func fisXML(id int, name, city string, lon, lat float64) string {
	return `<gml:featureMember><app:Bridge gml:id="Bridge_` + strconv.Itoa(id) + `"><app:Geometry><gml:Point><gml:pos>` + ftoa(lon) + ` ` + ftoa(lat) +
		`</gml:pos></gml:Point></app:Geometry><app:City>` + city + `</app:City><app:Name>` + name + `</app:Name></app:Bridge></gml:featureMember>`
}

func TestParseFISBridges(t *testing.T) {
	body := `<wfs:FeatureCollection>` + fisXML(6385, "Constabelbrug", "Edam", 5.048817, 52.511412) +
		fisXML(7, "Prins &amp; Bernhardbrug", "Zaandam", 4.8209, 52.4302) +
		fisXML(8, "Rheinbrücke", "Wesel", 6.6, 51.65) + // inside the box (a border area): kept
		fisXML(9, "Pont du Rhin", "Strasbourg", 7.8, 48.57) + // outside
		fisXML(10, "", "Ergens", 5, 52) + // no name
		`</wfs:FeatureCollection>`
	l := parseFISBridges([]byte(body))
	if len(l) != 3 || l[0].ID != "B6385" || l[0].Name != "Constabelbrug" || l[0].City != "Edam" || l[0].Lat != float32(52.511412) || l[1].Name != "Prins & Bernhardbrug" {
		t.Fatalf("parseFISBridges = %+v", l)
	}
}

// a store with three FIS bridges: Beatrixbrug (Zaandam), Leeghwaterbrug (Alkmaar) and the Botlekbrug,
// which NDW never reports
func testBridgeStore() *bridgeStore {
	s := newBridgeStore()
	list := parseFISBridges([]byte(`<x>` + fisXML(1, "Beatrixbrug Zaandam", "Zaandam", 4.8203, 52.4301) + fisXML(2, "Leeghwaterbrug Alkmaar", "Alkmaar", 4.75, 52.63) +
		fisXML(3, "Botlekbrug", "Rotterdam", 4.331145, 51.871283) + `</x>`))
	s.all, s.fisN = list, len(list)
	for i, b := range list {
		s.byID[b.ID] = int32(i)
		c := gridCell(b.Lat, b.Lon)
		s.grid[c] = append(s.grid[c], int32(i))
	}
	return s
}

func TestBridgeRecordsAndSnapshot(t *testing.T) {
	now := time.Date(2026, 10, 22, 10, 0, 0, 0, time.UTC)
	ts := func(m int) string { return now.Add(time.Duration(m) * time.Minute).Format(time.RFC3339) }
	at := func(m int) int64 { return now.Add(time.Duration(m) * time.Minute).Unix() }
	doc := `<mc:messageContainer xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance"><mc:payload>` +
		bridgeXML("NLZAA002360562400027", 52.43, 4.82, ts(-3), "") + // open now, ~25 m from the FIS point
		bridgeXML("NLTNZ001300522400303", 51.33, 3.83, ts(-300), "") + // "open" for 5 hours: missed close; no FIS bridge
		bridgeXML("NLALK002340557700383", 52.63, 4.75, ts(-20), ts(-14)) + // closed again
		`</mc:payload></mc:messageContainer>`
	recs, err := parseNDW(strings.NewReader(doc))
	if err != nil {
		t.Fatal(err)
	}
	br := bridgeRecords(recs)
	if len(br) != 3 || br[0].ndw != "NLZAA002360562400027" || br[0].lat != 52.43 || br[0].lon != 4.82 || br[0].end != 0 || br[2].end != at(-14) {
		t.Fatalf("bridgeRecords = %+v", br)
	}
	s := testBridgeStore()
	s.markSeen(br, now)
	s.open, s.openAt = br, now
	var plan []bridgeRec
	for i := 0; i < 5; i++ { // the favourite: 5 openings, only 3 are sent
		plan = append(plan, bridgeRec{ndw: "NLALK002340557700383", lat: 52.63, lon: 4.75, start: at(10 + i*30), end: at(16 + i*30)})
	}
	z := bridgeRec{ndw: "NLZAA002360562400027", lat: 52.43, lon: 4.82}
	with := func(r bridgeRec, a, b int) bridgeRec { r.start, r.end = at(a), at(b); return r }
	plan = append(plan, with(z, 5, 11), with(z, 5, 11) /* duplicate */, with(z, -30, -24) /* past */, with(z, 1800, 1806) /* beyond 24 h */)
	s.planned, s.plannedAt = plan, now
	d := s.snapshot(now, []string{"B2", "B3"})
	if len(d.Open) != 1 || d.Open[0].ID != "B1" {
		t.Errorf("open = %+v", d.Open)
	}
	if len(d.FavPlanned) != 3 || d.FavPlanned[0].ID != "B2" || !d.FavPlanned[0].Start.Equal(now.Add(10*time.Minute)) {
		t.Errorf("fav planned = %+v", d.FavPlanned)
	}
	if len(d.Planned) != 1 || d.Planned[0].ID != "B1" {
		t.Errorf("planned = %+v", d.Planned)
	}
	got := map[string]BridgeInfo{}
	for _, b := range d.Bridges {
		got[b.ID] = b
	}
	if len(d.Bridges) != 3 || !got["B1"].NDW || !got["B2"].NDW || got["B3"].NDW || got["B3"].Name != "Botlekbrug" {
		t.Errorf("bridges (B1 open, B2 and B3 favourites; B3 never reported by NDW) = %+v", d.Bridges)
	}
	if late := s.snapshot(now.Add(20*time.Minute), nil); len(late.Open) != 0 || late.OpenAt != nil {
		t.Errorf("an old download still reports open bridges: %+v", late.Open)
	}
	// a favourite stored as ISRS code (before the FIS list) is renamed
	if r := s.snapshot(now, []string{"NLALK002340557700383"}); r.Renamed["NLALK002340557700383"] != "B2" || len(r.FavPlanned) != 3 {
		t.Errorf("renamed = %v, fav planned %d", r.Renamed, len(r.FavPlanned))
	}
	// the NDW bridge without a FIS bridge becomes its own entry; the list has all four
	var buf strings.Builder
	if err := s.writeList(&buf); err != nil || strings.Count(buf.String(), `"id"`) != 4 || !strings.Contains(buf.String(), `"name":"Brug NLTNZ001300522400303"`) {
		t.Errorf("list = %s", buf.String())
	}
}
