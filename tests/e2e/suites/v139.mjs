// 1.39: the Hardloopagenda panel (hardlopen.nl's road races; on the test servers a local copy of
// one list page with five events in the coming days, places looked up live at PDOK), and the
// settings wording Standaardlocatie / "Zoek een plaats".
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };

{
  const cat = await (await fetch(BASEURL + 'api/catalog')).json();
  ok(cat.running === true, 'catalog: running on');
  let r;
  for (let i = 0; i < 30; i++) { r = await (await fetch(BASEURL + 'api/running')).json(); if (r.data) break; await new Promise(f => setTimeout(f, 1000)); }
  const evs = r.data?.events || [];
  ok(evs.length === 5 && evs.every(e => e.url.startsWith('http://127.0.0.1:8099/evenementen/')), `api: 5 events on the calendar's own site (${evs.length})`);
  const placed = evs.filter(e => e.lat && e.lon).map(e => e.place);
  ok(placed.includes('Amersfoort') && placed.includes('Nijmegen') && placed.length >= 4, `PDOK places, Amersfoort from the title: ${placed.join(', ')}`);
}
const ctxFor = async (prefs, opts = {}) => {
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await c.addInitScript(p => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, ...p })); } }, prefs);
  return c;
};
const titles = p => p.$$eval('#panel-running .plist li a', l => l.map(a => a.textContent.trim()));
{
  const c = await ctxFor({});
  const p = await c.newPage();
  await p.goto(BASEURL); await p.waitForSelector('#panel-running .plist, #panel-running .runctl + .pnote');
  ok((await p.textContent('#panel-running h2')).trim() === 'Hardloopagenda', 'panel Hardloopagenda');
  const order = await p.$$eval('.panel', l => l.map(x => x.id.replace('panel-', '')));
  ok(order.indexOf('running') === order.indexOf('sports') + 1, 'Hardloopagenda comes after Sportagenda');
  ok(/Utrecht/.test(await p.textContent('#p-running-extra')), 'the weather location (Utrecht) by default');
  let t = await titles(p);
  ok(t.length === 1 && t[0] === 'Amersfoort Night Run', `within 25 km of Utrecht: ${t.join(' | ')}`);
  const a = await p.$eval('#panel-running .plist a', a => [a.target, a.rel, a.href]);
  ok(a[0] === '_blank' && /noopener/.test(a[1]) && a[2].startsWith('http://127.0.0.1:8099/evenementen/'), 'the event opens on hardlopen.nl in a new tab');
  const meta = await p.textContent('#panel-running .plist .m');
  ok(/Amersfoort/.test(meta) && /\d+ km/.test(meta) && /0,8 · 1,5 · 5 · 10 · 15 km/.test(meta), `meta: ${meta}`);
  await p.selectOption('#panel-running .runctl select >> nth=0', '100');
  t = await titles(p);
  ok(t.length >= 4, `within 100 km: ${t.length} runs`);
  await p.selectOption('#panel-running .runctl select >> nth=1', 'half');
  t = await titles(p);
  ok(t.length === 1 && t[0] === 'Amersfoort Night Run', `10 to 21 km: only the run with 15 km (${t.join(' | ')})`);
  await p.selectOption('#panel-running .runctl select >> nth=1', 'ultra');
  ok(/Geen lopen binnen 100 km van Utrecht/.test(await p.textContent('#panel-running')), 'no ultra: a note');
  const pr = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')));
  ok(pr.runRadius === 100 && pr.runDist === 'ultra', 'radius and distance are remembered');
  await p.selectOption('#panel-running .runctl select >> nth=1', 'all');
  ok(/hardlopen\.nl \(Atletiekunie\)/.test(await p.textContent('#panel-running .pfoot')), 'source credit');
  const axe = await new AxeBuilder({ page: p }).include('#panel-running').analyze();
  ok(axe.violations.length === 0, `running axe: ${axe.violations.map(v => v.id).join(', ') || 0}`);
  await p.locator('#panel-running').screenshot({ path: `${OUT}/running.png` });
  // the place: Instellingen → Plaats voor de hardloopagenda
  await p.click('#p-running-extra .linkbtn'); await p.waitForTimeout(300);
  ok(await p.isVisible('#set-run') && await p.evaluate(() => document.activeElement?.id) === 'run-search', '"wijzigen" opens the place setting');
  await p.evaluate(() => { const pr = JSON.parse(localStorage.getItem('ndb:prefs')); });
  ok(/zelfde als je weerlocatie \(Utrecht\)/.test(await p.textContent('#run-now')), 'hint: follows the weather location');
  ok(await p.getAttribute('#alarm-city', 'placeholder') === 'Zoek een plaats' && (await p.textContent('#alarm-default')).trim() === 'Standaardlocatie' && (await p.textContent('#air-default')).trim() === 'Standaardlocatie',
    'alarm place "Zoek een plaats"; buttons Standaardlocatie');
  await p.click('#run-default'); await p.waitForTimeout(300);
  const run = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).run);
  ok(run?.name === 'Utrecht' && /standaardlocatie van deze server/.test(await p.textContent('#run-msg')), 'Standaardlocatie sets the server default');
  await p.click('#run-wx'); await p.waitForTimeout(300);
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).run) === null, 'Zelfde als weerlocatie clears the own place');
  const ax2 = await new AxeBuilder({ page: p }).include('#set-run').analyze();
  ok(ax2.violations.length === 0, `settings axe: ${ax2.violations.map(v => v.id).join(', ') || 0}`);
  await c.close();
}
{ // an own place, collapsed summary, sanitised prefs
  const c = await ctxFor({ run: { name: 'Nijmegen', lat: 51.84, lon: 5.86 }, runRadius: 10, runDist: 'bogus', panels: { order: [], collapsed: { running: true }, hidden: {}, page: {} } });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForTimeout(2500);
  const sum = await p.textContent('#p-running-sum');
  ok(/^: 1 loop binnen 10 km · eerstvolgende /.test(sum), `collapsed summary around Nijmegen: "${sum}"`);
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).runDist) === 'all', 'an unknown distance falls back to all');
  await c.close();
}
{ // English and phone
  const c = await ctxFor({ lang: 'en' }, { viewport: { width: 360, height: 800 }, isMobile: true, hasTouch: true });
  const p = await c.newPage(); await p.goto(BASEURL);
  await p.click('#mv-panels'); await p.waitForSelector('#panel-running .plist');
  const txt = await p.textContent('#panel-running');
  ok(/Running calendar/.test(txt) && /Within 25 km/.test(txt) && /All distances/.test(txt) && /Dutch athletics union/.test(txt), 'English texts');
  const w = await p.evaluate(() => [document.documentElement.scrollWidth, innerWidth]);
  ok(w[0] <= w[1], `phone: no horizontal scroll (${w})`);
  await p.locator('#panel-running').screenshot({ path: `${OUT}/running-phone-en.png` });
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
