// 1.31: Security-adviezen tabs (Edge-apparaten, Exploits), Cyberdreigingen tabs (Malware in NL, IOC's),
// the panel Dreigingsbeeld NL and the news source Hacker News.
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const axe = async (p, sel, m) => { const r = await new AxeBuilder({ page: p }).include(sel).analyze(); ok(r.violations.length === 0, `${m} axe: ${r.violations.map(v => v.id + ' ' + v.nodes[0].target).join(', ') || 0}`); };
const iso = ms => new Date(Date.now() + ms).toISOString();
const day = n => new Date(Date.now() + n * 864e5).toISOString().slice(0, 10);
const adv = (id, source, sev, title, h) => ({ id, source, title, url: 'https://example.org/' + id, published: iso(-h * 36e5), severity: sev, cves: ['CVE-2026-1' + id.length], products: [] });
const EDGE = [adv('fg-1', 'fortinet', 'medium', 'TESTEDGE FortiClient: arbitrary process termination', 2), adv('cs-1', 'cisco', 'critical', 'TESTEDGE Catalyst SD-WAN Manager API Authentication Bypass', 5),
  adv('pa-1', 'paloalto', 'high', 'TESTEDGE GlobalProtect App: Buffer Overflow', 1)];
const EXPL = { enabled: true,
  exploitdb: { fetched_at: iso(0), items: [{ id: '52692', title: 'Teltonika_RutOS 00.07.06.21 - command injection', kind: 'remote', url: 'https://www.exploit-db.com/exploits/52692', published: iso(-864e5) },
    { id: '52690', title: 'WordPress 7.0.2 - Path Traversal', kind: 'webapps', url: 'https://www.exploit-db.com/exploits/52690', published: iso(-2 * 864e5) }] },
  epss: { fetched_at: iso(0), date: day(0), since: day(-7), kev: ['CVE-2026-42271'], risers: [
    { cve: 'CVE-2026-81578', epss: 0.852, prev: 0.045, percentile: 0.998 }, { cve: 'CVE-2026-42271', epss: 0.926, prev: 0.128, percentile: 0.999 }] } };
const URLHAUS = { fetched_at: iso(0), data: { online: 374, week: 390, threats: { malware_download: 374 },
  asns: [{ asn: 209373, name: 'SWISSNET LLC', count: 149 }, { asn: 202412, name: 'Omegatech LTD', count: 75 }],
  newest: [{ added: iso(-36e5), url: 'hxxp://176.65.148.144/bins/Hilix[.]ppc', threat: 'malware_download' }] } };
const TFOX = { fetched_at: iso(0), data: { total: 1599, types: { payload: 900 }, families: [{ name: 'Mirai', count: 950, malpedia: 'https://malpedia.caad.fkie.fraunhofer.de/details/elf.mirai' }, { name: 'AMOS', count: 55 }],
  newest: [{ ioc: 'petroazaran[.]com', type: 'domain', threat: 'payload_delivery', family: 'ClearFake', confidence: 90, first_seen: iso(-36e5) },
    { ioc: 'f2911fe9394e3d09f36be8d6c17b62fde5fab04d1e985173f23a543111c97fac', type: 'sha256_hash', threat: 'payload', family: 'Mirai', confidence: 100, first_seen: iso(-36e5) }] } };
const NEWS = [ // [kept?] cyber headlines: only those at Dutch organisations stay
  { source: 'security-nl', title: 'Gemeente Utrecht getroffen door ransomware-aanval', keep: true },
  { source: 'tweakers', title: 'Odido meldt datalek met klantgegevens', keep: true },
  { source: 'omroep-brabant', title: 'DDoS-aanval legt website van omroep plat', keep: true },
  { source: 'security-nl', title: 'Finse organisaties gehackt via Citrix-lekken meldt Finse overheid', keep: false },
  { source: 'tweakers', title: 'Hackers dringen officieel X-account van Microsoft binnen', keep: false },
  { source: 'omroep-brabant', title: 'Datalek bij Duitse webshop raakt ook Brabanders', keep: false },
  { source: 'tweakers', title: 'Nieuwe telefoon van Fairphone heeft betere camera', keep: false }];
