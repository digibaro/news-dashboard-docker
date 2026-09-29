import { showBothViews } from './legacy-views.mjs';
import { chromium } from 'playwright';
const b = await chromium.launch();
showBothViews(b);
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };
const URL = process.env.BASE;
const since = Math.floor(Date.now() / 1000) - 31 * 86400;
for (const [src, panel] of [['ap', 'ap'], ['rivm', 'health']]) {
  const all = (await (await fetch(`${URL}api/news?sources=${src}&limit=60&group=0`)).json()).items;
  const old = all.filter(i => Date.now() - new Date(i.published) > 31 * 864e5);
  console.log(`${src}: ${all.length} items in the cache, ${old.length} older than 31 days`);
  const ctx = await b.newContext({ viewport: { width: 1440, height: 1000 } });
  await ctx.addInitScript(() => { try { localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })); } catch {} });
  const p = await ctx.newPage(); await p.goto(URL); await p.waitForSelector(`#panel-${panel} .pfoot`); await p.waitForTimeout(500);
  const shown = await p.$$eval(`#panel-${panel} .plist li`, lis => lis.map(li => ({ title: li.querySelector('a')?.textContent, dt: li.querySelector('time')?.getAttribute('datetime') })));
  const note = await p.textContent(`#panel-${panel} .pbody`);
  ok(shown.every(x => x.dt && Date.now() - new Date(x.dt) <= 31 * 864e5), `${panel}: ${shown.length} items shown, all within 31 days`);
  ok(!old.some(o => shown.some(s => s.title === o.title)), `${panel}: none of the ${old.length} older items shown`);
  ok(shown.length > 0 || /afgelopen 31 dagen/.test(note), `${panel}: items or the "31 dagen" note`);
  await ctx.close();
}
await b.close();
console.log(fails ? `\n${fails} FAILED` : '\nALL PASSED'); process.exit(fails ? 1 : 0);
