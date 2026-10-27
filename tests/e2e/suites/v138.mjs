// 1.38: the ADSB panel (ADS-B Exchange's live map in a frame; on the test servers a local stand-in).
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const MAP = /^http:\/\/127\.0\.0\.1:8099\/adsbx\/\?lat=52\.15&lon=5\.30&zoom=6&/;
const TAIL = 'iconScale=1.25&scale=0.65&noIsolation&hideSidebar&hideButtons&kiosk';
const HEX = '4853F6,4853E2,485E49,486A31,486A30,484B58,48605B,4844C2,4857B3,48605C,48644B,48644A,486449,484A91,484A92,484A93,484A94,484A95,484A96,485B2F,484AC1,484AC0';

{
  const cat = await (await fetch(BASEURL + 'api/catalog')).json();
  ok(cat.adsb?.enabled === true && /\/adsbx\/$/.test(cat.adsb.url), `catalog: adsb on, map ${cat.adsb?.url}`);
  const csp = (await fetch(BASEURL)).headers.get('content-security-policy');
  ok(/; frame-src http:\/\/127\.0\.0\.1:8099$/.test(csp), 'CSP: the map origin is the only frame');
}
const ctxFor = async (prefs, opts = {}) => {
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await c.addInitScript(p => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, ...p })); } }, prefs);
  const framed = [];
  c.on('request', r => { if (/\/adsbx\//.test(r.url())) framed.push(r.url()); });
  return { c, framed };
};
const src = p => p.getAttribute('#panel-adsb iframe', 'src');
{
  const { c, framed } = await ctxFor({});
  const p = await c.newPage();
  const cspErr = []; p.on('console', m => { if (/Content Security Policy|Refused to frame/i.test(m.text())) cspErr.push(m.text()); });
  await p.goto(BASEURL); await p.waitForSelector('#panel-adsb iframe'); await p.waitForTimeout(800);
  ok((await p.textContent('#panel-adsb h2')).trim() === 'ADSB', 'panel ADSB');
  const order = await p.$$eval('.panel', l => l.map(x => x.id.replace('panel-', '')));
  ok(order.indexOf('adsb') === order.indexOf('trains') + 1, 'ADSB comes after Treinstoringen');
  const tabs = await p.$$eval('#panel-adsb .sectabs .chip', l => l.map(x => x.textContent + (x.getAttribute('aria-pressed') === 'true' ? '*' : '')));
  ok(tabs.join('|') === 'Hulpdiensten*|Militair|Burgerluchtvaart', `views: ${tabs.join(', ')}`);
  let s = await src(p);
  ok(MAP.test(s) && s.endsWith('icaoFilter=' + HEX + '&' + TAIL), `Hulpdiensten: filtered on the 22 emergency helicopters (${s})`);
  ok(framed.length >= 1 && cspErr.length === 0, `the frame loads, no CSP errors (${cspErr.join(' ') || 'none'})`);
  ok(/adblocker/.test(await p.textContent('#panel-adsb')) && /Kaart: ADS-B Exchange/.test(await p.textContent('#panel-adsb .pfoot')), 'ad-blocker note and credit');
  await p.evaluate(() => { document.querySelector('#panel-adsb iframe').dataset.mark = '1'; });
  const n0 = framed.length;
  await p.click('#panel-adsb .sectabs .chip:has-text("Hulpdiensten")'); await p.waitForTimeout(400);
  ok(await p.getAttribute('#panel-adsb iframe', 'data-mark') === '1' && framed.length === n0, 'choosing the same view keeps the frame (no reload)');
  await p.click('#panel-adsb .sectabs .chip:has-text("Militair")'); await p.waitForTimeout(300);
  s = await src(p);
  ok(MAP.test(s) && s.endsWith('filterDbFlag=military&' + TAIL) && /militaire markering/.test(await p.textContent('#panel-adsb')), `Militair: the military flag (${s})`);
  await p.click('#panel-adsb .sectabs .chip:has-text("Burgerluchtvaart")'); await p.waitForTimeout(300);
  s = await src(p);
  ok(MAP.test(s) && s.endsWith('zoom=6&' + TAIL) && /Al het luchtverkeer/.test(await p.textContent('#panel-adsb')), `Burgerluchtvaart: all traffic (${s})`);
  ok(await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')).adsbView) === 'civil', 'the view is remembered');
  ok(/Luchtverkeer: burgerluchtvaart/.test(await p.getAttribute('#panel-adsb iframe', 'title')), 'the frame has a title');
  await p.locator('#panel-adsb').screenshot({ path: `${OUT}/adsb.png` });
  const axe = await new AxeBuilder({ page: p }).include('#panel-adsb').exclude('#panel-adsb iframe').analyze();
  ok(axe.violations.length === 0, `adsb axe: ${axe.violations.map(v => v.id).join(', ') || 0}`);
  await p.click('#panel-adsb .ptoggle');
  ok(await p.locator('#panel-adsb iframe').count() === 0 && /Burgerluchtvaart/.test(await p.textContent('#p-adsb-sum')), `collapsed: no frame, summary "${await p.textContent('#p-adsb-sum')}"`);
  await p.click('#panel-adsb .ptoggle'); await p.waitForSelector('#panel-adsb iframe');
  ok(true, 'expanding brings the map back');
  await c.close();
}
{ // a collapsed panel loads no map
  const { c, framed } = await ctxFor({ panels: { order: [], collapsed: { adsb: true }, hidden: {}, page: {} } });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForTimeout(1500);
  ok(framed.length === 0 && await p.locator('#panel-adsb iframe').count() === 0 && /Hulpdiensten/.test(await p.textContent('#p-adsb-sum')), 'collapsed on load: no map, summary');
  await c.close();
}
{ // English and phone
  const { c } = await ctxFor({ lang: 'en', adsbView: 'military' }, { viewport: { width: 360, height: 800 }, isMobile: true, hasTouch: true });
  const p = await c.newPage(); await p.goto(BASEURL);
  await p.click('#mv-panels'); await p.waitForSelector('#panel-adsb iframe');
  const txt = await p.textContent('#panel-adsb');
  ok(/Emergency air traffic/.test(txt) && /Military air traffic/.test(txt) && /Civil air traffic/.test(txt) && /ad blocker/.test(txt) && /Map: ADS-B Exchange/.test(txt), 'English texts');
  const w = await p.evaluate(() => [document.documentElement.scrollWidth, innerWidth, document.querySelector('#panel-adsb iframe').getBoundingClientRect().width]);
  ok(w[0] <= w[1] && w[2] > 250, `phone: no horizontal scroll, map ${Math.round(w[2])} px wide`);
  await p.locator('#panel-adsb').screenshot({ path: `${OUT}/adsb-phone-en.png` });
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
