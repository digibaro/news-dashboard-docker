// 1.35/1.36: Luchtradar (trauma, police and coastguard helicopters and military aircraft from adsb.lol, on an OpenStreetMap map).
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const iso = (s = 0) => new Date(Date.now() + s * 1000).toISOString();
const J = async u => (await fetch(BASEURL + u)).json();

// ---- live: the server's own answer (adsb.lol may be unreachable from the test network)
{
  const cat = await J('api/catalog');
  ok(cat.aircraft === true, 'catalog: aircraft on');
  const r = await J('api/aircraft');
  ok(r.enabled && r.groups.lifeliner && r.groups.police && r.groups.sar && r.groups.military && r.budget.limit === 450 && r.interval_s === 60 && r.bounds.length === 4,
    `api: groups, budget ${r.budget.used}/${r.budget.limit}, interval ${r.interval_s}s`);
  if (r.aircraft) ok(r.aircraft.every(a => ['lifeliner', 'police', 'sar', 'military'].includes(a.group) && a.lat >= 50.65 && a.lat <= 53.65 && a.lon >= 3.2 && a.lon <= 7.3),
    `live aircraft inside the Netherlands (${r.aircraft.length}: ${r.aircraft.map(a => a.callsign || a.reg).join(', ') || 'none'})`);
  else ok(true, `adsb.lol not reached yet (${r.error || 'not yet fetched'}): live check skipped`);
}

// ---- pages with fixed data (the map tiles come from the server, which gets them from the fixtures server)
const AC = [
  { hex: '484f6b', group: 'lifeliner', name: 'Lifeliner 1', callsign: 'LIFELN1', reg: 'PH-ELP', type: 'EC35', desc: 'EUROCOPTER EC-135', lat: 52.55, lon: 6.15, alt_m: 450, speed_kmh: 225, track: 45, heli: true,
    seen: iso(-20), near: { name: 'Zwolle', km: 6, dir: 'NE' }, trail: [[52.40, 5.95], [52.47, 6.05]] },
  { hex: '485111', group: 'police', callsign: 'ZXP02', reg: 'PH-PXB', type: 'EC35', lat: 52.08, lon: 4.31, alt_m: 300, speed_kmh: 150, track: 180, heli: true, seen: iso(-5), near: { name: 'Den Haag', km: 0 } },
  { hex: 'ae1234', group: 'military', callsign: 'RCH123', reg: '07-7180', type: 'C17', desc: 'BOEING C-17 Globemaster', lat: 52.5, lon: 5.0, alt_m: 8530, speed_kmh: 833, track: 95, seen: iso(-2), near: { name: 'Purmerend', km: 4, dir: 'W' } },
  { hex: '480abc', group: 'military', callsign: 'NEPTN77', reg: 'N-317', type: 'NH90', lat: 52.92, lon: 4.78, alt_m: 0, ground: true, speed_kmh: 0, track: 0, heli: true, seen: iso(-30), near: { name: 'De Kooy', km: 0 } },
];
const DATA = list => ({ enabled: true, fetched_at: iso(-20), groups: { lifeliner: true, police: true, sar: true, military: true }, interval_s: 60,
  budget: { used: 12, limit: 450, exhausted: false }, bounds: [50.65, 3.2, 53.65, 7.3], aircraft: list });
