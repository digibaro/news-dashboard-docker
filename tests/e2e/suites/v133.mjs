// 1.33: Bruggen in the Verkeer panel (NDW bridge openings, favourites and planned), Euro98 in Brandstofprijzen.
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const iso = min => new Date(Date.now() + min * 60000).toISOString();
const hm = min => new Date(Date.now() + min * 60000).toLocaleTimeString('nl-NL', { hour: '2-digit', minute: '2-digit', timeZone: 'Europe/Amsterdam' });

// ---- live: the server answers with bridges, and Euro98 is in the fuel prices
{
  const r = await (await fetch(BASEURL + 'api/bridges')).json();
  const d = r.data;
  ok(r.enabled === true && d && Array.isArray(d.open) && Array.isArray(d.planned) && Array.isArray(d.fav_planned) && Array.isArray(d.bridges), `api/bridges shape (${d?.open.length} open, ${d?.planned.length} planned, ${d?.bridges.length} bridges)`);
  if (d?.bridges.length) {
    const named = d.bridges.filter(x => !/^Brug NL/.test(x.name));
    ok(d.bridges.every(x => /^[A-Z][A-Z0-9]{2,24}$/.test(x.id) && x.lat > 50 && x.lat < 54), 'every bridge has an id and a position in NL');
    ok(named.length > 0 || r.error, `bridge names from vaarweginformatie.nl (${named.length}/${d.bridges.length}, e.g. ${named.slice(0, 3).map(x => x.name + ' ' + x.city).join('; ')})`);
    const ids = new Set(d.bridges.map(x => x.id));
    ok([...d.open, ...d.planned].every(o => ids.has(o.id)), 'every opening refers to a known bridge');
    ok(d.planned.length <= 60 && d.planned.every((o, i, a) => !i || a[i - 1].start <= o.start) && d.planned.every(o => Date.parse(o.end) > Date.now()), 'planned: at most 60, sorted, none in the past');
    const fav = d.bridges[0].id;
    const f = (await (await fetch(BASEURL + 'api/bridges?fav=' + fav)).json()).data;
    ok(f.fav_planned.every(o => o.id === fav) && f.fav_planned.length <= 3 && f.planned.every(o => o.id !== fav), `favourites get their own list (${f.fav_planned.length} for ${d.bridges[0].name})`);
  } else ok(true, `NDW not reachable or no bridges yet (${r.error || 'empty'}): live checks skipped`);
  const list = await (await fetch(BASEURL + 'api/bridges/list')).json();
  if (list.bridges?.length) {
    const names = new Set(list.bridges.map(x => x.name));
    ok(list.bridges.length > 1000 && names.has('Botlekbrug') && names.has('Kaagbrug') && list.bridges.every(x => /^[A-Z][A-Z0-9]{2,24}$/.test(x.id)), `every movable bridge can be chosen (${list.bridges.length}, incl. Botlekbrug and Kaagbrug)`);
  } else ok(true, 'vaarweginformatie.nl not reachable: bridge list check skipped');
  const fu = await (await fetch(BASEURL + 'api/fuel')).json();
  if (fu.data?.prices?.length) {
    const names = fu.data.prices.map(p => p.name);
    ok(names.join('|') === 'Euro95 (E10)|Euro98 (E5)|Diesel|LPG', `fuel prices: ${names.join(', ')}`);
    const e95 = fu.data.prices.find(p => p.fuel === 'euro95'), e98 = fu.data.prices.find(p => p.fuel === 'euro98');
    ok(e98.price > e95.price && e98.price - e95.price < 0.5, `Euro98 (${e98.price}) a little dearer than Euro95 (${e95.price})`);
  } else ok(true, `UnitedConsumers not reachable (${fu.error || 'no data'}): fuel check skipped`);
}

