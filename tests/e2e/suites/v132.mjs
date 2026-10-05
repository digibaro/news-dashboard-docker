// 1.32: Alarmeringen-wachter — alerts (banner, sound, desktop notification, push) for
// emergency alerts in specific streets.
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const iso = ms => new Date(Date.now() + ms).toISOString();
const get = async path => { const r = await fetch(BASEURL + path); return { status: r.status, body: await r.json().catch(() => null) }; };

// Stubs: a recording AudioContext (headless audio is unreliable) and Notification.
const stubs = () => {
  window.__osc = []; window.__notes = [];
  window.AudioContext = class {
    constructor() { this.state = 'running'; this.currentTime = 0; this.destination = {}; }
    resume() { return Promise.resolve(); }
    createGain() { return { gain: { setValueAtTime() {}, exponentialRampToValueAtTime() {} }, connect(d) { return d; } }; }
    createOscillator() { const o = { type: '', frequency: { value: 0 }, connect(g) { return g; }, start() { window.__osc.push(o.frequency.value); }, stop() {} }; return o; }
  };
  window.Notification = class { constructor(title, opts) { window.__notes.push({ title, ...opts }); } static get permission() { return 'granted'; } static requestPermission() { return Promise.resolve('granted'); } };
};
const ctxFor = async (prefs, opts = {}) => {
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await c.addInitScript(stubs);
  await c.addInitScript(p => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, ...p })); } }, prefs);
  return c;
};

// ---- server: /api/alarmwatch against the live Zwaailicht feed of the default city
{
  const bad = await get('api/alarmwatch?r=' + encodeURIComponent('Den Haag|Spui'));
  ok(bad.status === 400, `a rule without a valid city slug is refused (${bad.status})`);
  const none = await get('api/alarmwatch?r=' + encodeURIComponent('nergensdorp-xyz|Dorpsstraat|0'));
  ok(none.status === 200 && none.body.hits.length === 0 && /niet gevonden|niet bereikbaar/.test(none.body.errors['nergensdorp-xyz'] || ''), `an unknown place is reported per city (${JSON.stringify(none.body?.errors)})`);
  const live = await get('api/alarms');
  const items = live.status === 200 ? (live.body.groups || []).filter(g => g.scope === 'city').flatMap(g => g.items) : [];
  const pick = items.map(i => ({ i, m: /(?:naar|op|bij|Melding)\s+(.+?)(?:\s+in\s+|,\s*)[^,]+$/.exec(i.title) })).find(x => x.m && Date.now() - Date.parse(x.i.time) < 6 * 36e5);
  if (!pick) ok(true, `no recent alert with a street in ${live.body?.city || 'the default city'} (or Zwaailicht.nl not reachable): live match skipped`);
  else {
    const street = pick.m[1];
    const w = await get('api/alarmwatch?r=' + encodeURIComponent(`${live.body.city}|${street.toUpperCase()}|0`));
    const hit = w.body?.hits?.find(x => x.title === pick.i.title);
    ok(hit && hit.street === street.toUpperCase() && hit.city === live.body.city && hit.url === pick.i.url && hit.id, `live: "${street}" (upper case) matches "${pick.i.title}"`);
  }
  const sw = await (await fetch(BASEURL + 'sw.js')).text();
  ok(/requireInteraction: d\.sticky === '1'/.test(sw) && /vibrate:/.test(sw), 'service worker keeps watch notifications on screen and vibrates');
  const push = await get('api/push');
  if (push.body?.enabled) ok(push.body.topics.includes('alarmwatch'), `push topic alarmwatch offered (${push.body.topics.join(', ')})`);
}

