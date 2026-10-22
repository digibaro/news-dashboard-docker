// 1.34: Bekendmakingen, Productwaarschuwingen, Energieprijzen tabs (Mix & gas, Netcongestie),
// economy additions (mortgage rate, house prices, bankruptcies), phishing on Dutch brands, BNR.
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const iso = (days = 0) => new Date(Date.now() + days * 864e5).toISOString();
const J = async u => (await fetch(BASEURL + u)).json();

// ---- live: the server answers for every new source (a source that is unreachable from the test network is skipped)
{
  const cat = await J('api/catalog');
  ok(cat.sources.some(s => s.id === 'bnr' && /Google News/.test(s.name)), 'BNR (via Google News) is a source');
  const tech = cat.presets.find(p => p.id === 'tech');
  ok(tech.sources.includes('computable') && tech.sources.includes('agconnect'), 'Computable and AG Connect are in the Tech preset');
  ok(cat.recalls === true && cat.notices?.enabled === true && cat.notices.radius === 500 && cat.grid === true, 'catalog: recalls, notices (500 m) and grid are on');

  const en = await J('api/energy');
  const mix = en.mix?.data;
  if (mix) {
    const sum = mix.sources.reduce((a, s) => a + s.mw, 0);
    ok(mix.share > 0 && mix.share <= 100 && mix.sources.length >= 3 && Math.abs(sum - mix.total_mw) <= mix.sources.length && mix.load_mw > 0,
      `energy mix: ${mix.share}% renewable, ${mix.sources.map(s => s.key + ' ' + s.mw).join(', ')}`);
    ok(Date.now() - Date.parse(mix.at) < 12 * 36e5, `the mix is recent (${mix.at})`);
    ok(Array.isArray(mix.forecast) && mix.forecast.every(p => p.share >= 0 && p.share <= 100), `forecast per hour (${mix.forecast.length})`);
  } else ok(en.mix && (en.mix.error || true), `energy-charts not reachable (${en.mix?.error || 'not yet fetched'}): mix check skipped`);
  ok(en.gas_storage && typeof en.gas_storage.key === 'boolean', `gas storage: key ${en.gas_storage?.key}`);

  const rc = await J('api/recalls');
  if (rc.items) ok(rc.items.every(x => x.url.startsWith('https://www.nvwa.nl/') && x.title && !/^Veiligheidswaarschuwing/i.test(x.title)), `NVWA recalls (${rc.items.length}, e.g. ${rc.items[0]?.title})`);
  else ok(true, `NVWA not reachable (${rc.error || 'not yet fetched'}): recalls check skipped`);

  const ec = (await J('api/economy')).data;
  if (ec?.house_prices) ok(ec.house_prices.length >= 6 && ec.house_avg.value > 100000 && ec.bankruptcies.length >= 6 && ec.mortgage.length >= 6 && ec.mortgage.at(-1).value > 0.5 && ec.mortgage.at(-1).value < 10,
    `economy: house prices ${ec.house_prices.at(-1).value}% (avg ${ec.house_avg.value}), ${ec.bankruptcies.at(-1).value} bankruptcies, mortgage ${ec.mortgage.at(-1).value}%`);
  else ok(true, `CBS not reachable (${ec?.errors?.join('; ') || 'no data'}): economy check skipped`);

  const br = await J('api/breaches');
  ok(br.phishing_nl && (Array.isArray(br.phishing_nl.sites) || br.phishing_nl.error !== undefined || true), 'breaches: phishing_nl section');
  if (br.phishing_nl?.sites) ok(br.phishing_nl.sites.every(s => s.host.includes('[.]') && !/^h..ps?:/.test(s.host) && s.brand), `phishing hosts are defanged (${br.phishing_nl.sites.length})`);

  const g = await J('api/grid?pc=3511AB');
  if (g.area) ok(g.area.name && g.area.operator && [-1, 0, 1, 2, 3].includes(g.area.withdrawal.tennet), `grid congestion 3511AB: ${g.area.name} (${g.area.operator}), queue ${g.area.withdrawal.queue || 'none'}`);
  else ok(true, `capacity map not reachable (${g.error || JSON.stringify(g)}): grid check skipped`);
  ok((await fetch(BASEURL + 'api/grid?pc=x')).status === 400, 'grid: a bad postcode is refused');

  const n = await J('api/notices?postcode=2311EZ&number=1');
  if (n.items) ok(n.gemeente === 'Leiden' && n.items.every(x => x.m <= 500 && x.url.startsWith('https://zoek.officielebekendmakingen.nl/')), `notices near 2311EZ 1: ${n.items.length} in ${n.gemeente}`);
  else ok(true, `officielebekendmakingen.nl or PDOK not reachable (${n.error || JSON.stringify(n)}): notices check skipped`);
}

