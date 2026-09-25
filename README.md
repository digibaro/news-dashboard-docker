# Nieuwsdashboard

A fast, privacy-friendly **single-page news dashboard in Dutch, with an English interface**. It combines:

- **News** from about 80 selectable RSS/Atom feeds: NL, regional, tech & security, data breaches, finance, sport, international and Belgium.
- **Weather** for a configurable location: Open-Meteo forecast, Buienradar rain for the next 2 hours, and KNMI/KMI warnings via MeteoAlarm.
- **Live cyber threats**:
  - SANS ISC/DShield top ports, 30-day attacker trend and top source IPs
  - abuse.ch Feodo botnet C2 servers
  - geolocation via ip-api.com
  - the ISC Infocon level
  - Autoriteit Persoonsgegevens enforcement news
- **Security advisories**: NCSC-NL, with the `[kans/schade]` rating parsed into badges, plus optional CERT-EU, CISA, BSI and MSRC.
- **Top bar:** the current KNMI weather code, the number of P2000 alerts in the last hour per service for a configured area (default Den Haag), and the NCTV terrorism threat level.
- **Luchtkwaliteit:** the air quality index (1–11) and NO₂, PM2.5, PM10 and O₃ from the nearest Luchtmeetnet station. The place is chosen per visitor (default: their weather location).
- **Treinstoringen:** current rail disruptions and engineering works from the NS Disruptions API (needs a free key).
- **Energieprijzen:** today's and tomorrow's hourly electricity prices and the gas price (EnergyZero), with a chart and the cheapest 3 hours.
- **Politiek vandaag:** today's debates and committee meetings of the Tweede Kamer (or the next sitting day) and the latest votes.
- **Verkeer:** jams, accidents and road closures from NDW open data (Rijkswaterstaat), with readable road names.
- **Alarmeringen:** the latest P2000 alerts for your city from Zwaailicht.nl, grouped as Brandweer, Ambulance, Politie and Lifeliner (at most 2 each). The city is chosen per visitor under Instellingen.
- **Datalekken:** the latest 3 Dutch and 3 other data breaches at organisations, from Have I Been Pwned: number of accounts, leak date, and what data leaked.
- **Storingen:** status of Microsoft Azure, Microsoft 365, AWS and Cloudflare. Any service with an Atlassian Statuspage or RSS status feed can be added in `config.yaml`.
- **Gezondheid:** RIVM news filtered to health alerts (infectious diseases, vaccination, heat, smog).
- **Themes**: Licht / Donker (true black) / Auto.
- **Language**: Nederlands / English / Auto (browser language), switchable at the top and under Instellingen → Weergave. Only the interface is translated; news, advisories and alerts stay in their original language.

**Reading features:**
- **First visit:** pick topics (presets) instead of 80 switches.
- **Story grouping:** the same story from several outlets becomes one item, with "Ook bij: …" links.
- **Read state:** read/unread plus a "Bewaard" list.
- **Watchlist and mute words:** security advisories that mention your products are pinned to the top.
- **Freshness:** every panel shows how old its data is.
- **Thumbnails:** optional, via the built-in image proxy.
- **Installable and offline-capable** (PWA).
- **Keyboard shortcuts:** press `?` in the app.
- **Overview and kiosk mode:** "Vandaag in het kort" puts the essentials of today on one screen; kiosk mode is for a wall display (see below).

It is built as **one Go binary with the frontend embedded, plus one `config.yaml`**:
- **No database.** All caching is in memory.
- **No disk writes** at runtime by default.
- **No third-party requests from the browser.**
- **Per-user preferences** (sources, theme, weather location, panel order) live in the browser's `localStorage`.

---

## Contents