// ---- page: a fresh alert in a watched street gives a banner, a sound and a notification
const HITS = {
  fresh: { id: 'tag:zwaailicht.nl,2026:aw-1', title: "Ambulance met spoed naar Koningskade in 's-Gravenhage", url: 'https://zwaailicht.nl/den-haag/medisch/x/aw1', time: iso(-2 * 6e4), urgency: 'spoed', service: 'ambulance', city: 'den-haag', street: 'Koningskade' },
  old: { id: 'tag:zwaailicht.nl,2026:aw-0', title: "Brandgerucht bij Spui in 's-Gravenhage", url: 'https://zwaailicht.nl/den-haag/brand/x/aw0', time: iso(-2 * 36e5), urgency: 'geen spoed', service: 'brandweer', city: 'den-haag', street: 'Spui' },
};
for (const lng of ['nl', 'en']) {
  const c = await ctxFor({ lang: lng, alarmWatch: [{ city: 'den-haag', cityName: 'Den Haag', street: 'Koningskade' }, { city: 'den-haag', cityName: 'Den Haag', street: 'Spui', spoed: false }] });
  const asked = [];
  await c.route('**/api/alarmwatch?*', r => { asked.push(new URL(r.request().url()).searchParams.getAll('r')); r.fulfill({ json: { enabled: true, hits: [HITS.fresh, HITS.old], errors: {} } }); });
  const p = await c.newPage(); await p.goto(BASEURL);
  await p.waitForSelector('#aw-banner .awmsg', { timeout: 15000 });
  const st = await p.evaluate(() => ({ n: document.querySelectorAll('#aw-banner .awmsg').length, text: document.querySelector('#aw-banner').innerText, href: document.querySelector('#aw-banner a')?.href, osc: window.__osc, notes: window.__notes }));
  const head = lng === 'en' ? 'Emergency alert: Koningskade' : 'Alarmering: Koningskade';
  ok(st.n === 1 && st.text.includes(head) && st.text.includes(HITS.fresh.title) && st.href === HITS.fresh.url, `${lng}: one banner for the fresh alert, not for the 2-hour-old one ("${st.text.replace(/\s+/g, ' ').slice(0, 90)}")`);
  ok(st.osc.length === 6 && st.osc.join() === '880,660,880,660,880,660', `${lng}: two-tone sound (${st.osc.join(',')})`);
  ok(st.notes.length === 1 && st.notes[0].title === head && st.notes[0].tag === 'aw-' + HITS.fresh.id && st.notes[0].requireInteraction === true, `${lng}: desktop notification with the push tag (${JSON.stringify(st.notes[0] || {}).slice(0, 120)})`);
  ok(asked[0]?.join(';') === 'den-haag|Koningskade|0;den-haag|Spui|0', `${lng}: the page asks for both streets (${asked[0]?.join('; ')})`);
  if (lng === 'nl') {
    await p.locator('#aw-banner').screenshot({ path: `${OUT}/alarmwatch-banner.png` });
    const r = await new AxeBuilder({ page: p }).include('#aw-banner').analyze();
    ok(r.violations.length === 0, `banner axe: ${r.violations.map(v => v.id).join(', ') || 0}`);
    await p.click('#aw-banner .btn.primary');
    ok(!(await p.$('#aw-banner')), 'OK closes the banner');
    await p.reload(); await p.waitForTimeout(2500);
    ok(!(await p.$('#aw-banner')) && (await p.evaluate(() => window.__osc.length)) === 0, 'after a reload the same alert does not sound again');
    await p.click('#open-settings');
    const list = await p.$$eval('#aw-list li', l => l.map(x => x.innerText.replace(/\s+/g, ' ')));
    ok(list.length === 2 && /Koningskade in Den Haag/.test(list[0]) && /Laatste: Ambulance met spoed naar Koningskade/.test(list[0]) && /Laatste: Brandgerucht bij Spui/.test(list[1]), `Instellingen lists the streets with their latest alert (${list.join(' | ').slice(0, 160)})`);
  }
  await c.close();
}