// ---- pages with fixed data
const ENERGY = {
  enabled: true, fetched_at: iso(), data: { electricity: Array.from({ length: 24 }, (_, i) => ({ t: new Date(new Date().setHours(i, 0, 0, 0)).toISOString(), p: 0.2 + i / 100 })), gas: [] },
  mix: { fetched_at: iso(), data: { at: new Date(Date.now() - 3 * 36e5).toISOString(), share: 61.4, total_mw: 10000, load_mw: 12000,
    sources: [{ key: 'wind', mw: 5000 }, { key: 'gas', mw: 2500 }, { key: 'coal', mw: 1200 }, { key: 'solar', mw: 800 }, { key: 'nuclear', mw: 500 }],
    forecast: Array.from({ length: 24 }, (_, i) => ({ t: Math.floor(new Date(new Date().setHours(i, 0, 0, 0)) / 1000), share: i === 22 || i === 23 ? 90 : 40 })) } },
  gas_storage: { key: true, fetched_at: iso(), data: { nl: { name: 'Netherlands', day: '2026-10-20', full: 81.2, trend: 0.31, twh: 112.5, year_ago: 90.1 }, eu: { name: 'EU', day: '2026-10-20', full: 88.4, trend: 0.2, twh: 900 } } },
  grid: true,
};
const GRID = { enabled: true, fetched_at: iso(), area: { id: 'CM60', name: 'Utrecht Lage Weide', operator: 'Stedin',
  withdrawal: { queue: '28.1 MW', requests: '49', solved: '2034', tennet: 3, tennet_solved: '2035' }, injection: { tennet: 0 },
  projects: [{ name: 'Nieuw Transformatorstation - De Wetering', operator: 'Stedin', when: '2028' }] } };
const NOTICES = { enabled: true, gemeente: 'Utrecht', street: 'Teststraat', radius: 500, days: 14, fetched_at: iso(), items: [
  { title: 'Verleende omgevingsvergunning, dakkapel, Teststraat 3 3511AB Utrecht', type: 'omgevingsvergunning', date: iso().slice(0, 10), url: 'https://zoek.officielebekendmakingen.nl/gmb-2026-1.html', m: 80 },
  { title: 'Verkeersbesluit parkeerplaats', type: 'verkeersbesluit', date: iso(-3).slice(0, 10), url: 'https://zoek.officielebekendmakingen.nl/gmb-2026-2.html', m: 1200 } ] };
const RECALLS = { enabled: true, fetched_at: iso(), items: [
  { title: 'Jumbo pulled chicken', url: 'https://www.nvwa.nl/a', published: iso(-1) },
  { title: 'Peterselie krul 40g van ALDI (allergenen)', url: 'https://www.nvwa.nl/b', published: iso(-2), allergen: true },
  { title: 'Oud product', url: 'https://www.nvwa.nl/c', published: iso(-40) } ] };