1. [Quick start with Docker](#quick-start-with-docker)
2. [Configuration](#configuration)
3. [Adding or fixing a news source](#adding-or-fixing-a-news-source)
4. [Install on an ISPConfig VPS (systemd)](#install-on-an-ispconfig-vps-systemd)
5. [Docker on an ISPConfig VPS](#docker-on-an-ispconfig-vps)
6. [Updating](#updating)
7. [Data sources, terms and licences](#data-sources-terms-and-licences)
8. [Privacy and disk writes](#privacy-and-disk-writes)
9. [Security](#security)
10. [Monitoring](#monitoring)
11. [HTTP API](#http-api)
12. [Development](#development)
13. [Possible extensions](#possible-extensions)
14. [Changelog](#changelog)
15. [License](#license)

---

## Quick start with Docker

```sh
git clone https://github.com/digibaro/news-dashboard-docker.git nieuwsdashboard && cd nieuwsdashboard
cp config.yaml.default config.yaml                  # your own settings; not tracked by git
cp docker-compose.yml.default docker-compose.yml    # your own ports/limits; not tracked by git
# Keys and your contact details go in .env (not tracked by git); every line reaches the container:
cat > .env <<'ENV'
NDB_USER_AGENT=Nieuwsdashboard/1.0 (+https://nieuws.example.nl; beheer@example.nl)
ABUSECH_AUTH_KEY=
NS_API_KEY=
ENV
chmod 600 .env
docker compose up -d --build
```

- **`NDB_USER_AGENT`:** SANS ISC requires a User-Agent with your own site and e-mail.
- **`ABUSECH_AUTH_KEY`:** optional; a free key from <https://auth.abuse.ch/>.
- **`NS_API_KEY`:** for Treinstoringen; a free key from <https://apiportal.ns.nl>.

The compose file loads `.env` with `env_file`, so a new variable only needs a line in `.env`. This needs Docker Compose 2.24 or newer; check with `docker compose version`. After editing `.env`, run `docker compose up -d`: `restart` keeps the old environment.

**Local changes without editing the compose file:** put them in `docker-compose.override.yml`, which `docker compose` merges automatically and git ignores. Your `docker-compose.yml` can then stay an unchanged copy of the template. Example: another port and an external proxy network:

```yaml
services:
  nieuwsdashboard:
    ports: !override          # replace the template's ports; without !override both would be published
      - "8082:8080"
    networks:
      - default
      - proxy_frontend

networks:
  proxy_frontend:
    external: true
```

**Missing settings are logged at startup:** a missing NS key and the example User-Agent as warnings, a missing abuse.ch key as info. Check with `docker logs nieuwsdashboard 2>&1 | grep config:`.

Open <http://localhost:8080>. The first fetch round takes about 20–30 seconds.

What you get:

- **Image:** about 11 MB. `distroless/static`, no shell, runs as a non-root user.
- **Container hardening:** runs with `read_only: true`, `cap_drop: [ALL]`, `no-new-privileges` and a 128 MB memory limit, and needs no volumes. It typically uses 20–40 MB of RAM.
- **Health check:** `HEALTHCHECK` uses the binary's own `-healthcheck` flag, so no curl is needed in the image.
- **Logs:** go to stdout (`docker logs nieuwsdashboard`).

**Your own files:** `config.yaml` and `docker-compose.yml` are copies of the `.default` templates. They are in `.gitignore`, so `git pull` never overwrites them. After an update, compare them with the templates (`diff config.yaml.default config.yaml`, `diff docker-compose.yml.default docker-compose.yml`) to pick up new options. Keep only one compose file in the folder: when several exist (`compose.yaml`, `docker-compose.yml`, `docker-compose.yaml`), Compose warns and uses the first in that order.

**Config changes:** `config.yaml` is bind-mounted read-only. Many editors save by replacing the file, and a single-file bind mount keeps pointing at the old version. After editing, run `docker compose restart`.

---

## Configuration

Everything lives in `config.yaml`. The repository ships [`config.yaml.default`](config.yaml.default) as a template: copy it to `config.yaml` and edit the copy. `config.yaml` is in `.gitignore`, so your User-Agent, keys and local changes are never committed. The comments in the file explain each option. The file is re-read on `SIGHUP` and automatically when its modification time changes (checked every 60 s). An invalid file is rejected with a log line, and the previous configuration stays active. `server.listen`, `server.base_path` and `fetch.max_concurrent` need a restart.

| Section | What it controls |
|---|---|
| `server` | `listen` address, `base_path` (e.g. `/nieuws/` for a subfolder), `trusted_proxies` (whose `X-Forwarded-For` is believed), `log_level`, `metrics` (Prometheus endpoint, default off) |
| `fetch` | `user_agent` (**put your site and e-mail here**), default refresh `interval`, `timeout`, `max_concurrent` (max 2 per host is fixed) |
| `cache` | `max_items_per_source`, `max_age`, `snapshot_path` (empty = no disk writes, see below) |
| `features` | `show_images` (keep feed images), `proxy_images` (serve them through `/api/img`, see below), `geolocation` (ip-api lookups), `allow_custom_feeds` (reserved, see below) |
| `refresh` | how often an open browser tab asks the server for new data, per panel: `news`, `alerts`, `weather`, `air`, `traffic`, `trains`, `alarms`, `energy`, `politics`, `threats`, `advisories`, `breaches`, `outages`, `ap`, `health` (1m–24h, see below) |
| `keys` | `abusech_auth_key` (optional), `ns_api_key` (Treinstoringen) |
| `energy` | Energieprijzen: `enabled`, `url`, `interval` (min. 15m), `vat` (0.21), `electricity_extra` / `gas_extra` (€ added per kWh / m³, e.g. energy tax and markup; default 0) |
| `air` | Luchtkwaliteit: `enabled`, `base` (Luchtmeetnet API), `stations_url` (RIVM station list, CSV), `interval` (min. 15m) |
| `trains` | Treinstoringen: `enabled`, `url` (NS Disruptions API v3), `interval` (min. 2m). Needs `keys.ns_api_key` |
| `politics` | Politiek vandaag: `enabled`, `base` (Tweede Kamer OData), `interval` (min. 10m) |
| `weather` | default `location` (`name`, `lat`, `lon`, `region` = province for warnings, `country`), `interval`, MeteoAlarm feed URLs |
| `threats` | `enabled`, `interval` (min. 15m, ISC's request), `daily_interval`, `cisa_kev` |
| `alerts` | top bar: `nctv` (`enabled`, `url`, `interval`, min. 1h) and `knmi` (`true`/`false`) |
| `traffic` | `enabled`, `interval` (min. 2m), `url` (NDW DATEX II publication), `vild_base` (where the VILD location tables live) |
| `alarms` | `enabled`, `city` (default city slug, e.g. `den-haag`), `base` (feed URL prefix), `interval` (cache per city, min. 1m); `counts` for the top bar: `label`, `cities` (one or more slugs, e.g. a whole safety region), `interval` (1m–10m) |
| `breaches` | Datalekken panel: `enabled`, `url` (HIBP breach list), `interval` (min. 1h, default 3h), `include_sensitive` (default `false`) |
| `outages` | `enabled`, `interval` (min. 5m), `providers`: `id`, `name`, `url`, `homepage`, `format` (`statuspage` / `rss` / `m365`) |
| `advisories` | advisory feeds: `format: ncsc` (parses the NCSC title) or `rss` (any feed, severity from keywords) |
| `categories`, `sources` | news categories (`short` = chip label; `name_en`/`short_en` for the English interface) and feeds (`region` = province, for the "Mijn regio" preset) |
| `presets` | topics offered on the first visit and under Instellingen → Bronnen: a list of `sources`, or `region: true` for the broadcaster matching the visitor's weather province; `name_en`/`description_en` for the English interface |

Environment variables override the file, so Docker users rarely need to edit it:

| Variable | Overrides |
|---|---|
| `NDB_CONFIG` | path to `config.yaml` (default `./config.yaml`) |
| `NDB_LISTEN` | `server.listen` |
| `NDB_BASE_PATH` | `server.base_path` |
| `NDB_LOG_LEVEL` | `server.log_level` |
| `NDB_USER_AGENT` | `fetch.user_agent` |
| `NDB_SNAPSHOT_PATH` | `cache.snapshot_path` |
| `ABUSECH_AUTH_KEY` | `keys.abusech_auth_key` |
| `NS_API_KEY` | `keys.ns_api_key` |
| `NDB_TRUSTED_PROXIES` | `server.trusted_proxies` (comma-separated IPs/CIDRs) |
| `NDB_METRICS` | `server.metrics` (`true`/`false`) |

Unknown keys in `config.yaml` are an error, so typos don't pass silently.

### Refresh rates

There are two separate rates:

1. **Server → sources:** how often the server fetches upstream. This is set per source or panel. The server fetches once for all visitors, with jitter, conditional GETs and backoff on errors.
2. **Browser → server:** how often an open tab asks the server for its cached data. This is set under `refresh:`. These requests are cheap: they are served from memory, answer `304` when nothing changed, and are paused while the tab is hidden.

| Panel | Browser (`refresh:`) | Server fetches upstream |
|---|---|---|
| News | 5m | per source, `fetch.default_interval` 10m (some 15–30m) |
| Top bar (NCTV, KNMI, P2000 counts) | 3m | NCTV 6h, KNMI 10m, counts 3m |
| Weather | 15m | forecast 15m, rain 5m, warnings 10m |
| Luchtkwaliteit | 15m | index 30m, station list daily |
| Traffic | 5m | 5m |
| Treinstoringen | 3m | 5m |
| Energieprijzen | 30m | 1h |
| Politiek vandaag | 15m | 30m |
| Alarmeringen | 2m | 2m per city |
| Cyberdreigingen | 15m | 15m (ISC minimum), 30-day summary 1h |
| Security advisories | 30m | 15m |
| Datalekken | 30m | 3h (≈ 1 MB list) |
| Storingen | 10m | 10m |
| AP actions, Gezondheid | 30m | 30m |

A browser refresh faster than the server's interval gives nothing new, so keep `refresh:` at or above the matching server interval.

---

## Adding or fixing a news source

Add one line under `sources:`, then reload. No code changes are needed:

```yaml
  - { id: omroep-flevoland, name: "Omroep Flevoland", category: regio,
      url: "https://www.omroepflevoland.nl/rss", homepage: "https://www.omroepflevoland.nl", lang: nl }
```

**Fields:**
- `id`: lowercase letters, digits and `-`. It must be unique.
- `name`, `url`, `homepage`.
- `category`: must be one of the ids under `categories:`.
- **Optional:**
  - `default_enabled: true` (on for first-time visitors)
  - `interval: 30m`
  - `max_age: 720h`, for low-volume sources such as the AP
  - `type: rss|atom|rdf|json`, normally auto-detected
  - `lang: nl|en|…`: stories are only grouped within one language
  - `region: "Utrecht"`: province, for the "Mijn regio" preset
  - `enabled: false`: keeps the source in the file but neither fetches it nor shows it

**Before relying on a feed, verify it:**

```sh
./nieuwsdashboard -check-feeds                    # all news + advisory feeds
./nieuwsdashboard -check-feeds -only nos-politiek,tweakers
docker compose run --rm nieuwsdashboard -check-feeds   # the same, inside Docker
```

Then reload: `systemctl reload nieuwsdashboard` or `docker compose restart`, or wait up to 60 s for the automatic re-read. A new source appears in everyone's list under Instellingen → Bronnen. A new category also needs an entry under `categories:`.

The report shows status, item count and the newest date per feed. When a feed fails or is stale, the checker looks for a working alternative: `<link rel="alternate">` autodiscovery on the homepage, then common paths (`/rss`, `/feed`, `/rss.xml`, …). It prints what works. Sources that can't be fixed stay in `config.yaml` with `enabled: false` and a `# TODO` comment explaining why. HTML pages are never scraped as a substitute.

**Recommended sources** can be added the same way. A source with `default_enabled: true` is also switched on for existing visitors who customised their list, unless they have already seen that source before.

**Presets (first-visit topics).** Add or change them under `presets:`; every listed source must exist and be enabled. Give a regional broadcaster a `region:` (e.g. `"Utrecht"`) so it is offered to visitors whose weather location is in that province.

**Story grouping.** Articles from different outlets are grouped under the newest one when:
- they are in the same `lang` and published within 36 h of each other
- they share at least 25 % of their meaningful words (title + start of summary), or 60 % of the shorter title's words

Each article is compared with the group's lead only, so unrelated stories don't chain together. Visitors can switch grouping off under *Weergave*.

---

## Install on an ISPConfig VPS (systemd)

Tested on Debian 12 with systemd 252, using exactly the unit below. It works the same on Ubuntu and on any systemd distribution. No runtime needs to be installed.

The only system packages needed are `ca-certificates` (for HTTPS feeds) and `procps` (provides `/bin/kill` for `systemctl reload`). Both are present on every normal Debian/Ubuntu server, and ISPConfig itself needs them.

### 1. Build or copy the binary

On any machine with Go ≥ 1.27, or with Docker:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o nieuwsdashboard .
# without Go installed:
docker run --rm -v "$PWD":/src -w /src golang:1.27-alpine \
  sh -c 'CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o nieuwsdashboard .'
```

Copy it together with the config to the VPS:

```sh
sudo install -d -m 0755 /opt/nieuwsdashboard
sudo install -m 0755 nieuwsdashboard /opt/nieuwsdashboard/nieuwsdashboard
sudo install -m 0644 config.yaml.default /opt/nieuwsdashboard/config.yaml
sudoedit /opt/nieuwsdashboard/config.yaml    # set fetch.user_agent, weather.location, …
```

### 2. System user

```sh
sudo useradd --system --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin nieuwsdashboard
```

The service user only needs to *read* `/opt/nieuwsdashboard`, and the unit below makes that path read-only anyway.

### 3. systemd unit

Save as `/etc/systemd/system/nieuwsdashboard.service`:

```ini
[Unit]
Description=Nieuwsdashboard (news, weather and threat dashboard)
Documentation=file:///opt/nieuwsdashboard/README.md
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=nieuwsdashboard
Group=nieuwsdashboard
ExecStart=/opt/nieuwsdashboard/nieuwsdashboard -config /opt/nieuwsdashboard/config.yaml
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5s
Environment=NDB_LISTEN=127.0.0.1:8080
Environment=GOMEMLIMIT=96MiB
# Secrets such as ABUSECH_AUTH_KEY=... go in this optional file (chmod 600):
EnvironmentFile=-/etc/default/nieuwsdashboard

# Hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
PrivateDevices=true
ReadOnlyPaths=/opt/nieuwsdashboard
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectClock=true
ProtectHostname=true
ProtectProc=invisible
ProcSubset=pid
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=true
RestrictRealtime=true
RestrictSUIDSGID=true
LockPersonality=true
MemoryDenyWriteExecute=true
RemoveIPC=true
CapabilityBoundingSet=
AmbientCapabilities=
SystemCallArchitectures=native
SystemCallFilter=@system-service
SystemCallFilter=~@privileged
SystemCallErrorNumber=EPERM
UMask=0077
MemoryMax=256M
# Only needed when cache.snapshot_path is set, e.g. /var/lib/nieuwsdashboard/cache.json.gz:
#StateDirectory=nieuwsdashboard

[Install]
WantedBy=multi-user.target
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now nieuwsdashboard
systemctl status nieuwsdashboard
journalctl -u nieuwsdashboard -f          # logs (stdout)
curl -s http://127.0.0.1:8080/healthz     # {"status":"ok",...}
```

`systemd-analyze security nieuwsdashboard` rates this unit **1.4 OK**. The process runs without capabilities, with `NoNewPrivileges` and a seccomp filter.

### 4. Website in ISPConfig

1. **Sites → Website → Add new website**:
   - Pick the domain, e.g. `nieuws.example.nl`.
   - Enable **SSL** and **Let's Encrypt SSL**.
   - No PHP is needed: set PHP to *Disabled*.
   - Save.
2. Open the website again → tab **Options**.
3. Paste the directives for your web server (below) and save. ISPConfig rewrites the vhost and reloads the web server.

**nginx**: paste into **nginx Directives**:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
    proxy_http_version 1.1;
    proxy_read_timeout 30s;
}
```

**Apache**: paste into **Apache Directives**:

```apache
ProxyPreserveHost On
ProxyPass / http://127.0.0.1:8080/
ProxyPassReverse / http://127.0.0.1:8080/
RequestHeader set X-Forwarded-Proto "https" env=HTTPS
```

For Apache, the proxy modules must be enabled once, as root:

```sh
a2enmod proxy proxy_http headers && systemctl restart apache2
```

Notes:

- Keep `listen` on `127.0.0.1` so the dashboard is only reachable through the vhost.
- `server.trusted_proxies` already contains `127.0.0.1` and `::1`, so the rate limiters see the real client IP from `X-Forwarded-For`.
- **Subfolder instead of a (sub)domain:** set `server.base_path: "/nieuws/"` and use `location /nieuws/ { proxy_pass http://127.0.0.1:8080; … }` (nginx) or `ProxyPass /nieuws/ http://127.0.0.1:8080/nieuws/` (Apache).
- The app sends its own security headers (CSP, `nosniff`, `Referrer-Policy`, `Permissions-Policy`, `X-Frame-Options`), so you don't need to add them in the vhost. HSTS is best set by ISPConfig ("HSTS" option on the SSL tab).

---

## Docker on an ISPConfig VPS

Use the same proxy directives as above, but publish the container on localhost only. In your `docker-compose.yml` (copied from `docker-compose.yml.default`):

```yaml
    ports:
      - "127.0.0.1:8080:8080"
```

Then run `docker compose up -d --build`. Docker's own restart policy (`unless-stopped`) replaces the systemd unit.

**Client IPs.** Requests from nginx/Apache on the host reach the container from the Docker gateway (`172.16.0.0/12`). The compose file therefore sets `NDB_TRUSTED_PROXIES=127.0.0.1,::1,172.16.0.0/12`, so the rate limits apply per visitor and not to everyone at once. When port 8080 is **published directly to the internet** without a proxy in front, remove `172.16.0.0/12`. On setups where Docker's userland proxy forwards external traffic, visitors would otherwise appear to come from the gateway and could set their own `X-Forwarded-For`.

---

## Updating

| What | systemd | Docker |
|---|---|---|
| New version | replace `/opt/nieuwsdashboard/nieuwsdashboard`, then `sudo systemctl restart nieuwsdashboard` | `git pull && docker compose up -d --build` (your `config.yaml` and `docker-compose.yml` are left alone) |
| Edit sources/config | edit `config.yaml`, then `sudo systemctl reload nieuwsdashboard` (or wait ≤ 60 s) | edit `config.yaml`, then `docker compose restart` |

**One-time step when updating from 1.5.0 or older with Docker.** Up to 1.5.0 `docker-compose.yml` was part of the repository. Now it ships `docker-compose.yml.default`, and your own `docker-compose.yml` is not tracked. Before this first pull, git either refuses to update ("Your local changes … would be overwritten") or deletes the file. Keep your version like this:

```sh
cp docker-compose.yml /tmp/docker-compose.yml.mine   # safety copy of your settings
git checkout -- docker-compose.yml                   # drop local edits to the old tracked file
git pull                                             # removes the old tracked docker-compose.yml
cp /tmp/docker-compose.yml.mine docker-compose.yml   # put yours back: now untracked and ignored
```

Then, in `docker-compose.yml`, remove the `args:` / `VERSION: …` lines under `build:` and set `image: nieuwsdashboard:latest`: the version now comes from the `VERSION` file. Finish with `docker compose up -d --build`. `git status` should show nothing.

**Coming from 1.5.1** (which used `docker-compose.yaml`): your `docker-compose.yaml` keeps working and stays ignored. To follow the new name, run `mv docker-compose.yaml docker-compose.yml`.

A restart starts with an empty cache, which fills within about 30 seconds. If you want the news to be there immediately after a restart, see *snapshot* below.

---

## Data sources, terms and licences

The server fetches everything; browsers only talk to the dashboard itself.

| Source | Used for | Terms |
|---|---|---|
| Publishers' RSS/Atom feeds | news | Headlines and summaries remain the publishers' property; every item links to the original. Summaries are cut to ~300 characters, and images are off by default. |
| [Open-Meteo](https://open-meteo.com/) | forecast, geocoding | Free for **non-commercial** use (attribution). Commercial use needs an API subscription. |
| [Buienradar](https://www.buienradar.nl/) | rain next 2 h (NL/BE) | Free with attribution; not for commercial use without permission. |
| [MeteoAlarm](https://meteoalarm.org/) | KNMI/KMI warnings | CC BY 4.0 (see MeteoAlarm terms). |
| [SANS ISC / DShield](https://isc.sans.edu/) | ports, trend, top IPs, Infocon | CC BY-NC-SA, **non-commercial**. Requires a User-Agent with contact info; polled at most every 15 min (daily summary hourly). |
| [abuse.ch Feodo Tracker](https://feodotracker.abuse.ch/) | botnet C2 list | CC0. An `Auth-Key` is sent when configured. |
| [ip-api.com](https://ip-api.com/) | IP → country/AS | Free tier is **non-commercial only**, HTTP-only, 15 batch requests/min (honoured via `X-Rl`/`X-Ttl`). Results cached 24 h. Can be disabled: `features.geolocation: false`. |
| [NCSC-NL](https://advisories.ncsc.nl/) | advisories | Public RSS. |
| [Autoriteit Persoonsgegevens](https://www.autoriteitpersoonsgegevens.nl/) | AP actions panel | Public RSS. |
| CISA KEV (optional) | exploited vulnerabilities | US government work, public domain. |
| [NCTV](https://www.nctv.nl/onderwerpen/d/dtn) | terrorism threat level | Public page. There is no feed or structured field, so only the sentence "… niveau N op een schaal van 5" is read, every 6 h. If the wording changes the badge says *onbekend*; it never guesses. This is the one deliberate exception to "no HTML scraping". |
| KNMI via MeteoAlarm | top-bar weather code | KNMI's own RSS (`rss_KNMIwaarschuwingen.xml`) has not been updated since October 2023, so the code comes from the MeteoAlarm feed that carries KNMI's warnings. |
| [NDW](https://www.ndw.nu/) | traffic | Open data (Rijkswaterstaat, provinces, municipalities), polled every 5 min (≈ 260 KB). ANWB has no public API, and its site is not scraped. Road names come from NDW's VILD location table: only its ~400 KB table is read from the 42 MB zip with HTTP range requests, kept in memory and refreshed weekly or when NDW switches versions. |
| Azure, Microsoft 365, AWS, Cloudflare | outages | The providers' public status feeds. Microsoft 365 uses the JSON behind status.cloud.microsoft (consumer services, undocumented). The health of your own tenant would need Microsoft Graph with an app registration. |
| [RIVM](https://www.rivm.nl/) | health alerts | Public RSS. |
| [EnergyZero](https://www.energyzero.nl/) | Energieprijzen | The public price API behind EnergyZero's website (day-ahead EPEX prices). Not officially documented and no published terms (checked September 2026); fetched hourly, two small requests. |
| [Luchtmeetnet](https://www.luchtmeetnet.nl/) / [RIVM](https://data.rivm.nl/data/luchtmeetnet/) | Luchtkwaliteit | The index (every 30 min, ≈3 requests for all stations) and pollutants (on demand, cached 30 min) from the Luchtmeetnet API. Station locations come from RIVM's `luchtmeetnet_meetlocaties.csv`, one file checked daily, so no per-station API calls: the API answers bursts with HTTP 429. RIVM: "a free service from which no rights can be derived"; attribution shown. |
| [NS API portal](https://apiportal.ns.nl/) | Treinstoringen | Disruptions API v3. Free, but needs registration and a subscription key; the NS API terms apply. |
| [Tweede Kamer open data](https://opendata.tweedekamer.nl/) | Politiek vandaag | Official OData API, no key. No explicit licence found on the portal (checked September 2026), attribution shown. |
| [Have I Been Pwned](https://haveibeenpwned.com/) | Datalekken | The public breach list (`/api/v3/breaches`): no API key, and no visitor data is sent. Licensed **CC BY 4.0** (attribution shown in the panel). Fetched every 3 h. Left out: unverified, fabricated, retired, spam lists, malware and stealer logs, entries without a domain, and (unless `include_sensitive: true`) sensitive breaches. HIBP has no country field, so "Dutch" means a `.nl` domain or a description mentioning Dutch/the Netherlands. |
| [Zwaailicht.nl](https://zwaailicht.nl/blog/rss-feeds-p2000-meldingen) | P2000 alerts | Public Atom feeds per city (`/feed/meldingen/<city>.xml`), refreshed every minute. House numbers are left out by Zwaailicht. Fetched only for cities visitors actually choose, and cached 2 min per city. **Not for emergencies: call 112.** |

**Grid operators (Stedin, Enexis, Liander)** publish outages only as web pages or through internal app APIs, not as open data (checked September 2026). They are therefore not included; see the `# TODO` in `config.yaml`.

**Commercial use:** Open-Meteo, Buienradar, SANS ISC and ip-api all restrict it. Check their terms. For geolocation, the alternative is an offline GeoLite2/DB-IP Lite database, which adds a data file and a monthly update.

**Overview and kiosk mode.** Each visitor chooses a mode under Instellingen → Weergave → Modus; it is stored in their browser:

- **Normaal:** the news stream with the panels.
- **Overzicht** (key `v`): "Vandaag in het kort" puts weather, the most widely covered news of the last 24 h, traffic, trains, energy, air quality, security (high NCSC advisories, new breaches), politics and outages on one screen. A card title opens the full panel. Hidden panels are left out.
- **Kiosk:** for a wall display. Larger text, no settings or search. News and panels scroll by themselves every 15 s, pausing for a minute after any touch or key. The screen is kept awake where the browser allows it (Wake Lock). Leave with Esc or the small button in the corner.

A URL sets the mode for that visit only, without changing the saved choice: `https://nieuws.example.nl/?mode=kiosk` for a wall display, `?mode=digest` for the overview.

---

## Privacy and disk writes

- **Nothing is written to disk** at runtime by default. Caches are in memory, and logs go to stdout/stderr (journald or `docker logs`).
  - Checked with `strace` during a 5-minute run: no file was opened for writing.
  - Checked with `docker diff`: the running container's filesystem stays unchanged.
- **Optional warm-start snapshot:** set `cache.snapshot_path` (e.g. `/var/lib/nieuwsdashboard/cache.json.gz` and uncomment `StateDirectory=` in the unit). The server then writes one gzip JSON file at most every 30 minutes and on shutdown, and loads it at startup. This is the only code path that writes to disk.
- **No cookies, no trackers, no external fonts or scripts.**
  - The Content-Security-Policy only allows the page's own origin, plus `https:` images when `show_images` is on.
  - Feed titles and summaries are stripped of all HTML on the server and rendered as text in the browser.
  - Links are limited to `http(s)` and open with `rel="noopener noreferrer"`.
- **"Gebruik mijn locatie"** rounds coordinates to 2 decimals (~1 km) in the browser, and sends them only to this server.
- **Read state, "Bewaard", watchlist and mute words** live in `localStorage`. The service worker keeps the last good responses in the browser's cache for offline use; the server stores nothing per user.
- **Thumbnails** are off per visitor by default (*Instellingen → Weergave*). When on, they come from `/api/img`, so publishers never see the visitor. The proxy:
  - only fetches URLs this server signed itself (HMAC with a random key per process), so it is not an open proxy
  - checks every connection *after DNS resolution* and on each redirect, and refuses private, loopback, link-local (incl. `169.254.169.254`), CGNAT and other special-purpose ranges (IPv4 and IPv6)
  - accepts `https` only, at most 3 redirects, 5 MB, 40 megapixels
  - turns JPEG/PNG/GIF into 320 px JPEG thumbnails. WebP is passed through up to 250 KB, because the standard library cannot decode it.
  - keeps results in an in-memory LRU of at most 50 MB, rate-limited per client IP

---

## Security

- **Browser:**
  - The Content-Security-Policy allows only the dashboard's own origin. Its two inline scripts are allowed by **SHA-256 hash**, computed from the embedded page at startup, not by `'unsafe-inline'`.
  - It also sends `object-src 'none'`, `frame-ancestors 'none'` and `base-uri 'none'`, plus `nosniff`, `Referrer-Policy: no-referrer`, a restrictive `Permissions-Policy`, `X-Frame-Options: DENY` and `Cross-Origin-Opener/Resource-Policy`.
  - The page never uses `innerHTML`; all feed data is inserted as text.
- **Feed content:**
  - All HTML is stripped on the server, including double-escaped markup and Unicode bidi-override characters.
  - Links must be `http(s)` without credentials; titles and summaries are capped at 300 characters.
  - Responses are capped at 5 MB, 10 s and 3 redirects.
  - XML entities are never expanded, and absurdly nested XML is skipped.
- **Outbound requests from user input:** only the image proxy fetches addresses that originate outside the configuration. It accepts only URLs this server signed, and checks every connection's IP after DNS resolution (so also redirects and DNS rebinding) against private and special-purpose ranges. See *Privacy*.
- **Abuse limits:**
  - Weather, geocoding and image requests that cause upstream traffic are rate-limited per client IP.
  - Every cache has a fixed maximum (news per source, 500 weather locations, 1000 geocoding queries, 10 000 IP lookups, 50 MB of images).
  - The rate limiter itself is bounded and prunes at most once a minute.
- **Tested:** `main_test.go` feeds a hostile RSS fixture through the real fetcher and `/api/news`. It also checks:
  - security headers on every route
  - that the CSP hashes match the page
  - gzip/ETag behaviour
  - redirects from an allowed host to an internal address, which must be refused before connecting
  - the rate-limiter bounds

  `govulncheck` reports no known vulnerabilities. The Docker build only produces an image when `go vet` and all tests pass.

## Monitoring

- **`/healthz`** returns `200` with the status of every source (news, threat intelligence, advisories). Use it for Docker's `HEALTHCHECK`, Uptime Kuma or any HTTP check.
- **Bronstatus** (footer link in the dashboard) shows the same information for people: per source *ok · bijgewerkt 3 min geleden* or *niet bereikbaar sinds 10:42: HTTP 403*, with a filter for problems only.
- **`/metrics`** (off by default; `server.metrics: true` or `NDB_METRICS=true`) serves Prometheus text format:
  - `ndb_source_up`, `ndb_source_last_success_timestamp_seconds`, `ndb_source_items`, `ndb_source_consecutive_errors`
  - `ndb_http_requests_total{route,code}`
  - the image and geolocation cache sizes
  - `go_goroutines` and heap size

  Behind the ISPConfig vhost, restrict it (e.g. `location /metrics { allow 10.0.0.0/8; deny all; proxy_pass …; }`) if it should not be public.

## HTTP API

All JSON responses:
- are gzipped when the client accepts it
- carry a weak `ETag` and answer `304` to a matching `If-None-Match`
- use `Cache-Control: max-age=60`, except `/healthz`, which is `no-store`

| Endpoint | Returns |
|---|---|
| `GET /` | the dashboard |
| `GET /api/catalog` | categories, sources (without feed URLs), features, advisory sources |
| `GET /api/news?sources=a,b&limit=60&since=&group=` | merged, de-duplicated, date-sorted items with `related` (same story elsewhere) + per-source status. `since`: RFC 3339 or unix seconds; `group=0` disables story grouping |
| `GET /api/img?u=&s=` | thumbnail through the image proxy (only URLs signed by this server) |
| `GET /manifest.webmanifest`, `/icon-*.png`, `/sw.js` | installable web app: manifest, icons (drawn at startup) and offline service worker |
| `GET /api/weather?lat=&lon=&region=&cc=` | current, 24 h, 7 days, rain 2 h, warnings (defaults to the configured location) |
| `GET /api/geocode?q=` | place search, NL/BE first (rate-limited per IP) |
| `GET /api/threats` | Infocon, top ports, 30-day trend, top IPs + countries, Feodo C2, optional KEV, sources/licences |
| `GET /api/alerts` | top bar: NCTV level (`level`, `name`, `since`) and KNMI summary (`level`, `active`, `onset`, `types`, `areas`) |
| `GET /api/traffic` | jams (road, direction, from/to, delay), accidents, closure count, VILD version |
| `GET /api/alarms?city=` | P2000 alerts for a city slug (default from config): per service at most 2, with urgency, units and detail; Lifeliner falls back to national when the city has none |
| `GET /api/energy` | Energieprijzen: hourly `electricity` (today, tomorrow from ~13:00) and `gas` prices in € incl. VAT (+ configured extras) |
| `GET /api/air?lat=&lon=` | Luchtkwaliteit: nearest station (`name`, `distance_km`, `url`), `lki` (`value` 1–11, `at`) and `components` (NO2, PM25, PM10, O3 in µg/m³) |
| `GET /api/trains` | Treinstoringen: `key` (false without an NS key), `calamities`, `disruptions`, `maintenance` (active now, max 5) and `maintenance_total` |
| `GET /api/politics` | Politiek vandaag: `day`, `activities` (time, kind, subject, committee, cancelled, url) and the latest `votes` (result, kind, subject, date, url) |
| `GET /api/breaches` | Datalekken: the latest 3 Dutch (`nl`) and 3 other (`other`) breaches with `title`, `domain`, `url`, `breach_date`, `added`, `count`, `data_classes`, plus `total`/`shown` |
| `GET /api/outages` | per provider: status (`ok`/`minor`/`major`) and incidents |
| `GET /api/advisories?sources=&limit=` | normalised advisories: `{id, source, title, url, published, updated, severity, probability, impact, cves, products, exploited}` |
| `GET /healthz` | `{"status":"ok", …}` + per-source status for news (`sources`) and threat/advisory feeds (`feeds`) |
| `GET /metrics` | Prometheus metrics (only when enabled) |

---

## Development

```sh
go vet ./... && go test ./...        # unit tests: parsing, dedup, story grouping, NCSC, weather, ip-api batching,
                                     # image proxy + SSRF guard, PWA assets, config, …
cp config.yaml.default config.yaml   # once
go run . -config config.yaml         # http://127.0.0.1:8080
go run . -check-feeds                # verify all feeds
```

**Repository layout:**
- `main.go`: config, HTTP server, API
- `feeds.go`: fetcher, scheduler, feed parser, news cache, `-check-feeds`
- `panels.go`: weather, threats, advisories
- `web/index.html`: the entire frontend, embedded with `go:embed`. It uses no framework, no build step and no CDN.

**Dependency:** `gopkg.in/yaml.v3` is the only one.

---

## Possible extensions

These fit the architecture as extra scheduled jobs, but need a key, an account or a custom parser. So they are not in `config.yaml`:

- abuse.ch **URLhaus** and **ThreatFox** (free Auth-Key; the same key as Feodo)
- **Cloudflare Radar** attack trends per country (free API token)
- **GreyNoise** community / **AbuseIPDB** reputation for the top-IP list (free key)
- **Shadowserver** NL exposure statistics (account)
- **ransomware.live** victims filtered to NL/BE, **Spamhaus DROP** netblock counts, the **Tor exit list** (keyless; check the terms)
- **ENISA EUVD**, **NVD CVE API 2.0**, **FIRST EPSS** to enrich advisories with exploit probability
- **Custom feeds from the UI** (`features.allow_custom_feeds`): the flag is reserved but not implemented yet. The SSRF-safe dialer of the image proxy (see *Privacy*) is the building block for it.

Feeds that were tried and are currently broken are listed in `config.yaml` with `enabled: false` and a `# TODO` explaining why.

---

## Changelog

### 1.6.2
- **`env_file: .env` in `docker-compose.yml.default`:** every variable in `.env` reaches the container, so new keys no longer need an extra line in your own compose file. Add the `env_file:` block to your existing `docker-compose.yml` once (see the template). It needs Docker Compose 2.24 or newer.
- **Startup warnings:** a missing NS key and the example User-Agent are logged as warnings at startup and after a config reload; a missing abuse.ch key is logged as info.
- **Quick start:** keys and contact details go in `.env` instead of shell exports.

### 1.6.1
- **Treinstoringen checked against live NS data:** the NS API format matches the parser. Display fixes:
  - no trailing period in titles
  - the cause is no longer repeated when NS's situation text already contains it
  - engineering works show NS's period ("… t/m zondag 4 oktober 23:58 uur")
  - the heading says "Werkzaamheden (n)" instead of a misleading "planned in total"

### 1.6.0
- **New panels:**
  - **Luchtkwaliteit:** the nearest Luchtmeetnet station's index and pollutants, for a place chosen per visitor (default: the weather location).
  - **Treinstoringen:** NS disruptions and engineering works (NS Disruptions API; free key via `keys.ns_api_key` / `NS_API_KEY`).
  - **Energieprijzen:** hourly electricity prices for today and tomorrow, the gas price, a chart and the cheapest 3 hours (EnergyZero). Optional extras in config for an all-in price.
  - **Politiek vandaag:** Tweede Kamer meetings of today or the next sitting day, and the latest votes.
- **Overview and kiosk mode**, per visitor or via `?mode=digest` / `?mode=kiosk`.
- **Top bar order:** KNMI, Alarmeringen, then the NCTV threat level.
- New config sections `energy`, `air`, `trains`, `politics`, `keys.ns_api_key` and their `refresh` keys.

### 1.5.2
- **Compose file name:** the template is now `docker-compose.yml.default`; copy it to `docker-compose.yml`, the familiar name. It is still ignored by git. A `docker-compose.yaml` from 1.5.1 keeps working (see *Updating*).

### 1.5.1
- **Docker Compose template:** the repository ships `docker-compose.yaml.default`. Copy it to `docker-compose.yaml`, which is not tracked, so `git pull` never touches your local compose file (see *Updating* for the one-time step).
- **Version:** the version now comes from the `VERSION` file instead of a build argument in the compose file.

### 1.5.0
- **Datalekken panel:** the latest 3 Dutch and 3 other data breaches at organisations from Have I Been Pwned (CC BY 4.0), below Security-adviezen.
  - Each shows the number of accounts, the domain, when the breach happened and when it was added, and the kinds of data leaked (in Dutch or English).
  - Links go to the breach page on HIBP.
  - Spam lists, malware/stealer logs, fabricated, unverified and sensitive breaches are left out.
- New config section `breaches:` and `refresh.breaches`.
- **New news category Datalekken:** DataBreaches.net (new), plus The Record, SecurityWeek, The Hacker News and Autoriteit Persoonsgegevens (moved from Tech).

### 1.4.0
- **English interface:** Nederlands / English / Auto (browser language).
  - The switch is at the top on screens ≥ 700 px, and under Instellingen → Weergave on all screens.
  - Dates and numbers follow the language. News, advisories and alerts stay in their original language.
  - Categories and presets take optional `name_en` / `short_en` / `description_en` from `config.yaml`.
- **Weerlocatie:** an Opslaan button next to the search field; Enter also saves the best match.
- **`refresh:`** in `config.yaml`: how often the browser refreshes each panel (previously fixed in the page).

### 1.3.1
- **Top-bar counts:** P2000 alerts in the last hour per service (Brandweer, Ambulance, Politie, Lifeliner, KNRM) for a configured area, default Den Haag.
  - Feeds are polled and alerts counted over a sliding 60-minute window.
  - "≥" shows while the hour is not yet fully covered (after a restart or an outage).
- **Alarmeringen panel:** the 112 notice was removed from the panel (it remains in this README).

### 1.3.0
- **Alarmeringen panel** (after Verkeer): P2000 alerts from Zwaailicht.nl per city.
  - Grouped as Brandweer, Ambulance, Politie and Lifeliner, at most 2 each, with urgency, units and details.
  - The city is chosen in Instellingen, e.g. "Den Haag" or "Alphen aan den Rijn". It is checked before saving, and "zelfde als weerlocatie" is available.
  - The Lifeliner block shows the latest flights elsewhere in the country when there was none in the city.

### 1.2.1
- **Category chips:** only for categories you follow; "+ Onderwerpen" opens the topic presets.
- **Gezondheid:** now below the "Autoriteit Persoonsgegevens acties" panel (renamed from "AP-acties"). Panel orders saved by 1.2.0 are migrated once.

### 1.2.0
- **Top bar:** the NCTV terrorism threat level and the KNMI weather code.
- **New panels:**
  - **Verkeer** (NDW open data, with VILD road names via range requests)
  - **Storingen** (Azure, Microsoft 365, AWS, Cloudflare; extensible via `statuspage`/`rss`)
  - **Gezondheid** (RIVM health alerts)
- **Panel order:** for existing users, new panels appear at their intended place in the saved order.
- **Test:** a new one catches duplicate top-level names in the page script.

### 1.1.0
- **Story grouping:** the same story from several outlets becomes one item ("Ook bij: …").
- **Read and saved:** read state, a "Bewaard" list, and an option to hide read articles.
- **First visit:** topic presets (configurable), including a regional one based on the weather province. Every category is shown, with one-click recommended sources.
- **Watchlist and mute words:** matching advisories are pinned to the top.
- **Freshness:** per panel, plus an offline banner.
- **Installable:** manifest, icons and a service worker for offline use.
- **Thumbnails:** optional, through the SSRF-safe image proxy.
- **Keyboard shortcuts:** press `?`.
- **Monitoring:** a Bronstatus view, and opt-in `/metrics`.
- **Hardening:**
  - CSP with script hashes instead of `'unsafe-inline'`
  - bidi-override stripping
  - bounded rate limiter
  - trusted-proxy setting for Docker
  - fixed two stale-response races: switching weather location or sources quickly could show old data
  - tests run during the Docker build

### 1.0.0
- News from ~80 feeds, weather (forecast, rain 2 h, warnings), live cyber threats, NCSC advisories, AP actions, themes, Docker and ISPConfig/systemd deployment.

---

## License

Copyright (C) 2026 digibaro

This program is free software: you can redistribute it and/or modify it under the terms of the GNU General Public License as published by the Free Software Foundation, either version 3 of the License, or (at your option) any later version.

This program is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; without even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See [`LICENSE`](LICENSE) for the full text of the GNU General Public License v3.0.

The news, weather, threat and alert data shown by the dashboard belong to their publishers; see [Data sources, terms and licences](#data-sources-terms-and-licences).