// ---- Instellingen: add, validate, remove, sound switch, test button
{
  const c = await ctxFor({ alarmCity: 'den-haag', alarmCityName: 'Den Haag' });
  let hits = [];
  await c.route('**/api/alarmwatch?*', r => r.fulfill({ json: { enabled: true, hits, errors: {} } }));
  await c.route(/\/api\/alarms\?city=/, r => /nergensdorp/.test(r.request().url())
    ? r.fulfill({ status: 404, json: { error: 'plaats niet gevonden bij Zwaailicht.nl' } }) : r.fulfill({ json: { enabled: true, city: 'den-haag', groups: [] } }));
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#stream .item', { state: 'attached' });
  await p.click('#open-settings');
  ok(!(await p.isHidden('#set-aw')) && await p.getAttribute('#aw-city', 'placeholder') === 'Plaats, bijv. Den Haag' && await p.isHidden('#aw-list'), 'section shown, the place defaults to the alarm place, the list is empty');
  await p.click('#aw-add');
  ok(/Vul een straatnaam in/.test(await p.textContent('#aw-msg')), 'a street is required');
  await p.fill('#aw-street', 'Dorpsstraat'); await p.fill('#aw-city', 'Nergensdorp'); await p.click('#aw-add'); await p.waitForTimeout(400);
  ok(/kent geen plaats “Nergensdorp”/.test(await p.textContent('#aw-msg')), 'an unknown place is refused');
  // an alert that already happened in the street does not sound when you add the street
  hits = [{ ...HITS.fresh, id: 'pre-1', title: "Ambulance naar Spui in 's-Gravenhage", street: 'Spui' }];
  await p.fill('#aw-street', 'Spui 12a'); await p.fill('#aw-city', ''); await p.check('#aw-spoed'); await p.click('#aw-add'); await p.waitForTimeout(1200);
  const pr = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).alarmWatch);
  ok(pr.length === 1 && pr[0].street === 'Spui' && pr[0].city === 'den-haag' && pr[0].cityName === 'Den Haag' && pr[0].spoed === true, `"Spui 12a" without a place is stored as Spui in Den Haag, urgent only (${JSON.stringify(pr)})`);
  ok(!(await p.$('#aw-banner')) && /Spui in Den Haag wordt nu gevolgd/.test(await p.textContent('#aw-msg')), 'adding a street does not sound for alerts from before');
  ok(/Spui in Den Haag · alleen spoed/.test((await p.innerText('#aw-list li')).replace(/\s+/g, ' ')), 'the list shows the rule');
  await p.fill('#aw-street', 'spui'); await p.click('#aw-add');
  ok(/volg je al/.test(await p.textContent('#aw-msg')), 'the same street twice is refused');
  await p.uncheck('#aw-sound');
  ok((await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).alarmSound)) === false, 'sound switch is saved');
  await p.click('#aw-test'); await p.waitForTimeout(200);
  let t = await p.evaluate(() => ({ text: document.querySelector('#aw-banner')?.innerText || '', osc: window.__osc.length, n: window.__notes.length }));
  ok(/Alarmering: Spui/.test(t.text) && /Test: zo ziet een alarmering in Spui eruit/.test(t.text) && t.osc === 0 && t.n === 1 && /Geluid staat uit/.test(await p.textContent('#aw-msg')), 'test with sound off: banner and notification, no sound');
  await p.check('#aw-sound'); await p.click('#aw-test'); await p.waitForTimeout(200);
  t = await p.evaluate(() => ({ osc: window.__osc.length, n: document.querySelectorAll('#aw-banner .awmsg').length }));
  ok(t.osc === 6 && t.n === 2, `test with sound on: sound and a second banner (${t.osc} tones, ${t.n} banners)`);
  const r = await new AxeBuilder({ page: p }).include('#set-aw').analyze();
  ok(r.violations.length === 0, `settings axe: ${r.violations.map(v => v.id).join(', ') || 0}`);
  await p.click('#aw-list li .btn');
  ok((await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).alarmWatch.length)) === 0 && await p.isHidden('#aw-list') && /wordt niet meer gevolgd/.test(await p.textContent('#aw-msg')), '× removes the street');
  await c.close();
}

// ---- phone: the banner fits the screen
{
  const c = await ctxFor({ alarmWatch: [{ city: 'den-haag', cityName: 'Den Haag', street: 'Koningskade' }] }, { viewport: { width: 360, height: 780 }, isMobile: true, hasTouch: true });
  await c.route('**/api/alarmwatch?*', r => r.fulfill({ json: { enabled: true, hits: [HITS.fresh], errors: {} } }));
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#aw-banner .awmsg', { timeout: 15000 });
  const bx = await p.evaluate(() => { const r = document.querySelector('#aw-banner').getBoundingClientRect(); return { l: r.left, r: r.right, sw: document.documentElement.scrollWidth }; });
  ok(bx.l >= 15 && bx.r <= 345 && bx.sw <= 360, `360px: banner inside the 16px gutters (${Math.round(bx.l)}–${Math.round(bx.r)}, page ${bx.sw})`);
  await p.screenshot({ path: `${OUT}/alarmwatch-phone.png` });
  await c.close();
}

// ---- no rules: the page never asks
{
  const c = await ctxFor({});
  let n = 0; await c.route('**/api/alarmwatch?*', r => { n++; r.fulfill({ json: { enabled: true, hits: [], errors: {} } }); });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForSelector('#stream .item', { state: 'attached' }); await p.waitForTimeout(1500);
  ok(n === 0, `without streets the page does not call /api/alarmwatch (${n})`);
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