const SITES = [{ brand: 'PostNL', host: 'postnl-pakket.example[.]top', seen: iso(-1), source: 'OpenPhish' }];
const ctxFor = async (prefs, opts = {}) => {
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await c.addInitScript(p => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, ...p })); } }, prefs);
  const asked = { grid: [], notices: [] };
  await c.route(/\/api\/energy(\?|$)/, r => r.fulfill({ json: ENERGY }));
  await c.route(/\/api\/grid\?/, r => { asked.grid.push(r.request().url()); r.fulfill({ json: GRID }); });
  await c.route(/\/api\/notices\?/, r => { asked.notices.push(r.request().url()); r.fulfill({ json: NOTICES }); });
  await c.route(/\/api\/recalls(\?|$)/, r => r.fulfill({ json: RECALLS }));
  await c.route(/\/api\/waste\?/, r => r.fulfill({ json: { enabled: true, own: true, pickups: [], provider: 'x', calendar: 'Test', home: 'https://example.nl/' } }));
  await c.route(/\/api\/breaches(\?|$)/, async r => { const res = await r.fetch(); const j = await res.json(); j.phishing_nl = { fetched_at: iso(), sites: SITES }; r.fulfill({ json: j }); });
  await c.route(/\/api\/economy(\?|$)/, async r => { const res = await r.fetch(); const j = await res.json();
    j.data = { ...(j.data || { inflation: [], unemployment: [], eurusd: [] }), mortgage: [{ period: '2026-07', value: 3.85 }, { period: '2026-08', value: 3.91 }, { period: '2026-09', value: 3.88 }],
      house_prices: [{ period: '2026-07', value: 3.9 }, { period: '2026-08', value: 3.3 }, { period: '2026-09', value: 2.9 }], house_avg: { period: '2026-09', value: 504658 },
      bankruptcies: [{ period: '2026-07', value: 294 }, { period: '2026-08', value: 335 }, { period: '2026-09', value: 358 }] };
    j.enabled = true; r.fulfill({ json: j }); });
  return { c, asked };
};
const ADDR = { waste: { postcode: '3511AB', number: 1, suffix: '', provider: '' } };
{
  const { c, asked } = await ctxFor({});
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#panel-energy .sectabs');
  const tabs = await p.$$eval('#panel-energy .sectabs .chip', l => l.map(x => x.textContent + (x.getAttribute('aria-pressed') === 'true' ? '*' : '')));
  ok(tabs.join('|') === 'Prijzen*|Mix & gas|Netcongestie', `energy tabs: ${tabs.join(', ')}`);
  ok(/stroom nu/.test(await p.textContent('#panel-energy')), 'Prijzen tab shows the prices as before');
  await p.click('#panel-energy .sectabs .chip:has-text("Mix & gas")');
  const txt = await p.textContent('#panel-energy');
  ok(/61%\s*duurzaam/.test(txt) && /wind 50%/.test(txt) && /gas 25%/.test(txt) && /Groenste 3 uur vandaag: \d\d:00–\d\d:00 \(~\d+% duurzaam\)/.test(txt), `mix tab: share, legend and greenest hours`);
  ok(/Nederland 81,2% vol/.test(txt) && /vorig jaar 90,1%/.test(txt) && /EU 88,4% vol/.test(txt), 'gas storage NL and EU');
  ok(/zonnepanelen op daken tellen grotendeels niet mee/.test(txt), 'the mix says rooftop solar is missing');
  const widths = await p.$$eval('#panel-energy .mixbar span', l => l.map(x => parseFloat(x.style.width)));
  ok(widths.join() === '50,25,12,8,5', `mix bar segments (${widths.join()})`);
  await p.locator('#panel-energy').screenshot({ path: `${OUT}/energy-mix.png` });
  let axe = await new AxeBuilder({ page: p }).include('#panel-energy').analyze();
  ok(axe.violations.length === 0, `energy mix axe: ${axe.violations.map(v => v.id).join(', ') || 0}`);
  await p.click('#panel-energy .sectabs .chip:has-text("Netcongestie")');
  ok(/Stel je adres in/.test(await p.textContent('#panel-energy')) && asked.grid.length === 0, 'Netcongestie without an address: asks for one and fetches nothing');
  // recalls and notices
  const rc = await p.textContent('#panel-recalls');
  ok(/Jumbo pulled chicken/.test(rc) && /allergie/.test(rc) && !/Oud product/.test(rc), 'Productwaarschuwingen: last 30 days with an allergy label');
  ok(/Stel je adres in/.test(await p.textContent('#panel-notices')) && asked.notices.length === 0, 'Bekendmakingen without an address: asks for one');
  // economy and phishing
  const eco = await p.textContent('#panel-economy');
  ok(/Hypotheekrente\s*3,88%/.test(eco) && /Huizenprijzen\s*\+2,9%/.test(eco) && /€\s?504\.658/.test(eco) && /Faillissementen\s*358/.test(eco), 'economy: mortgage, house prices, bankruptcies');
  const ph = p.locator('#panel-breaches .phgrp', { hasText: 'Nederlandse merken' });
  ok(/PostNL/.test(await ph.textContent()) && /postnl-pakket\.example\[\.\]top/.test(await ph.textContent()) && (await ph.locator('a', { hasText: 'postnl' }).count()) === 0, 'phishing on Dutch brands: defanged host, no link');
  axe = await new AxeBuilder({ page: p }).include('#panel-recalls').include('#panel-notices').include('#panel-economy').include('#panel-breaches').analyze();
  ok(axe.violations.length === 0, `new panels axe: ${axe.violations.map(v => v.id).join(', ') || 0}`);
  await c.close();
}
{ // with an address
  const { c, asked } = await ctxFor({ ...ADDR, energyTab: 'grid' });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#panel-energy .ggrid');
  const txt = await p.textContent('#panel-energy');
  ok(/Utrecht Lage Weide/.test(txt) && /Stedin/.test(txt) && /Wachtrij/.test(txt) && /28\.1 MW · 49 verzoeken/.test(txt) && /opgelost verwacht in 2034/.test(txt) && /TenneT: tekort \(2035\)/.test(txt) && /Geen wachtrij/.test(txt),
    'Netcongestie: withdrawal queue, solved year, TenneT level, no queue for injection');
  ok(asked.grid.length === 1 && /pc=3511AB/.test(asked.grid[0]), `only the postcode is sent (${asked.grid[0]})`);
  await p.waitForSelector('#panel-notices .plist');
  const n = await p.$$eval('#panel-notices .plist li', l => l.map(x => x.textContent));
  ok(n.length === 2 && /dakkapel/.test(n[0]) && /80 m$/.test(n[0]) && /1,2 km$/.test(n[1]) && /Omgevingsvergunning ·/.test(n[0]), `notices: ${n.join(' | ')}`);
  ok(/postcode=3511AB&number=1$/.test(asked.notices[0]), 'notices ask with postcode and number only');
  ok(/Binnen 500 m van je adres, afgelopen 14 dagen · Utrecht/.test(await p.textContent('#panel-notices')), 'notices header');
  await p.locator('#panel-notices').screenshot({ path: `${OUT}/notices.png` });
  const axe = await new AxeBuilder({ page: p }).include('#panel-energy').include('#panel-notices').analyze();
  ok(axe.violations.length === 0, `grid and notices axe: ${axe.violations.map(v => v.id).join(', ') || 0}`);
  await c.close();
}
{ // English and phone
  const { c } = await ctxFor({ ...ADDR, lang: 'en', energyTab: 'mix' }, { viewport: { width: 360, height: 780 }, isMobile: true, hasTouch: true });
  const p = await c.newPage(); await p.goto(BASEURL);
  await p.click('#mv-panels'); await p.waitForSelector('#panel-energy .mixbar');
  const txt = await p.textContent('#panel-energy');
  ok(/Prices/.test(txt) && /Grid congestion/.test(txt) && /renewable/.test(txt) && /Gas storage/.test(txt) && /Netherlands 81\.2% full/.test(txt), 'en: energy tabs and mix');
  ok(/Product warnings/.test(await p.textContent('#panel-recalls h2')) && /allergy/.test(await p.textContent('#panel-recalls')), 'en: product warnings');
  ok(/Official notices/.test(await p.textContent('#panel-notices h2')) && /Within 500 m of your address/.test(await p.textContent('#panel-notices')), 'en: official notices');
  await p.click('#panel-energy .sectabs .chip:has-text("Grid congestion")'); await p.waitForSelector('#panel-energy .ggrid');
  ok(/Waiting list/.test(await p.textContent('#panel-energy')) && /shortage/.test(await p.textContent('#panel-energy')), 'en: grid congestion');
  await p.waitForTimeout(800); // the panels slide in
  const sw = await p.evaluate(() => document.documentElement.scrollWidth);
  const wide = await p.evaluate(() => [...document.querySelectorAll('body *')].filter(e => e.getBoundingClientRect().right > 361 && e.offsetParent).slice(0, 6).map(e => `${e.tagName}.${e.className}#${e.id} ${Math.round(e.getBoundingClientRect().right)}`));
  ok(sw <= 360, `360px: no horizontal scroll (${sw}${sw > 360 ? ': ' + wide.join(', ') : ''})`);
  await p.locator('#panel-energy').screenshot({ path: `${OUT}/grid-phone.png` });
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
