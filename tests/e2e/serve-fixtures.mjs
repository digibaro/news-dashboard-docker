// A tiny static file server for the test fixtures (mock MeteoAlarm feed, hostile RSS feed).
// Before serving, the dates in the fixtures are moved to "now", so they never age out:
//   mock/nl.xml   Limburg's warning expired an hour ago, all others active now
//   evil/evil.xml items published in the last half hour
//   /hardlopen/   page 1: the Go tests' hardlopen.nl list page, event i on day i+1 from today; later pages empty
// Usage: node serve-fixtures.mjs <port>
import { createServer } from 'node:http';
import { readFileSync } from 'node:fs';

const port = Number(process.argv[2] || 8099);
const at = h => new Date(Date.now() + h * 3600e3).toISOString().replace(/\.\d{3}Z$/, '+00:00');
function nlXml() {
  return readFileSync(new URL('./fixtures/mock/nl.xml', import.meta.url), 'utf8').replace(/<entry>[\s\S]*?<\/entry>/g, e => {
    const [on, off] = /Limburg/.test(e) ? [-5, -1] : [-2, 10];
    return e.replace(/(<cap:(?:onset|effective)>)[^<]*/g, `$1${at(on)}`).replace(/(<cap:expires>)[^<]*/g, `$1${at(off)}`)
      .replace(/(<(?:updated|published)>)[^<]*/g, `$1${at(-2)}`);
  });
}
function evilXml() {
  let i = 0;
  return readFileSync(new URL('./fixtures/evil/evil.xml', import.meta.url), 'utf8')
    .replace(/<pubDate>[^<]*<\/pubDate>/g, () => `<pubDate>${new Date(Date.now() - (10 + 20 - i++) * 60e3).toUTCString()}</pubDate>`);
}
const MON = ['jan', 'feb', 'mrt', 'apr', 'mei', 'jun', 'jul', 'aug', 'sep', 'okt', 'nov', 'dec'];
function runPage() {
  let i = 0, j = 0;
  const day = n => new Date(Date.now() + n * 864e5);
  return readFileSync(new URL('../../testdata/hardlopen.html', import.meta.url), 'utf8')
    .replaceAll('https://www.hardlopen.nl/evenementen/', `http://127.0.0.1:${port}/evenementen/`)
    .replace(/(__date__day">)\d+/g, (_, a) => a + day(++i).getDate())
    .replace(/(__date__month hide-sm-down">)[a-z]{3} \d{4}/g, (_, a) => { const d = day(++j); return `${a}${MON[d.getMonth()]} ${d.getFullYear()}`; });
}
const routes = { '/mock/nl.xml': nlXml, '/evil/evil.xml': evilXml };
createServer((req, res) => {
  if (req.url.startsWith('/adsbx/')) { // stand-in for the ADSB panel's map (ADS-B Exchange)
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }).end('<!doctype html><title>map</title><p>map</p>');
    return;
  }
  if (req.url.startsWith('/hardlopen/')) {
    const body = new URL(req.url, 'http://x').searchParams.get('page') === '1' ? runPage() : '<!doctype html><div class="events"></div>';
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Cache-Control': 'no-store' }).end(body);
    return;
  }
  const f = routes[req.url.split('?')[0]];
  if (!f) { res.writeHead(404).end(); return; }
  res.writeHead(200, { 'Content-Type': 'application/xml; charset=utf-8', 'Cache-Control': 'no-store' }).end(f());
}).listen(port, '127.0.0.1', () => console.log(`fixtures on http://127.0.0.1:${port}/`));