// ---- page with fixed data
const BR = [
  { id: 'B11', name: 'Beatrixbrug Zaandam', city: 'Zaandam', lat: 52.43, lon: 4.82 },
  { id: 'B12', name: 'Leeghwaterbrug Alkmaar', city: 'Alkmaar', lat: 52.63, lon: 4.75 },
  { id: 'B13', name: 'Prinsenbrug (Brug 3)', city: 'Haarlem', lat: 52.38, lon: 4.64 },
  { id: 'B14', name: 'Calandbrug Rozenburg', city: 'Rozenburg', lat: 51.9, lon: 4.2 },
].map(x => ({ ...x, ndw: true }));
const BOTLEK = { id: 'B15', name: 'Botlekbrug', city: 'Rotterdam', lat: 51.87, lon: 4.33 }; // NDW never reported it
const answer = fav => ({
  enabled: true, fetched_at: iso(-1), source: { name: 'NDW (brugopeningen)' },
  data: {
    bridges: [...BR, ...(fav.includes('B15') ? [BOTLEK] : [])],
    open: [{ id: 'B14', start: iso(-4) }],
    ...(fav.includes('NLZAA002360562400027') ? { renamed: { NLZAA002360562400027: 'B11' } } : {}),
    fav_planned: [
      ...(fav.includes('B12') ? [{ id: 'B12', start: iso(30), end: iso(36) }, { id: 'B12', start: iso(90), end: iso(96) }] : []),
    ],
    planned: [
      { id: 'B13', start: iso(10), end: iso(16) },
      ...(fav.includes('B12') ? [] : [{ id: 'B12', start: iso(30), end: iso(36) }]),
      { id: 'B11', start: iso(45), end: iso(51) },
    ],
  },
});
const ctxFor = async (prefs, opts = {}) => {
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await c.addInitScript(p => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, ...p })); } }, prefs);
  const asked = [];
  await c.route('**/api/bridges/list', r => r.fulfill({ json: { enabled: true, bridges: [...BR, BOTLEK] } }));
  await c.route(/\/api\/bridges(\?|$)/, r => { const fav = new URL(r.request().url()).searchParams.get('fav') || ''; asked.push(fav); r.fulfill({ json: answer(fav) }); });
  return { c, asked };
};
const rows = p => p.$$eval('#panel-traffic .brsec .brg', l => l.map(x => ({ star: x.querySelector('.brstar').textContent, name: x.querySelector('.where b').textContent, sub: x.querySelector('.where span').textContent, st: x.querySelector('.brst').textContent, cls: x.querySelector('.brst').className })));
{
  const { c, asked } = await ctxFor({});
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#panel-traffic .brsec');
  const tabs = await p.$$eval('#panel-traffic .brsec .sectabs .chip', l => l.map(x => x.textContent + (x.getAttribute('aria-pressed') === 'true' ? '*' : '')));
  ok(tabs.join('|') === 'Favorieten (0)*|Gepland', `tabs: ${tabs.join(', ')}`);
  ok(/Nog geen favorieten/.test(await p.textContent('#panel-traffic .brsec')), 'no favourites yet: a hint how to choose');
  await p.click('#panel-traffic .brsec .sectabs .chip:has-text("Gepland")');
  let r = await rows(p);
  ok(r.length === 4 && r[0].name === 'Calandbrug Rozenburg' && r[0].st === `Open sinds ${hm(-4)}` && /open/.test(r[0].cls) && r[0].sub === 'Rozenburg · nu open', `Gepland: the open bridge first (${r[0]?.name}: ${r[0]?.st})`);
  ok(r.slice(1).map(x => x.name).join('|') === 'Prinsenbrug (Brug 3)|Leeghwaterbrug Alkmaar|Beatrixbrug Zaandam' && r[1].st === `${hm(10)}–${hm(16)}` && r.every(x => x.star === '☆'), `then the planned openings in order (${r.slice(1).map(x => x.name + ' ' + x.st).join('; ')})`);
  await p.locator('#panel-traffic').screenshot({ path: `${OUT}/bridges-planned.png` });
  const axe = await new AxeBuilder({ page: p }).include('#panel-traffic .brsec').analyze();
  ok(axe.violations.length === 0, `bridges axe: ${axe.violations.map(v => v.id).join(', ') || 0}`);
  await p.click('#panel-traffic .brg:has-text("Leeghwaterbrug") .brstar'); await p.waitForTimeout(500);
  const pr = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).bridges);
  ok(pr.length === 1 && pr[0].id === 'B12' && pr[0].name === 'Leeghwaterbrug Alkmaar' && pr[0].city === 'Alkmaar', `☆ adds a favourite (${JSON.stringify(pr)})`);
  ok(asked.at(-1) === 'B12', `the panel asks for the favourite's openings (fav=${asked.at(-1)})`);
  r = await rows(p);
  ok(!r.some(x => x.name === 'Leeghwaterbrug Alkmaar'), 'a favourite is no longer under Gepland');
  await p.click('#panel-traffic .brsec .sectabs .chip:has-text("Favorieten")');
  r = await rows(p);
  ok(r.length === 1 && r[0].star === '★' && r[0].st === `Open ${hm(30)}–${hm(36)}` && r[0].sub === `Alkmaar · daarna ${hm(90)}`, `Favorieten: next opening and the one after (${r[0]?.st} · ${r[0]?.sub})`);
  await p.reload(); await p.waitForSelector('#panel-traffic .brsec .brg');
  ok((await p.getAttribute('#panel-traffic .brsec .sectabs .chip[aria-pressed="true"]', 'class')) && /Favorieten \(1\)/.test(await p.textContent('#panel-traffic .brsec .sectabs [aria-pressed="true"]')), 'the tab and the favourite survive a reload');
  // settings: search and choose
  await p.click('#open-settings');
  ok(/1 favorieten · 5 beweegbare bruggen, 4 met gegevens van NDW/.test(await p.textContent('#br-count')), `settings count: ${await p.textContent('#br-count')}`);
  await p.fill('#br-q', 'rozen');
  let labels = await p.$$eval('#br-list label', l => l.map(x => x.textContent));
  ok(labels.length === 1 && /Calandbrug Rozenburg · Rozenburg/.test(labels[0]), `search finds by place (${labels.join(' | ')})`);
  await p.check('#br-list input'); await p.waitForTimeout(400);
  await p.fill('#br-q', '');
  labels = await p.$$eval('#br-list label', l => l.map(x => x.textContent + (x.querySelector('input').checked ? '✓' : '')));
  ok(labels.slice(0, 2).every(x => x.endsWith('✓')) && labels.length === 4, `favourites first in the list (${labels.join(' | ')})`);
  ok(!labels.some(x => /Botlekbrug/.test(x)), 'without a search: favourites and the bridges NDW reports');
  await p.fill('#br-q', 'botlek');
  labels = await p.$$eval('#br-list label', l => l.map(x => x.textContent));
  ok(labels.length === 1 && /Botlekbrug · Rotterdam · nog geen gegevens van NDW/.test(labels[0]), `a bridge NDW never reported can be found and is marked (${labels.join(' | ')})`);
  await p.check('#br-list input'); await p.waitForTimeout(400); await p.fill('#br-q', '');
  const ax2 = await new AxeBuilder({ page: p }).include('#set-bridges').analyze();
  ok(ax2.violations.length === 0, `settings axe: ${ax2.violations.map(v => v.id).join(', ') || 0}`);
  await p.keyboard.press('Escape');
  r = await rows(p);
  ok(r.length === 3 && r.find(x => x.name === 'Calandbrug Rozenburg')?.st === `Open sinds ${hm(-4)}`, `a favourite that is open shows "Open sinds" (${r.map(x => x.name + ': ' + x.st).join('; ')})`);
  const bot = r.find(x => x.name === 'Botlekbrug');
  ok(bot?.st === '—' && bot.sub === 'Rotterdam · nog geen gegevens van NDW', `a favourite without NDW data says so (${bot?.st} · ${bot?.sub})`);
  await p.click('#panel-traffic .brg:has-text("Calandbrug") .brstar'); await p.waitForTimeout(300);
  ok((await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).bridges.map(x => x.id))).join() === 'B12,B15', '★ removes a favourite');
  await c.close();
}
{ // a favourite saved with an NDW code (before 1.33.0 was released) gets the bridge id
  const { c } = await ctxFor({ bridges: [{ id: 'NLZAA002360562400027', name: 'Beatrixbrug Zaandam', city: 'Zaandam' }] });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#panel-traffic .brsec'); await p.waitForTimeout(500);
  ok((await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).bridges.map(b => b.id).join())) === 'B11', 'an NDW-code favourite is renamed to the bridge id');
  await c.close();
}
{ // English and phone
  const { c } = await ctxFor({ lang: 'en', bridges: [{ id: 'B11', name: 'Beatrixbrug Zaandam', city: 'Zaandam' }] }, { viewport: { width: 360, height: 780 }, isMobile: true, hasTouch: true });
  const p = await c.newPage(); await p.goto(BASEURL);
  await p.click('#mv-panels'); await p.waitForSelector('#panel-traffic .brsec');
  const r = await rows(p);
  ok(r.length === 1 && r[0].st === 'Closed' && r[0].sub === 'Zaandam · no opening planned (24 hours)', `en: a favourite without plans: ${r[0]?.st} · ${r[0]?.sub}`);
  ok(/Favourites \(1\)/.test(await p.textContent('#panel-traffic .brsec .sectabs')), 'en: tab names');
  await p.waitForTimeout(800); // the panels slide in
  const sw = await p.evaluate(() => document.documentElement.scrollWidth);
  const wide = await p.evaluate(() => [...document.querySelectorAll('body *')].filter(e => e.getBoundingClientRect().right > 361 && e.offsetParent).slice(0, 6).map(e => `${e.tagName}.${e.className}#${e.id} ${Math.round(e.getBoundingClientRect().right)}`));
  ok(sw <= 360, `360px: no horizontal scroll (${sw}${sw > 360 ? ': ' + wide.join(', ') : ''})`);
  await p.locator('#panel-traffic .brsec').screenshot({ path: `${OUT}/bridges-phone.png` });
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