const RADAR = { enabled: true, country: 'NL', radar: { fetched_at: iso(0), data: {
  trend: [0.22, 0.25, 1, 0.58, 0.37, 0.89, 0.08], days: [-7, -6, -5, -4, -3, -2, -1].map(day),
  vectors: [{ name: 'SYN Flood', share: 62.2 }, { name: 'UDP Flood', share: 24 }, { name: 'Mirai (UDP) Flood', share: 8 }],
  origins: [{ name: 'United States', code: 'US', share: 21.4 }, { name: 'China', code: 'CN', share: 6 }],
  hijacks: [{ at: iso(-5 * 36e5), asn: 60781, victims: [207449], prefixes: ['2a13:8c85::/32'], countries: ['GB'], ongoing: true, score: 8 }],
  leaks: [{ at: iso(-30 * 36e5), asn: 269524, countries: ['CO', 'NL', 'BR'] }], hijacks_n: 2, leaks_n: 5,
  as_names: { 60781: 'LeaseWeb Netherlands B.V.', 207449: 'Example UK Ltd', 269524: 'EDGEUNO S.A.S' } } } };

async function open(w, { scheme = 'light', lang = 'nl', mobile = false, nl = RADAR, live = false } = {}) {
  const ctx = await b.newContext({ viewport: { width: w, height: 1000 }, colorScheme: scheme, serviceWorkers: 'block', isMobile: mobile, hasTouch: mobile });
  await ctx.addInitScript(l => { if (sessionStorage.getItem('t-init')) return; sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, lang: l })); }, lang); // once per tab: a reload keeps what the page saved
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  if (!live) {
    await p.route('**/api/advisories*', async r => { const res = await r.fetch(); const j = await res.json();
      j.items = [...j.items.filter(x => !['fortinet', 'cisco', 'paloalto', 'ivanti'].includes(x.source)), ...EDGE];
      j.groups = { fortinet: 'edge', cisco: 'edge', paloalto: 'edge' };
      j.sources = [...j.sources.filter(x => !j.groups[x.id]), ...['fortinet', 'cisco', 'paloalto'].map(id => ({ id, name: { fortinet: 'Fortinet PSIRT', cisco: 'Cisco PSIRT', paloalto: 'Palo Alto Networks' }[id], url: 'https://example.org', fetched_at: iso(0) }))];
      return r.fulfill({ response: res, json: j }); });
    await p.route('**/api/exploits', r => r.fulfill({ json: EXPL }));
    await p.route('**/api/threats', async r => { const res = await r.fetch(); const j = await res.json(); j.urlhaus = URLHAUS; j.threatfox = TFOX; return r.fulfill({ response: res, json: j }); });
    await p.route('**/api/nlthreat', r => r.fulfill({ json: nl }));
    await p.route('**/api/news?*', async r => { const res = await r.fetch(); const j = await res.json(); // headlines for the incident filter
      j.items = [...NEWS.map((x, i) => ({ id: 'inc-' + i, url: 'https://example.org/inc-' + i, published: iso(-(i + 1) * 36e5), summary: '', ...x })), ...(j.items || [])];
      return r.fulfill({ response: res, json: j }); });
  }
  await p.goto(BASEURL); await p.waitForSelector('#stream .item'); await p.waitForTimeout(2500);
  return [ctx, p, errs];
}
const txt = (p, sel) => p.evaluate(s => document.querySelector(s)?.innerText.replace(/\s+/g, ' ').trim() || '', sel);
const chip = (p, sel, label) => p.click(`${sel} .sectabs .chip:has-text("${label}")`);