const ctxFor = async (prefs, list = AC, opts = {}) => {
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await c.addInitScript(p => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, ...p })); } }, prefs);
  const asked = [], reqs = [];
  await c.route(/\/api\/aircraft(\?|$)/, r => { asked.push(r.request().url()); r.fulfill({ json: DATA(list) }); });
  c.on('request', r => reqs.push(r.url()));
  return { c, asked, reqs };
};
const tilesIn = reqs => reqs.filter(u => /\/api\/tile\/\d+\/\d+\/\d+\.png$/.test(u));
{
  const { c, asked, reqs } = await ctxFor({ weather: { name: 'Zwolle', lat: 52.51, lon: 6.09 } });
  const p = await c.newPage();
  await p.goto(BASEURL); await p.waitForSelector('#panel-aircraft .airleaf.leaflet-container .am-ic svg'); await p.waitForSelector('#panel-aircraft .airlist'); await p.waitForTimeout(1200);
  ok(/Luchtradar/.test(await p.textContent('#panel-aircraft h2')), 'panel Luchtradar');
  const order = await p.$$eval('.panel', l => l.map(x => x.id.replace('panel-', '')));
  ok(order.indexOf('aircraft') === order.indexOf('trains') + 1, 'Luchtradar comes after Treinstoringen');
  const tabs = await p.$$eval('#panel-aircraft .sectabs .chip', l => l.map(x => x.textContent + (x.getAttribute('aria-pressed') === 'true' ? '*' : '')));
  ok(tabs.join('|') === 'Hulpdiensten (2)*|Militair (2)', `tabs: ${tabs.join(', ')}`);
  ok(asked.length >= 1 && asked.every(u => /live=1/.test(u)), `an open panel asks with live=1 (${asked.length}×)`);
  const tiles = await p.$$eval('#panel-aircraft img.leaflet-tile-loaded', l => l.map(i => i.getAttribute('src')));
  ok(tiles.length >= 4 && tiles.every(u => /^api\/tile\/\d+\/\d+\/\d+\.png$/.test(u)), `map tiles from this server (${tiles.length}, e.g. ${tiles[0]})`);
  const ext = reqs.filter(u => new URL(u).host !== new URL(BASEURL).host);
  ok(ext.length === 0 && reqs.some(u => /\/vendor\/leaflet\.js$/.test(u)) && reqs.some(u => /\/vendor\/leaflet\.css$/.test(u)), `Leaflet from this server, no third-party requests (${ext.join(', ') || 'none'})`);
  ok(await p.locator('#panel-aircraft .am-ic').count() === 2 && await p.locator('#panel-aircraft path.am-trail').count() === 1 && await p.locator('#panel-aircraft path.am-me').count() === 1 && await p.locator('#panel-aircraft .am-ic circle.am-mark').count() === 2,
    'map: two helicopters, one trail, your location');
  ok(/OpenStreetMap/.test(await p.textContent('#panel-aircraft .leaflet-control-attribution')) && /kaart: OpenStreetMap/.test(await p.textContent('#panel-aircraft .pfoot')), 'OpenStreetMap attribution');
  const rows = await p.$$eval('#panel-aircraft .airlist li', l => l.map(x => x.textContent.replace(/\s+/g, ' ')));
  ok(rows.length === 2 && /Lifeliner 1/.test(rows[0]) && /PH-ELP · EUROCOPTER EC-135/.test(rows[0]) && /450 m hoog · 225 km\/u · 6 km ten noordoosten van Zwolle · 6 km van je/.test(rows[0]),
    `lifeliner row: ${rows[0]}`);
  ok(/Politie ZXP02/.test(rows[1]) && /boven Den Haag/.test(rows[1]), `police row: ${rows[1]}`);
  // a refresh keeps the map; a click on a marker or a row selects the flight
  await p.evaluate(() => { document.querySelector('#panel-aircraft .airleaf').dataset.mark = '1'; });
  await p.click('#panel-aircraft .sectabs .chip:has-text("Hulpdiensten")'); await p.waitForTimeout(300);
  ok(await p.getAttribute('#panel-aircraft .airleaf', 'data-mark') === '1' && await p.locator('#panel-aircraft .am-ic').count() === 2, 'a refresh keeps the same map and redraws the aircraft');
  await p.click('#panel-aircraft .am-ic[data-hex="485111"] svg'); await p.waitForTimeout(300);
  ok(await p.locator('#panel-aircraft .airlist li.sel').count() === 1 && /ZXP02/.test(await p.textContent('#panel-aircraft .airlist li.sel')) && await p.locator('#panel-aircraft .am-ic.sel[data-hex="485111"]').count() === 1,
    'clicking a marker selects the flight in the list');
  await p.click('#panel-aircraft .airlist li:first-child .linkbtn'); await p.waitForTimeout(500);
  ok(await p.locator('#panel-aircraft .am-ic.sel[data-hex="484f6b"]').count() === 1 && await p.locator('#panel-aircraft path.am-trail.sel').count() === 1 &&
    await p.getAttribute('#panel-aircraft a[href*="adsbexchange.com/?icao"]', 'href') === 'https://globe.adsbexchange.com/?icao=484f6b', 'clicking a row selects the marker and its trail, with a link to the full route');
  await p.locator('#panel-aircraft').screenshot({ path: `${OUT}/aircraft-help.png` });
  let axe = await new AxeBuilder({ page: p }).include('#panel-aircraft').analyze();
  ok(axe.violations.length === 0, `aircraft axe: ${axe.violations.map(v => v.id + ' ' + v.nodes.map(n => n.target.join(' ')).join(',')).join('; ') || 0}`);
  // Militair
  await p.click('#panel-aircraft .sectabs .chip:has-text("Militair")'); await p.waitForTimeout(300);
  ok(await p.locator('#panel-aircraft .am-ic').count() === 2 && await p.locator('#panel-aircraft .am-ic path.am-mark').count() === 1 && await p.locator('#panel-aircraft .am-ic.gnd circle.am-mark').count() === 1,
    'Militair: the C-17 as an arrow, the NH90 on the ground as a circle');
  const mil = await p.textContent('#panel-aircraft');
  ok(/RCH123/.test(mil) && /8\.530 m hoog/.test(mil) && /NEPTN77/.test(mil) && /aan de grond/.test(mil) && /boven De Kooy/.test(mil), 'military rows');
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).airTab) === 'mil', 'the tab is remembered');
  await p.locator('#panel-aircraft').screenshot({ path: `${OUT}/aircraft-mil.png` });
  // dark theme: the tiles are inverted
  await p.evaluate(() => { document.documentElement.dataset.theme = 'dark'; });
  ok(/invert/.test(await p.$eval('#panel-aircraft .leaflet-tile-pane', e => getComputedStyle(e).filter)), 'dark theme: tiles inverted');
  await p.locator('#panel-aircraft').screenshot({ path: `${OUT}/aircraft-dark.png` });
  await p.evaluate(() => { delete document.documentElement.dataset.theme; });
  // collapsed: a summary, the map is removed
  await p.click('#panel-aircraft .ptoggle');
  ok(/2 hulpdiensten in de lucht · 2 militair/.test(await p.textContent('#p-aircraft-sum')) && await p.locator('#panel-aircraft .airleaf').count() === 0, `collapsed: summary, no map (${await p.textContent('#p-aircraft-sum')})`);
  await p.click('#panel-aircraft .ptoggle'); await p.waitForSelector('#panel-aircraft .airleaf .am-ic svg');
  ok(true, 'expanding brings the map back');
  await c.close();
}
{ // nothing in the air
  const { c } = await ctxFor({ airTab: 'help' }, []);
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#panel-aircraft .airleaf'); await p.waitForSelector('#panel-aircraft .pnote');
  ok(/Er vliegt nu geen traumahelikopter, politie- of kustwachthelikopter/.test(await p.textContent('#panel-aircraft')) && await p.locator('#panel-aircraft .am-ic').count() === 0, 'empty: no emergency aircraft, the map still shows');
  await p.click('#panel-aircraft .sectabs .chip:has-text("Militair")');
  ok(/Nu geen militaire toestellen boven Nederland/.test(await p.textContent('#panel-aircraft')), 'empty: no military aircraft');
  await c.close();
}
{ // a collapsed panel asks without live=1 and loads neither Leaflet nor tiles
  const { c, asked, reqs } = await ctxFor({ panels: { order: [], collapsed: { aircraft: true }, hidden: {}, page: {} } });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForTimeout(1500);
  ok(asked.length >= 1 && asked.every(u => !/live=1/.test(u)) && !reqs.some(u => /vendor\/leaflet/.test(u)) && tilesIn(reqs).length === 0, `collapsed: no live requests, no Leaflet, no tiles (${asked.length})`);
  ok(/Lifeliner 1 in de lucht|2 hulpdiensten in de lucht/.test(await p.textContent('#p-aircraft-sum')), 'collapsed summary on load');
  await c.close();
}
{ // English and phone
  const { c, asked } = await ctxFor({ lang: 'en' }, AC, { viewport: { width: 360, height: 800 }, isMobile: true, hasTouch: true });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForTimeout(1000);
  const before = asked.length;
  ok(asked.every(u => !/live=1/.test(u)), `phone, news page: no live requests (${asked.length})`);
  await p.click('#mv-panels'); await p.waitForSelector('#panel-aircraft .airleaf .am-ic svg'); await p.waitForTimeout(800);
  ok(asked.slice(before).some(u => /live=1/.test(u)), 'phone: the panels page asks for live positions');
  const txt = await p.textContent('#panel-aircraft');
  ok(/Emergency services \(2\)/.test(txt) && /6 km northeast of Zwolle/.test(txt) && /450 m up · 225 km\/h/.test(txt) && /above Den Haag/.test(txt) && /map: OpenStreetMap/.test(txt), 'English texts');
  const w = await p.evaluate(() => [document.documentElement.scrollWidth, innerWidth, document.querySelector('#panel-aircraft .airleaf').getBoundingClientRect().width,
    document.querySelectorAll('#panel-aircraft img.leaflet-tile-loaded').length]);
  ok(w[0] <= w[1] && w[2] > 250 && w[3] > 0, `phone: no horizontal scroll, map ${Math.round(w[2])} px wide, ${w[3]} tiles`);
  await p.locator('#panel-aircraft').screenshot({ path: `${OUT}/aircraft-phone-en.png` });
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
