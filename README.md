# Kobo Anki

Spaced repetition flashcards on Kobo e-readers. Renders directly to the e-ink display using [FBInk](https://github.com/NiLuJe/FBInk) — no WiFi or browser needed.

Includes **kobo-vocab**, a vocabulary extractor that pulls words you looked up on your Kobo and turns them into flashcard decks using offline dictionaries.

## How it works

1. **kobo-vocab** reads your Kobo's vocabulary database (`KoboReader.sqlite`), looks up each word in bundled dictionaries, and exports flashcard CSVs
2. **kobo-anki-fbink** renders a touch-driven flashcard UI directly on the e-ink screen
3. Cards are scheduled using the [FSRS](https://github.com/open-spaced-repetition/go-fsrs) spaced repetition algorithm

There is also a **kobo-anki-server** web UI that serves flashcards via a local HTTP server for use in the Kobo browser — useful but requires WiFi.

## Requirements

- A Kobo e-reader (tested on Clara BW)
- [NickelMenu](https://pgaskin.net/NickelMenu/) for launching
- [FBInk](https://github.com/NiLuJe/FBInk) binary for your device (GPL-3.0)
- SSH access for initial setup

## Building from source

Requires Go 1.22+.

```sh
git clone https://github.com/tent4kel/kobo-anki.git
cd kobo-anki
sh build.sh
```

This cross-compiles three ARM binaries into `bin/`:
- `kobo-anki-fbink` — e-ink flashcard UI
- `kobo-anki-server` — web flashcard UI
- `kobo-vocab` — vocabulary extractor

You also need the `fbink` binary. Download the Kobo build from [FBInk releases](https://github.com/NiLuJe/FBInk/releases) and place it in `bin/`.

## Installation

Copy to your Kobo via USB or SCP:

```
/mnt/onboard/.adds/kobo-anki/
├── bin/
│   ├── kobo-anki-fbink
│   ├── kobo-anki-server
│   ├── kobo-vocab
│   └── fbink              ← from FBInk releases
├── start.sh
├── anki-core.conf          ← copy from .example files
├── anki-fbink.conf
├── anki-mywords.conf
├── templates/              ← only needed for server mode
└── words/                  ← flashcard CSVs go here
```

Copy the `.example` config files and rename them (remove `.example`). Edit to match your setup.

### NickelMenu config

Add to `/mnt/onboard/.adds/nm/config`:

```
menu_item:main:Kobo Anki:cmd_spawn:quiet:/mnt/onboard/.adds/kobo-anki/start.sh
```

## Configuration

### anki-core.conf

Shared settings for both fbink and server modes.

```ini
data_dir=words              # directory containing flashcard CSVs
reverse=false               # swap front/back
request_retention=0.9       # FSRS target retention (0.0-1.0)
maximum_interval=36500      # max days between reviews
enable_short_term=false     # false = schedule days out, true = minutes
```

### anki-fbink.conf

Display settings for the e-ink UI.

```ini
font_dir=/mnt/onboard/fonts/extra-kobo
font_front=KF_Newsreader-Regular.ttf
font_back=KF_Newsreader-Italic.ttf
font_menu=KF_Newsreader-Regular.ttf
size_title=24
size_card=28
size_menu=16
darkmode=false
touch_cooldown=300
```

### anki-mywords.conf

Vocabulary extractor settings. See the example file for language-specific stemming rules.

```ini
dict_dir=dict               # path to dictionary zip files
db=/mnt/onboard/.kobo/KoboReader.sqlite
out=words                   # output directory for generated CSVs
```

## Flashcard CSV format

Cards are stored as CSV with 11 columns (FSRS format):

```
front,back,due,stability,difficulty,elapsed_days,scheduled_days,reps,lapses,state,last_review
hello,bonjour,2025-01-01,0,0,0,0,0,0,0,
```

New cards can use a minimal 2-column format — the app adds FSRS fields on first review:

```
hello,bonjour
merci,thank you
```

## Dictionaries

kobo-vocab uses Kobo-format dictionaries (`dicthtml-*.zip`). These are the same format used by the Kobo reader itself. Place dictionary zips in the `dict/` directory.

## Launcher scripts

- **start.sh** — Production launcher for NickelMenu. Kills Nickel, feeds the hardware watchdog, runs the app with crash recovery (max 5 restarts), reboots on exit.
- **start-debug.sh** — Debug launcher for SSH. Same Nickel/watchdog handling but doesn't reboot on exit.
- **start-fbink.sh** — Minimal launcher, no Nickel management.
- **start-server.sh** — Starts the web server mode.

## Credits

- [FSRS](https://github.com/open-spaced-repetition/go-fsrs) — spaced repetition algorithm (MIT)
- [FBInk](https://github.com/NiLuJe/FBInk) — e-ink framebuffer rendering (GPL-3.0)
- [dictutil](https://github.com/pgaskin/dictutil) — Kobo dictionary parsing (MIT)
- [go-sqlite](https://pkg.go.dev/modernc.org/sqlite) — pure Go SQLite driver (BSD)
- [NickelMenu](https://pgaskin.net/NickelMenu/) — Kobo launcher

## License

MIT
