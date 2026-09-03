# Kobo Anki

Spaced repetition flashcards on Kobo e-readers, rendering directly to the e-ink display via
[FBInk](https://github.com/NiLuJe/FBInk) — no WiFi or browser required for the primary UI.

> **Note:** This project was built with substantial AI coding assistance and has been
> tested at small scale, on one device and one user's workflow. Review the code before
> trusting it with data you care about.

## How it works

Flashcards come from three sources:

- **kobo-vocab** reads the words you've looked up on your Kobo and builds cards from
  dictionaries or online providers.
- **kobo-anki-apkgimport** converts real Anki `.apkg` decks — HTML, images, and Cloze
  cards included.
- **Hand-written CSV** — a plain two-column `front,back` file; Markdown supported.

Cards are scheduled with the [FSRS](https://github.com/open-spaced-repetition/go-fsrs)
spaced repetition algorithm, and reviewed through either of two interfaces:

- **kobo-anki-fbink** — a touch-driven UI rendered directly on the e-ink panel.
- **kobo-anki-server** — a web UI served over the device's own WiFi, for use in the Kobo's
  browser.

## Modules

| module | what it does | docs |
|---|---|---|
| `core` | FSRS scheduling, session queue, and deck CSV format shared by both review UIs | [docs/core.md](docs/core.md) |
| `kobo-vocab` | Extracts vocabulary from your Kobo's lookup history; builds cards from dictionaries and online providers | [docs/kobo-vocab.md](docs/kobo-vocab.md) |
| `cmd/apkgimport` | Imports real Anki `.apkg` decks | [docs/apkgimport.md](docs/apkgimport.md) |
| `cmd/fbink` | On-device e-ink review UI | [docs/fbink.md](docs/fbink.md) |
| `cmd/server` | Web-based review UI | [docs/server.md](docs/server.md) |

## Getting started

See [docs/installation.md](docs/installation.md) for requirements, building from source,
and installing on the device.

## Credits

- [FSRS](https://github.com/open-spaced-repetition/go-fsrs) — spaced repetition algorithm (MIT)
- [FBInk](https://github.com/NiLuJe/FBInk) — e-ink framebuffer rendering (GPL-3.0)
- [dictutil](https://github.com/pgaskin/dictutil) — Kobo dictionary parsing (MIT)
- [go-sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go SQLite driver (BSD)
- [goldmark](https://github.com/yuin/goldmark) — Markdown parsing (MIT)
- [x/image](https://pkg.go.dev/golang.org/x/image) — pure-Go TrueType/OpenType font rendering (BSD)
- [NickelMenu](https://pgaskin.net/NickelMenu/) — Kobo launcher

## License

MIT
