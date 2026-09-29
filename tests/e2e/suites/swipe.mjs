import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e'; // screenshots
const URL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
async function open(w, prefs = {}, url = URL) {
  const ctx = await b.newContext({ viewport: { width: w, height: 844 }, hasTouch: true, isMobile: w < 700, deviceScaleFactor: 1 });
  await ctx.addInitScript(v => localStorage.setItem('ndb:prefs', v), JSON.stringify({ v: 2, onboarded: true, ...prefs }));
  const p = await ctx.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(url); await p.waitForSelector('#stream .item', { state: 'attached' }); await p.waitForSelector('#panel-weather .pbody > .wxnow', { state: 'attached' });
  await p.waitForTimeout(500);
  return [ctx, p, errs];
}
// a swipe: touchstart at (x0,y0) on the element under that point, touchend at (x1,y1)
const swipe = (p, x0, y0, x1, y1, sel) => p.evaluate(([x0, y0, x1, y1, sel]) => {
  const target = sel ? document.querySelector(sel) : document.elementFromPoint(x0, y0);
  const t = (x, y) => new Touch({ identifier: 1, target, clientX: x, clientY: y });
  target.dispatchEvent(new TouchEvent('touchstart', { bubbles: true, touches: [t(x0, y0)], changedTouches: [t(x0, y0)] }));
  target.dispatchEvent(new TouchEvent('touchend', { bubbles: true, touches: [], changedTouches: [t(x1, y1)] }));
}, [x0, y0, x1, y1, sel]);
const state = p => p.evaluate(() => {
  const vis = s => { const e = document.querySelector(s); return !!e && getComputedStyle(e).display !== 'none'; };
  return { view: document.documentElement.dataset.mview, news: vis('#news'), side: vis('#side'), chips: vis('#chips'), tabs: vis('#mview'),
    alertbar: vis('#alertbar'), top: vis('#top'), sel: document.querySelector('#mview [aria-selected="true"]')?.id, y: Math.round(scrollY),
    sw: document.documentElement.scrollWidth };
});

