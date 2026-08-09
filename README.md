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
line_height=1.0             # multiplier on the font's natural line height
margin=0                    # pixels inset around the front/back card content area
darkmode=false
touch_cooldown=300

# Optional: explicit bold/italic files for Markdown emphasis in card text.
# If omitted, a same-directory "Foo-Regular.ttf" -> "Foo-Bold.ttf" naming
# convention is tried automatically; if no real file is found either way,
# bold falls back to a synthetic double-strike and italic falls back to
# plain text (no synthetic italic).
# font_front_bold=KF_Newsreader-Bold.ttf
# font_front_italic=KF_Newsreader-Italic.ttf
# font_back_bold=KF_Newsreader-BoldItalic.ttf

# Optional per-deck overrides for the front/back card screens - any key
# above can be overridden for one deck (the CSV filename without ".csv"):
# [deck:vocab-nl-de]
# size_card=32
# line_height=1.3
```

**Rendering architecture**: the e-ink UI composes each screen into an in-memory
image (Go's `x/image/font/opentype` — genuine TrueType/OpenType rasterization,
not a bitmap-font fallback) and blits it to the panel with a single `fbink`
call, instead of shelling out to `fbink` once per label/button. Card front/back
text is parsed as Markdown (bold/italic/lists — see `kobo-vocab` below) via
[goldmark](https://github.com/yuin/goldmark) and rendered with real mixed-style
line-wrapping. List lines always render left-aligned regardless of the
surrounding block's alignment, since a centered bullet list reads badly.

### anki-mywords.conf

Vocabulary extractor settings. See the example file for language-specific stemming rules.

```ini
dict_dir=dict               # path to dictionary zip files
db=/mnt/onboard/.kobo/KoboReader.sqlite
out=words                   # output directory for generated CSVs
auto_parse=false            # run kobo-vocab automatically in the background on startup
max_senses=1                # how many senses (<li> entries) to include per word
max_depth=1                 # direct sub-blocks to keep per sense (1 = gloss only, drops examples)
max_chars=150               # hard cap on definition length, as a safety net
```

`max_senses`/`max_depth`/`max_chars` can also be set inside a `[lang]` section (e.g. `[nl]`)
to override the global defaults for words from that language only.

When `auto_parse=true`, `start.sh` first does a fast, network-free check of how many new
words are pending (`kobo-vocab -count-new`), then - only if there are any - launches the
real `kobo-vocab` run in the background (so it doesn't block the flashcard UI from
starting) and shows a brief on-screen notification ("N new words found, starting
lookup...", then "Vocabulary updated"/"...failed"). If nothing's new, it skips the run
entirely. When `false` (the default), you run `kobo-vocab` manually.

kobo-vocab supports both the flat Kobo dict layout (stock/official dicts) and the nested
`<ol>/<li>` layout produced by tools like [dictgen](https://github.com/pgaskin/dictutil)
from richer sources (multiple senses, example sentences). `max_senses`/`max_depth`/`max_chars`
trim entries from these richer dicts down to something readable on the e-ink screen — by
default only the first sense's core gloss is kept, with example sentences dropped.

Definitions are stored as **Markdown**, not plain text: `<b>`/`<i>` become `**bold**`/`*italic*`,
multiple senses become a `- ` list, and `<br>` becomes a line break. `max_chars` counts visible
characters (not raw Markdown syntax) and never cuts inside an emphasis marker or leaves a
dangling list bullet. Both the e-ink UI and the web UI (`cmd/server`) render this Markdown
properly rather than showing literal asterisks.

### Content providers

`providers=` controls which sources are tried, in order, for each word — the first one that
finds something wins. Built-in providers:

| provider | cost | typical latency | notes |
|---|---|---|---|
| `dict` | free | instant | local `dicthtml-*.zip` (see [Dictionaries](#dictionaries)), fully offline. Always first. With `dict_examples=true`, builds the same headword+alternatives+example card shape as `lexicala`/`chatgpt` from the dict's own example phrases, for words it happens to illustrate - no API, no cost. |
| `lexicala` | ~50 calls/day (RapidAPI) | ~0.5s | online dictionary API; needs `APIs/lexicala.json` |
| `mymemory` | free | ~1s | machine-translation API, no credentials needed |
| `chatgpt` | per-token (OpenAI or any compatible endpoint) | ~1.3s | LLM-generated card: headword + a few alternative meanings + one example sentence, consistent on both the front (source language) and back (target language); needs `APIs/chatgpt.json` |

See `anki-mywords.conf.example` for the full set of `chatgpt_*`/`lexicala_conf` keys and how
to set `providers=` globally or per `[lang]` section.

The card layout itself (`[**{head}**][, {alts}][ *{ex}*]` by default) is controlled by
`card_format` - one shared template across `dict` (with `dict_examples=true`), `lexicala`,
and `chatgpt`, so restyling it applies everywhere at once. `{head}`/`{alts}`/`{ex}` are the
placeholders; wrapping a chunk in `[...]` drops it entirely, not just blank, when every
placeholder inside is empty - see the comment above `card_format` in
`anki-mywords.conf.example` for the syntax.

`lexicala`/`mymemory`/`chatgpt` all need working HTTPS, which some Kobo firmware can't do out
of the box - certain devices ship with no system CA store at all, so every TLS request fails
with `x509: certificate signed by unknown authority` regardless of provider. `cacert.pem`
(repo root, deployed automatically by `deploy-ftp.sh`) is a standard CA bundle appended to
whatever trust store the OS provides, fixing that - see `ca_cert` in
`anki-mywords.conf.example`.

**Benchmark.** A 15-word nl→de run (10 everyday words + 5 deliberately obscure ones) against
the live APIs, each provider queried independently:

- **dict** found 14/15 instantly. The one miss was an irregular inflection (`liepen`, past
  tense of `lopen`) that its suffix-stripping rules can't reach — those only cover regular
  patterns.
- **lexicala** hit its real daily cap after just 4 words — the RapidAPI plan's actual quota
  was already spent elsewhere that day, well under the 45/day our local counter assumed.
  Realistically only useful as an occasional early-chain enrichment, not a bulk source.
- **mymemory** answered all 15, but quality swings hard by word: clean single-word hits for
  straightforward vocabulary, unrelated corpus-matched sentence fragments for others (e.g. an
  unrelated line about a "Präsidium" for `uitzoeken`).
- **chatgpt** is the only provider producing real flashcard structure (headword + curated
  alternatives + one example sentence that translates consistently between front and back),
  but it isn't infallible: it mistranslated `onomkoopbaar` ("incorruptible/can't be bribed")
  as `unverkäuflich` ("not for sale"), a case dict and mymemory both got right with
  `unbestechlich`. And on the compound `beeldvorming` ("public image/perception"), both dict
  *and* chatgpt independently fell back to the literal cognate `Bildung` ("education"),
  missing the lexicalized whole-word meaning.

Digging into that benchmark led to `dict_examples`: dicthtml-nl-de.zip already stores real
example phrases (Dutch source + German translation) right next to each sense, in a `<li>`
with no `<div>` wrapper - a plain-text sibling immediately after the sense that *does* have
one. That structural difference reliably tells "this is an example for the sense before it"
apart from "this is actually the next sense", so `dict` can build the same headword +
alternatives + example card shape as `lexicala`/`chatgpt`, for free and instantly, for any
word the dictionary happens to illustrate.

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

kobo-vocab uses Kobo-format dictionaries (`dicthtml-*.zip`). These are the same format used by the Kobo reader itself. Place dictionary zips in the `dict/` directory (gitignored, not checked into the repo — bring your own).

The `dicthtml-nl-de.zip` used during development is a personal digitization of Van Dale's
Dutch–German dictionary, not a freely redistributable one, which is why `dict/` stays
gitignored rather than shipped with the repo.

To check whether a dictionary file parses correctly before deploying it (and see a sample
of the entries it produces), run:

```sh
kobo-vocab -test-dict path/to/dicthtml-xx-yy.zip
```

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
- [goldmark](https://github.com/yuin/goldmark) — Markdown parsing (MIT)
- [x/image](https://pkg.go.dev/golang.org/x/image) — pure-Go TrueType/OpenType font rendering (BSD)
- [NickelMenu](https://pgaskin.net/NickelMenu/) — Kobo launcher

## License

MIT
