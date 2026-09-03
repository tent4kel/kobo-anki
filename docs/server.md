# cmd/server: web UI

`kobo-anki-server` serves the same flashcard review flow as `cmd/fbink`, over a local HTTP
server, for use in the Kobo's built-in browser. It shares scheduling, deck loading, and
Markdown/HTML card rendering with `cmd/fbink` via the `core` package (see
[core](core.md)) — the two front ends stay behaviorally identical because neither
duplicates that logic.

Requires WiFi (to reach the device's own IP from its browser) — there's no offline mode
for this UI; use `cmd/fbink` for that.

## Running it

```sh
./start-server.sh
```

listens on port `8080`. Point the Kobo's browser (or any browser on the same network) at
`http://<device-ip>:8080/`.

## Routes

| route | purpose |
|---|---|
| `/` | deck list |
| `/study` | current card's front |
| `/back` | current card's back, with rating buttons |
| `/rate` | records a rating, advances to the next card |
| `/stats` | per-deck due/total counts |
| `/media/<deck>/<file>` | a deck's own media (images, per-model stylesheets) — see [apkgimport](apkgimport.md) |
| `/mnt/onboard/fonts/*` | serves the Kobo's own bundled fonts, so a deck's `cards.css` can reference one by absolute path the same way `cmd/fbink` does |
| `/quit` | stops the server |

## Templates

`templates/*.html` must be present next to the binary — the server has no built-in
fallback UI. These are plain HTML with a small amount of inline JavaScript for pagination
(a discrete `scrollTop` jump per tap rather than smooth scrolling, since the target
browser is an old embedded WebKit build with limited CSS/JS support).

## Configuration

`cmd/server` reads `anki-core.conf` (see [core](core.md)) for scheduling settings and
`data_dir`. It has no config file of its own beyond that.
