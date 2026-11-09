// 1.40: the Zeldzame waarnemingen panel (waarneming.nl's recent-rarities feeds; on the test servers
// the Go tests' bird feed moved to today and yesterday, a wolf as the only mammal, no reptiles).
import { chromium } from 'playwright';
import AxeBuilder from '@axe-core/playwright';
const OUT = process.env.E2E_OUT || '/tmp/ndb-e2e';
const BASEURL = process.env.BASE || 'http://127.0.0.1:8090/';
const b = await chromium.launch();
let fails = 0; const ok = (c, m) => { console.log((c ? 'PASS ' : 'FAIL ') + m); if (!c) fails++; };

{
  const cat = await (await fetch(BASEURL + 'api/catalog')).json();
  ok(cat.sightings === true && cat.refresh?.sightings === 3600, 'catalog: sightings on, refreshed every hour');
  let r;
  for (let i = 0; i < 30; i++) { r = await (await fetch(BASEURL + 'api/sightings')).json(); if (r.data) break; await new Promise(f => setTimeout(f, 1000)); }
  const it = r.data?.items || [];
  ok(it.length === 6 && it.every(x => x.url.startsWith('http://127.0.0.1:8099/')), `api: 6 species today and yesterday, links on the site (${it.length})`);
  ok(it.map(x => x.name).join() === 'Notenkraker,Woestijntapuit,Rotszwaluw,Wolf,Flamingo,Notenkraker', `api order: ${it.map(x => x.name)}`);
}
const ctxFor = async (prefs, opts = {}) => {
  const c = await b.newContext({ viewport: { width: 1440, height: 1000 }, serviceWorkers: 'block', ...opts });
  await c.addInitScript(p => { if (!sessionStorage.getItem('t-init')) { sessionStorage.setItem('t-init', '1'); localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true, ...p })); } }, prefs);
  return c;
};
const names = p => p.$$eval('#panel-sightings .plist li > span:first-child a', l => l.map(a => a.textContent.trim()));
{
  const c = await ctxFor({});
  const p = await c.newPage(); const errs = []; p.on('pageerror', e => errs.push(e.message));
  await p.goto(BASEURL); await p.waitForSelector('#panel-sightings .plist');
  ok((await p.textContent('#panel-sightings h2')).trim() === 'Zeldzame waarnemingen', 'panel Zeldzame waarnemingen');
  const order = await p.$$eval('.panel', l => l.map(x => x.id.replace('panel-', '')));
  ok(order.indexOf('sightings') === order.indexOf('sky') + 1, 'after Vanavond aan de hemel');
  ok((await names(p)).join() === 'Notenkraker,Woestijntapuit,Rotszwaluw,Wolf,Flamingo,Notenkraker', 'all groups, rare and very rare');
  const first = await p.$eval('#panel-sightings .plist li', li => ({ tag: li.querySelector('.tag')?.textContent, m: li.querySelector('.m').textContent, notes: li.querySelector('.notes')?.textContent,
    a: [li.querySelector('a').target, li.querySelector('a').rel] }));
  ok(first.tag === 'zeer zeldzaam' && /Nucifraga caryocatactes/.test(first.m) && /vandaag/.test(first.m) && /21 waarnemingen/.test(first.m) && /FR, GE/.test(first.m), `first: ${first.m}`);
  ok(/trek naar zuid/.test(first.notes) && first.a[0] === '_blank' && /noopener/.test(first.a[1]), 'notes; opens waarneming.nl in a new tab');
  ok(/gisteren/.test(await p.textContent('#panel-sightings .plist li:last-child .m')), 'yesterday\'s sighting says gisteren');
  const chips = () => p.$$eval('#panel-sightings .sightctl .chip', l => l.map(x => x.getAttribute('aria-pressed')).join());
  ok(await chips() === 'true,true,true', 'three groups, all on');
  await p.click('#panel-sightings .chip:has-text("Vogels")');
  ok((await names(p)).join() === 'Wolf', 'without birds: the wolf');
  await p.click('#panel-sightings .chip:has-text("Reptielen")');
  await p.click('#panel-sightings .chip:has-text("Zoogdieren")');
  ok(await chips() === 'false,true,false', 'the last group can not be switched off');
  await p.click('#panel-sightings .chip:has-text("Reptielen")');
  await p.click('#panel-sightings .chip:has-text("Zoogdieren")');
  ok(/Vandaag en gisteren geen zeldzame waarnemingen/.test(await p.textContent('#panel-sightings')), 'reptiles only: a note');
  await p.click('#panel-sightings .chip:has-text("Vogels")');
  await p.selectOption('#panel-sightings .sightctl select', 'very');
  ok((await names(p)).join() === 'Notenkraker,Woestijntapuit,Rotszwaluw,Notenkraker', `very rare birds: ${await names(p)}`);
  const pr = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')));
  ok(pr.sightGroups.join() === '1,3' && pr.sightRare === 'very', `remembered: ${pr.sightGroups} ${pr.sightRare}`);
  ok(/waarneming\.nl/.test(await p.textContent('#panel-sightings .pfoot')), 'source credit');
  const axe = await new AxeBuilder({ page: p }).include('#panel-sightings').analyze();
  ok(axe.violations.length === 0, `axe: ${axe.violations.map(v => v.id).join(', ') || 0}`);
  await p.locator('#panel-sightings').screenshot({ path: `${OUT}/sightings.png` });
  ok(errs.length === 0, `no page errors ${errs.join('|')}`);
  await c.close();
}
{ // collapsed summary; sanitised prefs
  const c = await ctxFor({ sightGroups: [9], sightRare: 'bogus', panels: { order: [], collapsed: { sightings: true }, hidden: {}, page: {} } });
  const p = await c.newPage(); await p.goto(BASEURL); await p.waitForTimeout(2500);
  const sum = await p.textContent('#p-sightings-sum');
  ok(/^: 6 soorten · 5 zeer zeldzaam · Notenkraker$/.test(sum), `collapsed summary: "${sum}"`);
  const pr = await p.evaluate(() => JSON.parse(localStorage.getItem('ndb:prefs')));
  ok(pr.sightGroups.join() === '1,2,3' && pr.sightRare === 'all', 'unknown groups and rarity fall back to the defaults');
  await c.close();
}
{ // English and phone
  const c = await ctxFor({ lang: 'en' }, { viewport: { width: 360, height: 800 }, isMobile: true, hasTouch: true });
  const p = await c.newPage(); await p.goto(BASEURL);
  await p.click('#mv-panels'); await p.waitForSelector('#panel-sightings .plist');
  const txt = await p.textContent('#panel-sightings');
  ok(/Rare sightings/.test(txt) && /Birds/.test(txt) && /Reptiles and amphibians/.test(txt) && /very rare/.test(txt) && /21 observations/.test(txt) && /yesterday/.test(txt), 'English texts');
  const w = await p.evaluate(() => [document.documentElement.scrollWidth, innerWidth]);
  ok(w[0] <= w[1], `phone: no horizontal scroll (${w})`);
  await p.locator('#panel-sightings').screenshot({ path: `${OUT}/sightings-phone-en.png` });
  await c.close();
}
await b.close();
console.log(fails ? `${fails} FAILED` : 'ALL PASSED');
process.exit(fails ? 1 : 0);