{
  const [ctx, p, errs] = await open(390);
  let s = await state(p);
  ok(s.view === 'news' && s.news && !s.side && s.tabs && s.chips && s.alertbar && s.top && s.sel === 'mv-news', `phone opens on the news: ${JSON.stringify(s)}`);
  await p.screenshot({ path: `${OUT}/swipe-news.png` });
  await swipe(p, 300, 500, 120, 510);
  await p.waitForTimeout(250);
  s = await state(p);
  ok(s.view === 'panels' && !s.news && s.side && !s.chips && s.alertbar && s.top && s.sel === 'mv-panels', 'swipe left: panels (chips hidden, top bar and header stay)');
  const order = await p.evaluate(() => [...document.querySelectorAll('#side .panel')].slice(0, 3).map(x => x.id).join());
  ok(order.startsWith('panel-weather'), `panels from top to bottom: ${order}…`);
  await p.screenshot({ path: `${OUT}/swipe-panels.png` });
  // separate scroll positions
  await p.evaluate(() => scrollTo(0, 900)); await p.waitForTimeout(100);
  await swipe(p, 100, 500, 300, 505);
  await p.waitForTimeout(250);
  s = await state(p);
  ok(s.view === 'news' && s.y === 0, `swipe right: news again at its own position (${s.y})`);
  await p.evaluate(() => scrollTo(0, 1500)); await p.waitForTimeout(100);
  await swipe(p, 300, 500, 100, 500); await p.waitForTimeout(250);
  ok((await state(p)).y === 900, 'panels keep their scroll position (900)');
  await swipe(p, 100, 500, 300, 500); await p.waitForTimeout(250);
  ok((await state(p)).y === 1500, 'news keeps its scroll position (1500)');
  // sticky header in both views
  const hdr = await p.evaluate(() => Math.round(document.querySelector('#top').getBoundingClientRect().top));
  await swipe(p, 300, 500, 100, 500); await p.waitForTimeout(250);
  const hdr2 = await p.evaluate(() => Math.round(document.querySelector('#top').getBoundingClientRect().top));
  ok(hdr === 0 && hdr2 === 0, 'header stays at the top when scrolled, in both views');
  await p.evaluate(() => scrollTo(0, 0)); await p.waitForTimeout(100);
  // not a swipe: from the screen edge, mostly vertical, too short, inside a sideways scroller
  await swipe(p, 10, 500, 250, 500); await p.waitForTimeout(150);
  ok((await state(p)).view === 'panels', 'a swipe from the screen edge is left to the browser');
  await swipe(p, 200, 300, 280, 500); await p.waitForTimeout(150);
  ok((await state(p)).view === 'panels', 'a mostly vertical movement does not switch');
  await swipe(p, 200, 400, 240, 400); await p.waitForTimeout(150);
  ok((await state(p)).view === 'panels', 'a short movement does not switch');
  const strip = await p.evaluate(() => { const els = [...document.querySelectorAll('#side *')].filter(e => ['auto', 'scroll'].includes(getComputedStyle(e).overflowX) && e.scrollWidth > e.clientWidth + 2);
    const e = els[0]; if (!e) return null; e.scrollIntoView({ block: 'center' }); const r = e.getBoundingClientRect(); e.setAttribute('data-test-strip', ''); return { x: r.left + r.width / 2, y: r.top + r.height / 2, cls: e.className }; });
  if (strip) {
    await swipe(p, strip.x + 60, strip.y, strip.x - 100, strip.y, '[data-test-strip]'); await p.waitForTimeout(150);
    ok((await state(p)).view === 'panels', `a swipe inside a sideways scroller (${strip.cls}) scrolls it instead of switching`);
  } else ok(true, 'no sideways scroller on the page (skipped)');
  // tabs, keyboard
  await p.click('#mv-news'); await p.waitForTimeout(200);
  ok((await state(p)).view === 'news', 'tab "Nieuws" switches');
  await p.focus('#mv-news'); await p.keyboard.press('ArrowRight'); await p.waitForTimeout(200);
  ok((await state(p)).view === 'panels' && await p.evaluate(() => document.activeElement?.id === 'mv-panels'), 'arrow keys move between the tabs');
  const ax = await new AxeBuilder({ page: p }).include('#mview').include('#alertbar').analyze();
  ok(ax.violations.length === 0, `axe on tabs and top bar: ${ax.violations.map(v => v.id).join(',') || 0}`);
  // top bar link from the news view, search from the panels view
  await p.click('#mv-news'); await p.evaluate(() => scrollTo(0, 0));
  await p.click('#ab-p2k'); await p.waitForTimeout(600);
  s = await state(p);
  const inView = await p.evaluate(() => { const r = document.querySelector('#panel-alarms').getBoundingClientRect(); return r.top >= 0 && r.top < 400; });
  ok(s.view === 'panels' && inView, 'Alarmeringen in the top bar opens the panels view at that panel');
  await p.click('#search-open'); await p.fill('#q', 'de'); await p.waitForTimeout(400);
  ok((await state(p)).view === 'news', 'searching switches to the news');
  ok(errs.length === 0 && s.sw <= 390, `no page errors, no horizontal scroll ${errs.join('|')}`);
  await ctx.close();
}
// a link to a panel (push notification)
{
  const [ctx, p] = await open(390, {}, URL + '#panel-quakes');
  await p.waitForTimeout(800);
  const s = await state(p);
  ok(s.view === 'panels' && await p.evaluate(() => { const r = document.querySelector('#panel-quakes').getBoundingClientRect(); return r.top >= 0 && r.top < 400; }), '#panel-quakes opens the panels view at that panel');
  await ctx.close();
}
// tablet, desktop, overview mode, English
for (const w of [800, 1440]) {
  const [ctx, p] = await open(w);
  const s = await state(p);
  ok(s.news && s.side && !s.tabs, `${w}px: news and panels side by side or stacked as before, no tabs`);
  await swipe(p, 500, 500, 100, 500); await p.waitForTimeout(150);
  const s2 = await state(p);
  ok(s2.news && s2.side, `${w}px: a swipe changes nothing`);
  await ctx.close();
}
{
  const [ctx, p] = await open(390, { mode: 'digest' });
  const s = await state(p);
  ok(!s.tabs, 'overview mode on a phone: no tabs');
  await ctx.close();
  const [c2, p2] = await open(390, { lang: 'en' });
  ok(await p2.evaluate(() => document.querySelector('#mv-news').textContent + '|' + document.querySelector('#mv-panels').textContent) === 'News|Panels', 'English tabs');
  await c2.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED');
process.exit(fails ? 1 : 0);
