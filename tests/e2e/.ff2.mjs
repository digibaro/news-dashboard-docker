import { firefox } from 'playwright';
import { spawn } from 'node:child_process';
import { rmSync } from 'node:fs';
const URL = 'http://127.0.0.1:8095/';
let srv;
const start = async (bin, cfg) => { srv = spawn(bin, ['-config', cfg], { env: { ...process.env, NDB_LISTEN: '127.0.0.1:8095', NDB_SNAPSHOT_PATH: '', NDB_LOG_LEVEL: 'warn' }, stdio: 'ignore' });
  for (let i = 0; i < 60; i++) { try { if ((await fetch(URL)).ok) return; } catch {} await new Promise(r => setTimeout(r, 500)); } };
const stop = () => new Promise(r => { srv.on('exit', r); srv.kill(); });
const info = p => p.evaluate(async () => ({ meta: document.querySelector('meta[name="ndb-page"]')?.content || 'none', advt: !!document.querySelector('#panel-advisories .advt'),
  tabs: [...document.querySelectorAll('#panel-advisories .advt .chip')].map(x => x.textContent).join('|'), sw: navigator.serviceWorker?.controller?.scriptURL || 'none',
  ready: document.readyState, caches: await caches.keys().catch(() => 'n/a') })).catch(e => 'eval error: ' + e.message.slice(0, 80));
rmSync('/tmp/prof', { recursive: true, force: true });
const mode = process.argv[2];
await start(mode === 'fresh' ? '/upg/ndb131' : '/upg/ndb130', mode === 'fresh' ? '/upg/c131.yaml' : '/upg/c130.yaml');
const ctx = await firefox.launchPersistentContext('/tmp/prof', { viewport: { width: 1440, height: 1000 } });
const p = ctx.pages()[0] || await ctx.newPage();
let navs = 0; p.on('framenavigated', f => { if (f === p.mainFrame()) navs++; });
p.on('console', m => { if (m.type() === 'error' || m.type() === 'warning') console.log('  console', m.type(), m.text().slice(0, 140)); });
p.on('pageerror', e => console.log('  pageerror', e.message.slice(0, 140)));
await p.goto(URL); await p.evaluate(() => localStorage.setItem('ndb:prefs', JSON.stringify({ v: 2, onboarded: true })));
await p.reload(); await p.waitForTimeout(8000);
console.log(mode, 'first:', JSON.stringify(await info(p)), 'navs', navs);
if (mode !== 'fresh') { await stop(); await start('/upg/ndb131', '/upg/c131.yaml'); }
for (let i = 1; i <= 3; i++) {
  navs = 0;
  await p.reload({ waitUntil: 'commit', timeout: 15000 }).catch(e => console.log('  reload error', e.message.slice(0, 80)));
  await p.waitForTimeout(9000);
  console.log(mode, 'reload', i, JSON.stringify(await info(p)), 'navs', navs);
}
await ctx.close(); await stop();