for (const [w, scheme] of [[1440, 'light'], [1440, 'dark'], [360, 'light']]) {
  const tag = `${w} ${scheme}`, mobile = w === 360;
  const [ctx, p, errs] = await open(w, { scheme, mobile });
  if (mobile) { await p.click('#mv-panels2'); await p.waitForTimeout(300); }
  // Security-adviezen
  const tabs = await p.$$eval('#panel-advisories .sectabs .chip', l => l.map(x => x.textContent + ':' + x.getAttribute('aria-pressed')));
  ok(tabs.join() === 'Adviezen:true,Edge-apparaten:false,Exploits:false', `${tag}: advisory tabs ${tabs}`);
  ok(!/TESTEDGE/.test(await txt(p, '#panel-advisories')), `${tag}: vendor advisories are not in the Adviezen tab`);
  await chip(p, '#panel-advisories', 'Edge-apparaten'); await p.waitForTimeout(200);
  const edge = await p.$$eval('#panel-advisories .adv', l => l.map(x => x.querySelector('.top').innerText.replace(/\s+/g, ' ') + ' | ' + x.querySelector('.ttl')?.textContent));
  ok(edge.length === 3 && /^Kritiek Cisco PSIRT/.test(edge[0]) && /^Hoog Palo Alto/.test(edge[1]) && /^Middel Fortinet/.test(edge[2]), `${tag}: edge tab, critical first: ${edge.map(x => x.slice(0, 26)).join(' · ')}`);
  ok(/Cisco PSIRT/.test(await txt(p, '#panel-advisories .pfoot')) && !/NCSC/.test(await txt(p, '#panel-advisories .pfoot')), `${tag}: footer lists the vendor sources only`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-advisories').screenshot({ path: `${OUT}/adv-edge.png` });
  await axe(p, '#panel-advisories', `${tag} edge tab`);
  await chip(p, '#panel-advisories', 'Exploits'); await p.waitForTimeout(200);
  const ex = await txt(p, '#panel-advisories');
  ok(/EPSS-STIJGERS \(7 DAGEN\)/i.test(ex) && /CVE-2026-81578 4,5% → 85,2%/.test(ex) && /CVE-2026-42271 12,8% → 92,6% actief misbruikt/.test(ex), `${tag}: EPSS risers with KEV mark: ${ex.slice(0, 160)}`);
  ok(/NIEUWE PUBLIEKE EXPLOITS/i.test(ex) && /remote Teltonika_RutOS/.test(ex) && /FIRST EPSS/.test(ex) && /Exploit-DB/.test(ex), `${tag}: new exploits and sources`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-advisories').screenshot({ path: `${OUT}/adv-exploits.png` });
  await axe(p, '#panel-advisories', `${tag} exploits tab`);
  // Cyberdreigingen
  await p.locator('#panel-threats').scrollIntoViewIfNeeded();
  await p.click('#tt-urlhaus'); await p.waitForTimeout(200);
  const uh = await txt(p, '#tp-urlhaus');
  ok(/374 actieve malware-URL’s op servers in Nederland · 390 nieuw in 7 dagen/.test(uh) && /SWISSNET LLC/.test(uh) && /hxxp:\/\/176\.65\.148\.144\/bins\/Hilix\[\.\]ppc/.test(uh), `${tag}: Malware in NL: ${uh.slice(0, 110)}`);
  await p.click('#tt-threatfox'); await p.waitForTimeout(200);
  const tf = await txt(p, '#tp-threatfox');
  ok(/1\.599 nieuwe indicatoren in 24 uur/.test(tf) && /Mirai/.test(tf) && /petroazaran\[\.\]com · ClearFake/.test(tf) && /f2911fe9394e3d09…/.test(tf), `${tag}: IOC's: ${tf.slice(0, 120)}`);
  const links = await p.$$eval('#tp-urlhaus a, #tp-threatfox a', l => l.map(a => a.href));
  ok(links.every(h => /abuse\.ch|malpedia/.test(h)), `${tag}: no links to malware, only to abuse.ch and Malpedia (${links.length})`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-threats .tsec').last().screenshot({ path: `${OUT}/threats-ioc.png` }).catch(() => {});
  await axe(p, '#panel-threats', `${tag} threats`);
  // Dreigingsbeeld NL
  await p.locator('#panel-nlthreat').scrollIntoViewIfNeeded();
  const nl = await p.evaluate(() => ({ h2: document.querySelector('#panel-nlthreat h2').textContent, secs: [...document.querySelectorAll('#panel-nlthreat .wsec h3')].map(x => x.textContent.trim()),
    bars: document.querySelectorAll('#panel-nlthreat .ddtrend span').length, text: document.querySelector('#panel-nlthreat').innerText.replace(/\s+/g, ' ') }));
  ok(nl.h2 === 'Dreigingsbeeld NL' && nl.secs.join('|') === '📰 Incidenten bij Nederlandse organisaties (7 dagen)|💥 DDoS-aanvallen op Nederland (7 dagen)|🧭 Routing (BGP, 7 dagen)', `${tag}: panel and sections: ${nl.secs.join(' | ')}`);
  const inc = await p.$$eval('#panel-nlthreat .wsec:first-child li', l => l.map(x => x.querySelector('a')?.textContent));
  ok(NEWS.filter(x => x.keep).every(x => inc.includes(x.title)) && NEWS.filter(x => !x.keep).every(x => !inc.includes(x.title)), `${tag}: only incidents at Dutch organisations (${inc.filter(t => NEWS.some(x => x.title === t)).length} of ${NEWS.length} test headlines kept)`);
  ok(nl.bars === 7 && /Soort: SYN Flood 62% · UDP Flood 24%/.test(nl.text) && /Herkomst \(webaanvallen\): Verenigde Staten 21% · China 6%/.test(nl.text), `${tag}: DDoS trend, types and origins`);
  ok(/2 hijacks en 5 route leaks/.test(nl.text) && /AS60781 LeaseWeb Netherlands B\.V\. kondigde 2a13:8c85::\/32 aan van AS207449 Example UK Ltd/.test(nl.text) && /nog bezig/.test(nl.text) && /AS269524 EDGEUNO S\.A\.S lekte routes \(CO, NL, BR\)/.test(nl.text),
    `${tag}: BGP events: ${nl.text.slice(nl.text.indexOf('Routing'), nl.text.indexOf('Routing') + 200)}`);
  if (w === 1440 && scheme === 'light') await p.locator('#panel-nlthreat').screenshot({ path: `${OUT}/nlthreat.png` });
  if (w === 360) await p.locator('#panel-nlthreat').screenshot({ path: `${OUT}/nlthreat-360.png` });
  await axe(p, '#panel-nlthreat', `${tag} nlthreat`);
  ok(errs.length === 0 && await p.evaluate(() => document.documentElement.scrollWidth) <= w, `${tag}: no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}
{ // no Cloudflare token, abuse.ch key missing, empty EPSS week; the tab choice is remembered
  const [ctx, p, errs] = await open(1440, { nl: { enabled: true, country: 'NL', radar: { missing_key: true } } });
  await p.route('**/api/threats', async r => { const res = await r.fetch(); const j = await res.json(); j.threatfox = { missing_key: true }; j.urlhaus = URLHAUS; return r.fulfill({ response: res, json: j }); });
  const nl = await txt(p, '#panel-nlthreat');
  ok(/gratis Cloudflare-token nodig \(Radar: Read\) in CLOUDFLARE_RADAR_TOKEN/.test(nl) && await p.$eval('#panel-nlthreat a[href*="dash.cloudflare.com"]', a => !!a), 'no token: a note with a link to create one');
  await chip(p, '#panel-advisories', 'Edge-apparaten'); await p.reload(); await p.waitForSelector('#panel-advisories .sectabs'); await p.waitForTimeout(1500);
  ok(await p.getAttribute('#panel-advisories .sectabs .chip:has-text("Edge-apparaten")', 'aria-pressed') === 'true', 'the advisory tab is remembered after a reload');
  await p.click('#tt-threatfox'); await p.waitForTimeout(200);
  ok(/gratis abuse\.ch-sleutel nodig/.test(await txt(p, '#tp-threatfox')), "IOC's without a key: how to get one");
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{ // English
  const [ctx, p] = await open(1440, { lang: 'en' });
  const tabs = await p.$$eval('#panel-advisories .sectabs .chip', l => l.map(x => x.textContent));
  ok(tabs.join() === 'Advisories,Edge devices,Exploits', `English advisory tabs: ${tabs}`);
  ok(/Threat picture NL/.test(await txt(p, '#panel-nlthreat h2')) && /2 hijacks and 5 route leaks involving Dutch networks/.test(await txt(p, '#panel-nlthreat')), 'English panel');
  ok(/Malware in NL/.test(await txt(p, '#tt-urlhaus')) && /IOCs/.test(await txt(p, '#tt-threatfox')), 'English threat tabs');
  await ctx.close();
}
{ // live: the catalog has Hacker News; the server's own data for the new parts
  const [ctx, p, errs] = await open(1440, { live: true });
  const cat = await p.evaluate(() => fetch('api/catalog').then(r => r.json()));
  const hn = cat.sources.find(s => s.id === 'hacker-news'), tech = cat.presets.find(x => x.id === 'tech'), thn = cat.sources.find(s => s.id === 'thehackernews');
  ok(hn && hn.category === 'tech' && !hn.default_enabled && tech.sources.includes('hacker-news') && !cat.sources.some(s => s.id === 'hackernews') && cat.sources.filter(s => s.name === 'Hacker News').length === 1, 'Hacker News: one Tech source (hacker-news), off by default, in the Tech & security preset');
  { // a visitor who had chosen the removed duplicate keeps Hacker News
    const c2 = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
    await c2.addInitScript(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, sources: ['nos-algemeen', 'hackernews'] })));
    const p2 = await c2.newPage(); const urls = []; p2.on('request', r => { if (/\/api\/news\?/.test(r.url())) urls.push(decodeURIComponent(r.url())); });
    await p2.goto(BASEURL); await p2.waitForTimeout(2500);
    ok(urls.some(u => /sources=[^&]*\bhacker-news\b/.test(u) && !/\bhackernews\b/.test(u)), `old choice "hackernews" becomes "hacker-news" (${urls[0]?.match(/sources=[^&]*/)?.[0]})`);
    await c2.close();
  }
  ok(thn?.category === 'tech', `The Hacker News is a Tech, privacy & security source (category ${thn?.category})`);
  const ex = await p.evaluate(() => fetch('api/exploits').then(r => r.json()));
  ok(ex.enabled && (ex.exploitdb.items?.length > 0 || ex.exploitdb.error === undefined), `live exploits: ${ex.exploitdb.items?.length ?? 0} from Exploit-DB${ex.epss.date ? ', EPSS ' + ex.epss.date : ''}`);
  const adv = await p.evaluate(() => fetch('api/advisories?limit=30').then(r => r.json()));
  ok(Object.values(adv.groups || {}).filter(g => g === 'edge').length === 4, `live: four vendor feeds in the edge group (${Object.keys(adv.groups || {}).join(', ')})`);
  // 1.31.1: ad blockers hide elements by generic class/id rules (EasyList "##.advt" hid the tab row in Firefox
  // with uBlock Origin). Check every class and id on the rendered page against the current lists.
  const lists = await Promise.all(['https://easylist.to/easylist/easylist.txt', 'https://easylist-downloads.adblockplus.org/easylistdutch.txt']
    .map(u => fetch(u).then(r => r.ok ? r.text() : '').catch(() => '')));
  const rules = new Set(lists.join('\n').split('\n').filter(l => /^##[.#][A-Za-z0-9_-]+$/.test(l)).map(l => l.slice(2)));
  if (rules.size < 1000) ok(true, `ad-block lists not reachable (${rules.size} rules): check skipped`);
  else {
    await p.click('#panel-advisories .sectabs .chip:has-text("Exploits")').catch(() => {});
    await p.click('#tt-threatfox').catch(() => {});
    const names = await p.evaluate(() => { const s = new Set(); for (const e of document.querySelectorAll('*')) { for (const c of e.classList) s.add('.' + c); if (e.id) s.add('#' + e.id); } return [...s]; });
    const hidden = names.filter(n => rules.has(n));
    ok(hidden.length === 0, `no class or id on the page matches a generic ad-block rule (${names.length} names, ${rules.size} rules)${hidden.length ? ': ' + hidden.join(', ') : ''}`);
  }
  ok(errs.length === 0, 'no page errors ' + errs.join('|'));
  await ctx.close();
}
{ // 1.31.1: a page from another build refreshes itself (once), then offers a Reload button
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' }); // page.route cannot see requests a service worker makes
  await ctx.addInitScript(() => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })); } });
  const p = await ctx.newPage(); let loads = 0; p.on('load', () => loads++);
  await p.goto(BASEURL); await p.waitForSelector('#stream .item');
  const same = await p.evaluate(async () => { const id = document.querySelector('meta[name="ndb-page"]').content; const r = await fetch('api/today'); return { id, hdr: r.headers.get('X-NDB-Page'), vary: r.headers.get('Vary') }; });
  ok(/^[0-9a-f]{16}$/.test(same.id) && same.hdr === same.id && !(await p.$('.updbar')), `page and server share the build id ${same.id}: no reload, no bar`);
  ok(/X-NDB-Page/.test(same.vary || ''), `answers vary by build id, so caches keep builds apart (Vary: ${same.vary})`);
  await p.route('**/api/alerts*', async r => { const res = await r.fetch(); return r.fulfill({ response: res, headers: { ...res.headers(), 'x-ndb-page': 'aaaaaaaaaaaaaaaa' } }); });
  loads = 0; await p.reload(); await p.waitForSelector('.updbar', { timeout: 20000 }); await p.waitForTimeout(500);
  ok(loads === 2, `a different server build: the page reloads itself once (${loads} loads)`);
  ok(/Er is een nieuwe versie van Nieuws Hub\./.test(await p.textContent('.updbar')) && !!(await p.$('.updbar button')), 'still different after that reload: a bar with Vernieuwen');
  const r = await new AxeBuilder({ page: p }).include('.updbar').analyze();
  ok(r.violations.length === 0, `update bar axe: ${r.violations.map(v => v.id).join(', ') || 0}`);
  await p.unroute('**/api/alerts*');
  await Promise.all([p.waitForEvent('load'), p.click('.updbar button')]); await p.waitForSelector('#stream .item'); await p.waitForTimeout(800);
  ok(!(await p.$('.updbar')), 'Vernieuwen reloads; with matching builds the bar is gone');
  await ctx.close();
}
{ // settings transfer: export everything on one device, import on another (including the phone pages)
  const P = { v: 2, onboarded: true, theme: 'dark', lang: 'nl', sources: ['nos-algemeen', 'tweakers', 'security-nl'], known: [], pageSize: 100, summaries: false,
    density: 'comfortable', category: 'tech', threatTab: 'feodo', advTab: 'edge', advFilter: 'high', alarmCity: 'den-haag', alarmCityName: 'Den Haag',
    group: false, hideRead: true, images: true, trending: false, pushTopics: ['water', 'quakes'], waste: { postcode: '2511AB', number: 1, suffix: '', provider: '' },
    sports: ['f1'], sportsSeen: ['f1', 'road', 'mtb', 'athletics', 'football'], solar: { kwp: 4.5, tilt: 35, az: 0 }, saver: 'off', hazardTab: 'world',
    watch: ['fortinet'], mute: ['voetbal'], weather: { name: 'Utrecht', lat: 52.09, lon: 5.12, region: 'Utrecht', cc: 'NL' }, air: { name: 'Amsterdam', lat: 52.37, lon: 4.9 },
    mode: 'normal', panels: { order: null, collapsed: { sky: true }, hidden: { satellite: true }, page: { weather: 'b', markets: 'a', economy: 'a' } } };
  const c1 = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await c1.addInitScript(x => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); const o = JSON.parse(x); o.panels.order = undefined; localStorage.setItem('ndb:prefs', JSON.stringify(o)); } }, JSON.stringify(P));
  const p1 = await c1.newPage(); await p1.goto(BASEURL); await p1.waitForSelector('#stream .item', { state: 'attached' }); await p1.waitForTimeout(1500);
  await p1.click('#open-settings'); await p1.click('#prefs-copy').catch(() => {}); await p1.waitForTimeout(300);
  const exported = JSON.parse(await p1.$eval('#prefs-json', t => t.value));
  await c1.close();
  const keys = Object.keys(P).filter(k => !['sources', 'known', 'panels', 'v'].includes(k));
  const canon = v => Array.isArray(v) ? v.map(canon) : v && typeof v === 'object' ? Object.fromEntries(Object.keys(v).sort().map(k => [k, canon(v[k])])) : v;
  const same = (a, b) => JSON.stringify(canon(a)) === JSON.stringify(canon(b)); // key order does not matter
  const lostOnExport = keys.filter(k => !same(exported[k], P[k]));
  ok(lostOnExport.length === 0 && same(exported.panels.page, P.panels.page) && same(exported.panels.collapsed, P.panels.collapsed) && same(exported.panels.hidden, P.panels.hidden),
    `export contains every setting, including the phone pages (${keys.length + 3} checked${lostOnExport.length ? '; different: ' + lostOnExport.join(', ') : ''})`);
  const c2 = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await c2.addInitScript(() => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })); } });
  const p2 = await c2.newPage(); await p2.goto(BASEURL); await p2.waitForSelector('#stream .item', { state: 'attached' }); await p2.waitForTimeout(1000);
  await p2.click('#open-settings'); await p2.fill('#prefs-json', JSON.stringify(exported)); await p2.click('#prefs-import'); await p2.waitForTimeout(800);
  const imported = await p2.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')));
  const lostOnImport = keys.filter(k => !same(imported[k], exported[k]));
  ok(/Voorkeuren geïmporteerd/.test(await p2.textContent('#prefs-msg')) && lostOnImport.length === 0 && same(imported.panels, exported.panels) && P.sources.every(id => imported.sources.includes(id)),
    `import on another device restores every setting${lostOnImport.length ? ' (different: ' + lostOnImport.join(', ') + ')' : ''}`);
  await p2.keyboard.press('Escape'); await p2.setViewportSize({ width: 360, height: 900 }); await p2.waitForTimeout(400);
  await p2.click('#mv-panels2'); await p2.waitForTimeout(400);
  const onB = await p2.evaluate(() => [...document.querySelectorAll('#side .panel')].filter(x => getComputedStyle(x).display !== 'none').map(x => x.id.replace('panel-', '')));
  ok(onB.includes('weather') && !onB.includes('markets') && !onB.includes('economy'), `phone pages follow the imported choice: Achtergrond has ${onB.slice(0, 5).join(', ')}…`);
  await c2.close();
}
for (const w of [1440, 360]) { // first visit: panel groups (all on); unchecked groups become hidden panels
  const c = await b.newContext({ viewport: { width: w, height: 1000 }, serviceWorkers: 'block', isMobile: w < 700, hasTouch: w < 700 });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#welcome .pgrp');
  const g = await p.$$eval('#welcome .pgrp', l => l.map(x => ({ name: x.querySelector('strong').textContent, on: x.querySelector('input').checked })));
  const hgt = await p.evaluate(() => Math.round(document.querySelector('#welcome .welcome').getBoundingClientRect().height));
  console.log(`welcome height at ${w}px: ${hgt}`);
  ok(g.length === 9 && g.map(x => x.name).join('|') === 'Weer & natuur|Gezondheid|Thuis & vandaag|Verkeer & reizen|Alarmeringen|Storingen|Economie & politiek|Sport|Security & privacy'
    && g.filter(x => x.on).map(x => x.name).join('|') === 'Weer & natuur|Thuis & vandaag|Verkeer & reizen', `${w}px: nine panel groups, on by default: ${g.filter(x => x.on).map(x => x.name).join(', ')}`);
  if (w === 1440) {
    await p.locator('#welcome').screenshot({ path: `${OUT}/welcome-panels.png` });
    const r = await new AxeBuilder({ page: p }).include('#welcome').analyze();
    ok(r.violations.length === 0, `first visit axe: ${r.violations.map(v => v.id).join(', ') || 0}`);
    await p.click('#welcome .pgrp:has-text("Security & privacy") input'); // switch one more group on
    await p.click('#welcome .btn.primary'); await p.waitForTimeout(800);
    const st = await p.evaluate(() => ({ hidden: JSON.parse(localStorage.getItem('ndb:prefs')).panels.hidden, shown: [...document.querySelectorAll('#side .panel')].map(x => x.id.replace('panel-', '')) }));
    const on = ['weather', 'satellite', 'sky', 'quakes', 'today', 'waste', 'energy', 'fuel', 'traffic', 'trains', 'threats', 'nlthreat', 'advisories', 'breaches', 'ransomware', 'ap'];
    ok(st.shown.slice().sort().join() === on.slice().sort().join() && ['sports', 'economy', 'alarms', 'pollen', 'outages'].every(id => st.hidden[id]), `default groups plus Security & privacy shown (${st.shown.length} panels), the rest hidden`);
  }
  await c.close();
}
{ // Instellingen: Standaardplaats for air quality and hay fever
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block' });
  await c.addInitScript(() => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, air: { name: 'Groningen', lat: 53.22, lon: 6.57 } })); } });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#stream .item', { state: 'attached' }); await p.waitForTimeout(800);
  const def = (await (await fetch(BASEURL + 'api/catalog')).json()).weather_location;
  await p.click('#open-settings'); await p.click('#air-default'); await p.waitForTimeout(400);
  const air = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).air);
  ok(air && air.name === def.name && air.lat === Math.round(def.lat * 100) / 100 && /standaardplaats van deze server/.test(await p.textContent('#air-msg')), `Standaardplaats sets the air place to the server default (${air?.name})`);
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
