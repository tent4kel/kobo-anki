# Kobo Anki

> **Disclaimer:** This project is vibecoded — built almost entirely with AI coding
> assistance — and has only been tested at small scale, on one device and one user's
> workflow. Expect rough edges; review the code before trusting it with your data.

Spaced repetition flashcards on Kobo e-readers. Renders directly to the e-ink display using [FBInk](https://github.com/NiLuJe/FBInk) — no WiFi or browser needed.

## How it works
1. Includes multiple ways to create and import flashcards:
-  **kobo-vocab** reads your Kobo's vocabulary database (`KoboReader.sqlite`), looks up each word in bundled dictionaries, and exports flashcard CSVs
-  **kobo-anki-apkgimport** tool to import anki decks and convert them to CSV, supporst html, images, cloze cards
-  **place your own CSV files** simple two comma seperated tables with fromt and back, Markdown supported

2. Cards are scheduled using the [FSRS](https://github.com/open-spaced-repetition/go-fsrs) spaced repetition algorithm


3. **GUI** two interfaces:
- **kobo-anki-fbink** renders a touch-driven flashcard UI directly on the device.
- There is also a **kobo-anki-server** web UI that serves flashcards via a local HTTP server for use in the Kobo browser — useful but requires WiFi.

## Requirements

- A Kobo e-reader (tested on Clara BW)
- [NickelMenu](https://pgaskin.net/NickelMenu/) for launching
- (now included)  [FBInk](https://github.com/NiLuJe/FBInk) binary for your device (GPL-3.0)

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

You also need the `fbink` binary, a static build is included.
## Installation

First copy the `.example` config files and rename them (remove `.example`), then edit to match
your setup.

Then connect the Kobo over USB and copy the files into `.adds/`, giving you this layout:

```
/mnt/onboard/.adds/kobo-anki/
├── bin/
│   ├── kobo-anki-fbink
│   ├── kobo-anki-apkgimport
│   ├── kobo-anki-server
│   ├── kobo-vocab
│   └── fbink              ← from FBInk releases
├── start.sh
├── config/
│   ├── anki-core.conf      ← copy from the .example files
│   ├── anki-fbink.conf
│   ├── anki-mywords.conf
│   ├── cacert.pem          ← needed for the online providers, see below
│   ├── cards.css           ← shared styling for html/apkg cards, see below
│   └── APIs/               ← provider credentials, only if you use them
├── dict/                   ← dicthtml-*.zip dictionaries
├── templates/              ← only needed for server mode
├── samples/
│   └── french.csv          ← sample deck; copy into words/ to try it
├── logs/                   ← created automatically at first run
└── words/                  ← flashcard CSVs go here
```

### NickelMenu config

Add to `/mnt/onboard/.adds/nm/config`:

```
menu_item:main:Kobo Anki:cmd_spawn:quiet:/mnt/onboard/.adds/kobo-anki/start.sh
menu_item:main:Update Vocabulary:cmd_spawn:quiet:/mnt/onboard/.adds/kobo-anki/start-vocab.sh
```

### Upgrading an existing install

If you already have kobo-anki installed under the old flat layout (every
file loose at the app-folder root), move things into place by hand once,
over SSH or an FTP client:

```
cd /mnt/onboard/.adds/kobo-anki
mkdir -p config logs samples
mv anki-core.conf anki-fbink.conf anki-mywords.conf cacert.pem cards.css config/
[ -f anki-apkgimport.conf ] && mv anki-apkgimport.conf config/
[ -d APIs ] && mv APIs config/
[ -f french.csv ] && mv french.csv samples/
rm -f *.log
```

If an earlier deploy left a stale nested `.adds/$KOBO_FOLDER/kobo-anki/`
directory on the device (from before deploy-ftp.sh's double-nesting bug was
fixed), delete that whole directory too.

Re-deploy after that so the binaries and start scripts land at their new
locations too.

## Configuration

### anki-core.conf

Shared settings for both fbink and server modes.

```ini
data_dir=words              # directory containing flashcard CSVs
reverse=false               # swap front/back
request_retention=0.9       # FSRS target retention (0.0-1.0)
maximum_interval=36500      # max days between reviews
enable_short_term=true      # learning steps (see Scheduling)
learning_pool=10            # max un-rated new cards admitted at once; 0 = no gate
step_gap=3                  # cards shown before an "Again" card comes back
```

See [Scheduling](#scheduling) for what the last four do.

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
block_spacing=0.5           # extra line heights after a paragraph or list item
ligatures=true              # draw the font's own fi/fl/ff/ffi/ffl ligatures
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
surrounding block's alignment, since a centered bullet list reads badly, and
wrap with a hanging indent so a long item's continuation lines stay in one
column instead of running back under their own bullet.

**Block spacing**: `block_spacing` (default `0.5`) adds that fraction of a
line height after every paragraph and every list item, so consecutive senses
read as separate entries rather than one run-on. It follows Markdown's own
rule for what a paragraph is — a *blank* line. A single newline is a soft
break and stays tight, which is why the default `card_format` separates
headword, alternatives and example with blank lines: that is what makes them
eligible for the gap. Wrapped continuation lines are never spaced. No space
is added after the final block, since trailing space would only push
vertically centred text off centre.

**Typography**: `x/image` applies real kerning — it reads GPOS pair
positioning (and the legacy `kern` table) for Latin text, and both the
measuring and drawing paths use it, so wrapping agrees with what lands on the
panel. Kerning does need a *static* font file: an unflattened variable font's
GPOS reads as zero kerning for every pair, so put a static instance in
`font_dir`.

What `x/image` does *not* do is GSUB shaping, so the font's ligature glyphs
are never reached on their own. `ligature.go` closes that gap for the Latin
f-ligatures without pulling in a shaping engine: it reads the font's own
`liga` feature out of GSUB, keeps the substitutions whose result glyph is
also reachable as a Unicode presentation form (`U+FB00`–`U+FB06`), and
rewrites the text before measuring and drawing it. For text fonts that is the
whole `liga` set — verified against Newsreader and DejaVu Serif, where all
five (ff, fi, fl, ffi, ffl) round-trip. A font declaring no ligatures gets
none: Go Regular ships the `fi`/`fl` glyphs but no `liga` feature, and is
correctly left alone. Switch it off per deck with `ligatures=false` — German
suppresses ligatures across morpheme boundaries (`Auf|lage`), which needs a
hyphenation dictionary to detect.

Beyond that lies genuine shaping — contextual alternates, mark positioning,
non-Latin scripts, ligatures with no codepoint — which would mean moving the
draw path onto [go-text/typesetting](https://github.com/go-text/typesetting)
(pure Go, no cgo, ARM-clean). Nothing in this app currently needs it.

### anki-mywords.conf

Vocabulary extractor settings.

```ini
dict_dir=/mnt/onboard/.kobo/custom-dict   # where the dictionaries live
db=/mnt/onboard/.kobo/KoboReader.sqlite
out=words                   # output directory for generated CSVs
auto_parse=false            # run kobo-vocab automatically in the background on startup
max_senses=1                # senses to include per word
max_chars=150               # cap on each card face, in visible characters

[lang:nl]                   # how an nl word is reduced to a form worth looking up
strip=en
prefix_strip=ge

[pair:nl-de]                # the nl->de dictionary, and the cards built from it
profile=vandale
dict_examples=true
providers=dict,pons,lexicala,chatgpt
```

**Sections are named for the narrowest thing their settings depend on.** A `[lang:xx]`
section holds stemming and nothing else — Dutch reduces the same way whether the target is
German or English. Everything about a dictionary lives in `[pair:xx-yy]`: its profile and
selectors, plus `max_senses`, `max_chars`, `dict_examples` and `providers`, which are
defaults globally and overridable per pair.

That distinction is load-bearing rather than tidiness. `nl-de` and `nl-en` are different
dictionaries from different generators, and PONS publishes a Dutch↔German dictionary but no
Dutch↔English one — so they need different provider lists, which a per-language section had
no way to express.

When `auto_parse=true`, `start.sh` first does a fast, network-free check of how many new
words are pending (`kobo-vocab -count-new`), then - only if there are any - launches the
real `kobo-vocab` run in the background (so it doesn't block the flashcard UI from
starting) and shows a brief on-screen notification ("N new words found, starting
lookup...", then "Vocabulary updated"/"...failed"). If nothing's new, it skips the run
entirely. When `false` (the default), you run `kobo-vocab` manually.

kobo-vocab supports both the flat Kobo dict layout (stock/official dicts) and the nested
`<ol>/<li>` layout produced by tools like [dictgen](https://github.com/pgaskin/dictutil)
from richer sources (multiple senses, example sentences). `max_senses`/`max_chars` trim
entries from these richer dicts down to something readable on the e-ink screen — by default
only the first sense is kept. Which part of a sense is used is the profile's business, not a
setting's: a `translation=` selector says where the gloss is, and the raw-block fallback for
a sense that matches none of them keeps the first block.

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
| `dict` | free | instant | local `dicthtml-*.zip` (see [Dictionaries](#dictionaries)), fully offline. Always first. With `dict_examples=true`, builds the same headword+alternatives+example card shape as `pons`/`lexicala`/`chatgpt` from the dict's own example phrases, for words it happens to illustrate - no API, no cost. |
| `pons` | 1000 queries/month, plus a daily cap | ~0.7s | [PONS](https://en.pons.com/p/online-dictionary/developers/api) online dictionary — an edited dictionary, not machine translation, so one call yields senses *and* example collocations for both card faces; needs `APIs/pons.json`. Covers only the pairs PONS publishes (see below), and does not lemmatize, so keep `dict` ahead of it |
| `lexicala` | ~50 calls/day (RapidAPI) | ~0.5s | online dictionary API; needs `APIs/lexicala.json` |
| `chatgpt` | per-token (OpenAI or any compatible endpoint) | ~1.3s | LLM-generated card: headword + a few alternative meanings + one example sentence, consistent on both the front (source language) and back (target language); needs `APIs/chatgpt.json` |

See `anki-mywords.conf.example` for the full set of `chatgpt_*`/`lexicala_conf`/`pons_conf`
keys and how to set `providers=` globally or per `[pair:xx-yy]` section.

**PONS language pairs**, as of 2026-08-11 (`GET https://api.pons.com/v1/dictionaries`, no
credentials needed — check it rather than trusting this list, it is a snapshot). 54
dictionaries: 52 bilingual plus two single-language German ones (`dede`, `dedx`). A key is
the two language codes sorted alphabetically and run together, which is also what this
provider builds from a language pair — `nl`+`de` → `denl`.

- **Paired with German (29)**: `arde bgde csde dade deel deen dees defa defi defr dehr dehu
  deis deit deja dela delb denl deno depl dept dero deru desk desl desr desv detr dezh`
- **Not involving German (23)**: `bgen enes enfr enit enpl enpt enru ensl ensr enzh esfr
  esit espl espt essl eszh frit frpl frsl frzh itpl itsl plru`

Note what is absent: Dutch appears in exactly one dictionary, `denl`. There is
**no Dutch↔English pair**, so a `nl-en` deck cannot use this provider at all. The first
such word costs one `404`; the missing dictionary is then recorded in `pons.state.json`
and that pair is skipped for good. It is deliberately *not* treated as a failure: the
circuit breaker wraps one provider instance shared by every pair in the run, so counting a
structural 404 against it used to switch PONS off for `nl-de` as well.

`lexicala` keeps a daily call counter in a `*.state.json` beside its credentials, so repeated
runs in a day share one budget rather than each assuming the whole allowance.

`pons` deliberately keeps no counter. PONS sends no usage header, and its monthly window
starts on an account-specific date, so any budget tracked locally would be counting against
the wrong window — and a month is too long a window to be wrong about. Instead the API's own
`503` ("out of allowance") stands the provider down until the next calendar day, persisted in
`APIs/pons.state.json` so the standdown survives between runs. Worst case after a rollover is
one wasted call per day. Delete that file to retry immediately.

The card layout itself (`[**{head}**][\n\n{alts}][\n\n*{ex}*]` by default - each bracketed
chunk its own Markdown paragraph, not an inline comma/space join) is controlled by
`card_format` - one shared template across `dict` (with `dict_examples=true`), `pons`,
`lexicala`, and `chatgpt`, so restyling it applies everywhere at once. `{head}`/`{alts}`/`{ex}` are the
placeholders; wrapping a chunk in `[...]` drops it entirely, not just blank, when every
placeholder inside is empty - see the comment above `card_format` in
`anki-mywords.conf.example` for the syntax.

`pons`/`lexicala`/`chatgpt` all need working HTTPS, which some Kobo firmware can't do out
of the box - certain devices ship with no system CA store at all, so every TLS request fails
with `x509: certificate signed by unknown authority` regardless of provider. `cacert.pem`
(repo root — `deploy-ftp.sh` installs it under `config/` on the device, see
[Installation](#installation)) is a standard CA bundle appended to whatever trust store the
OS provides, fixing that - see `ca_cert` in `anki-mywords.conf.example`.

**Benchmark.** A 15-word nl→de run (10 everyday words + 5 deliberately obscure ones) against
the live APIs, each provider queried independently:

The 15 words, recorded here so the run is repeatable (the original set was not written down,
so the `pons` column below comes from a re-run on this reconstructed list rather than from
the same session as the older numbers): `huis water boek spreken werken afspraak invloed
uitzoeken mooi liepen` and `onomkoopbaar beeldvorming wezel luidruchtig vergezocht`.

- **dict** found 14/15 instantly. The one miss was an irregular inflection (`liepen`, past
  tense of `lopen`) that its suffix-stripping rules can't reach — those only cover regular
  patterns.
- **lexicala** hit its real daily cap after just 4 words — the RapidAPI plan's actual quota
  was already spent elsewhere that day, well under the 45/day our local counter assumed.
  Realistically only useful as an occasional early-chain enrichment, not a bulk source.
- **mymemory** answered all 15, but quality swings hard by word: clean single-word hits for
  straightforward vocabulary, unrelated corpus-matched sentence fragments for others (e.g. an
  unrelated line about a "Präsidium" for `uitzoeken`). It has since been removed as a
  built-in provider — machine translation of a bare headword, with no sense structure to
  hang a card on, was never worth a chain slot next to a real dictionary.
- **pons** also found 14/15, missing the same `liepen` — it does not lemmatize either, and
  `fm=1` fuzzy matching is *not* a fix: it ranks by string similarity, answering `liepen`
  with `liegen`/`lieden`/`piepen`, none of which is the lemma. It produces the full
  headword+alternatives+example shape on both faces, like `chatgpt` but from an edited
  dictionary. On two words it was materially better than the local dict: `beeldvorming` →
  `Imagebildung, Meinungsbildung` where dict gives the literal cognate `Bildung` (the exact
  failure noted below), and `werken` → `arbeiten, funktionieren, wirken` where dict picks an
  unrelated idiom sense (`bringen`, "eine unerwünschte Person hinausbefördern"). Its quirk
  is packing several translations into one field — `spreken` → `sprechen, reden` arrives as
  a single headword rather than a headword plus alternatives.
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

## Scheduling

FSRS owns the long-term schedule — the due date, measured in days. It does not
own what you see next while you are actually sitting there, and the two used to
be conflated: the queue was "every card whose due date has passed", redrawn at
random on every advance. Three things went wrong as a result.

**Rating "Again" did not bring the card back.** Every FSRS interval is clamped
to a minimum of one day, Again included, so the card you had just failed was
the one card guaranteed not to reappear.

**A never-seen card has no due date at all**, which reads as "due since the
zero time" — a freshly generated 500-word deck offered all 500 at once, with
nothing tracking how many were already half-learned.

**Drawing at random with replacement** meant one card could come up twice in a
row while another due card was never shown.

A *session* now decides the running order once, when you open a deck, and then
walks it. Every due card is shown exactly once, and a card that is still
mid-step goes back into the same sitting, `step_gap` cards further on, rather
than disappearing until its due date. Keep rating a card Again and it keeps
coming back — the sitting ends when you stop failing it.

### Learning mode

`learning_pool` caps how many *never-seen* words may be in progress at once —
admitted into the sitting but not yet graduated to a real interval. It has
exactly one job: pacing new words, both how fast they arrive and how many
you're juggling at once while they're still being learned, so a 500-word deck
doesn't dump all 500 on you back to back. It has no effect on reviews — every
due review is always included, unconditionally, and never competes with new
words for a pool slot.

A word occupies its slot from admission until it graduates — until FSRS trusts
it with a real (nonzero) interval rather than "ask again in a few minutes."
Under the long-term scheduler that's every word's very first rating, whatever
grade it gets; under the short-term scheduler a word can take two or more
ratings to graduate, and it keeps its slot for all of them, so `learning_pool`
words are what you're actively working on, not merely words you've glanced at
once. Only once a slot frees does the next held word come in. By the end of
the sitting, every due word — seen or unseen — has been shown at least once.
Set `learning_pool=0` to turn the gate off entirely and admit every new word
at once, as before.

The deck list shows the real backlog — every card past its due date, unseen
words included — not the smaller opening batch a sitting starts with. The
stats page shows both, so the difference is visible where it's useful rather
than baked into the headline figure.

### Studying ahead

A deck can easily have nothing due for days — everything scheduled six days out
means six "Done!" screens in a row. When that happens the done screen offers to
study ahead: the cards that are *not* due yet, **soonest first**, so the ones
closest to falling due are the ones you pull forward. There is no limit; you
stop when you like.

It is offered rather than entered automatically. Reviewing a card early costs
it exactly the spacing that was making it stick, so the app tells you nothing
is due first and lets you decide. This is the one place the running order is
sorted instead of shuffled — with study-ahead the order *is* the point.

"Nothing due" is the scheduler's verdict on right now, and the learning pool is
part of the scheduler — so the done screen reads the same whether the deck is
empty or the pool is holding new words back. It is not the place to explain a
mechanism you should not have to think about; the deck list is where the real
backlog lives.

### Learning steps

`enable_short_term=true` puts FSRS in its learning-steps mode: a new word is
answered twice in one sitting before it gets a real interval (Good → ten
minutes → Good → a few days), and a lapsed word gets one extra look before
going back on schedule. The session queue hands those repeats over
immediately, so the "ten minutes" never costs any actual waiting — you just
answer the card twice.

It is worth leaving on. With it off you get one look per card per day, and a
repeat within the same day earns nothing at all: FSRS derives recall stability
from how long you went without seeing the card, and over zero elapsed days
that term is exactly zero — so a Good right after an Again gives back none of
what the Again took away.

## Flashcard CSV format

Cards are stored as CSV with 12 columns (FSRS scheduling plus a card id):

```
front,back,due,stability,difficulty,elapsed_days,scheduled_days,reps,lapses,state,last_review,id
hello,bonjour,2025-01-01,0,0,0,0,0,0,0,,hello
```

`id` identifies the card independently of how its faces are rendered — kobo-vocab fills it
with the word it looked up. Without it, a card's identity was its front text, so re-running
the extractor after a `card_format` change (or with a different provider answering) produced
a front that no longer matched, orphaning the review history and re-adding the word as a
duplicate. It is deliberately the **last** column, so a two-column file is never mistaken for
`id,front`. Cards without an id fall back to matching on their front text, exactly as before.

Hand-written decks can use a minimal 2-column format — the app fills in the remaining columns
on first save, and these cards keep working exactly as before:

```
hello,bonjour
merci,thank you
```

Any number of columns in between works too: a deck written by an earlier version keeps its
review history when the missing columns are filled in.

`french.csv` in the repo root is a small ready-to-use sample deck — `deploy-ftp.sh` installs
it under `samples/` on the device; copy it into `words/` to try the app before setting up any
dictionaries.

### cards.css

Shared CSS for html/apkg cards (`Format` column = `html`, i.e. decks imported by
`kobo-anki-apkgimport`) — plain-text cards from kobo-vocab/hand-written decks never touch this,
they go through goldmark instead. It's injected into every such card right before `</head>`,
so it cascades after — and can override at equal specificity — the deck's own CSS that
`kobo-anki-apkgimport` baked in at import time. Edit it and restart the app (or reload the
page in server mode); no need to re-run `kobo-anki-apkgimport`. Empty by default.

A deck that embeds its own font via `@font-face` (declared in its own CSS, not `cards.css`)
is honoured automatically — the referenced font file is loaded from the deck's own media,
the same way `<img>` sources are. Without one, four bundled sans-serif faces (regular, bold,
italic, bold-italic) cover everything else. A font is only picked up if a `@font-face` rule
actually names it — one just sitting unused in a deck's media isn't detected on its own.

**Using a Kobo system font.** `@font-face` in `cards.css` can reference one of the fonts
already on the device (`/mnt/onboard/fonts/extra-kobo/*.ttf` etc.) by its literal absolute
path — `src: url('/mnt/onboard/fonts/extra-kobo/KF_Literata-Regular.ttf')` — which
`kobo-anki-fbink` reads directly off the filesystem, and `kobo-anki-server` serves to the
browser from that same real path (via a route scoped to exactly that directory).

**Per-deck styling.** `kobo-anki-apkgimport` writes each of a deck's note types' CSS to its
own `style-<model>.css` in `<deck>.media/` (a deck usually has one or two note types, so
usually one or two files) instead of baking a copy into every single card — on one real test
deck that cut the CSV from 8.2MB to 3.1MB. Edit one of these files to restyle just that deck;
it's written once and never overwritten by re-importing the same `.apkg` (delete it to reset
to the deck's original CSS on the next import).

### Splitting an apkg import by tag

`kobo-anki-apkgimport -split-tags Ch1,Ch2` writes a separate deck per tag — `<deck> [Ch1]`,
`<deck> [Ch2]` — pulling cards with that tag out of the base `<deck>`; a card matching more
than one of the given tags is written into each matching tag's deck, and a card matching
none of them stays in the base deck. `-split-tags all` (or `-split-tags "*"`) splits by every
tag actually found in the deck instead of naming them — note a bare `-split-tags` with no
value at all isn't safe to support: Go's flag parser would silently consume whatever comes
next on the command line (even another flag like `-deck`) as its value, so `all`/`*` is the
explicit way to ask for that. `split_tags=` in `anki-apkgimport.conf` (see
`anki-apkgimport.conf.example`) sets a default so it doesn't need retyping every import; a
`-split-tags` flag, when given, overrides the config file rather than adding to it.

**Spreadsheet-friendly.** Decks are written as UTF-8 with a BOM and CRLF row terminators, so
they open correctly by double-click in Excel and Numbers — without the BOM, Excel guesses the
encoding and mangles accented characters (`Größe` → `GrÃ¶ÃŸe`). Card text can legitimately
contain line breaks, which appear as multi-line quoted fields; that is valid RFC 4180 and
spreadsheets render it as one cell, but it does mean a deck is not one-card-per-physical-line
for tools like `grep`. Files written before this change (no BOM) still load fine.

One caveat: Excel in a locale whose list separator is `;` (German, Dutch, French…) may put
everything in a single column on double-click. Use *Data → From Text/CSV* and pick comma, or
open with Numbers/LibreOffice, which honor the comma regardless of locale.

## Dictionaries

kobo-vocab uses Kobo-format dictionaries (`dicthtml-*.zip`) — the same format the reader
itself uses. Copy yours to **`/mnt/onboard/.kobo/custom-dict/`** on the device, which is
where `dict_dir` points by default.

That location is deliberate. A dictionary in `custom-dict` is one Nickel lists, so it is the
dictionary the Kobo uses when you long-press a word while reading — and therefore the one
whose name it writes into `WordList.DictSuffix`. Pointing `dict_dir` at the same folder means
every word you looked up is a word this tool can look up again. A private copy beside the app
works too, but the two drift, and then a word Nickel found is one kobo-vocab misses.

The `dicthtml-nl-de.zip` used during development is a personal digitization of Van Dale's
Dutch–German dictionary, not a freely redistributable one, which is why `dict/` stays
gitignored rather than shipped with the repo.

To check whether a dictionary file parses correctly before deploying it (and see a sample
of the entries it produces), run:

```sh
kobo-vocab -test-dict path/to/dicthtml-xx-yy.zip
```

### Dictionary profiles

The Kobo format fixes the outside of a dictionary but not the inside, and generators
disagree about everything there: where the translation sits, whether senses are numbered,
which half of an example is the foreign one. That structure is described by a **profile** —
a handful of CSS selectors held as config data — rather than by branches in the parser, so a
dictionary this project has never seen is supported by writing a config section instead of
patching code.

Four profiles ship: `vandale` (this project's own build — everything explicitly marked),
`flat` (StarDict-style conversions with no markup, where the gloss is found by position),
`nested` (numbered senses and nothing else), and `wikdict`
([download.wikdict.com](https://download.wikdict.com), a good free source to point at).

Normally you configure nothing: the profile is detected from the markup.
`-test-dict` prints which one was chosen, and prints it back as config you can paste and
adjust; `-print-config` lists the profile in force for every dictionary in `dict_dir`. To
pin or correct one, add a section keyed by language pair:

```ini
[pair:de-fr]
profile=wikdict
example=div.ex ul li     # keep the base profile, override one selector
```

Writing the profile out is worth doing even when detection is right: an explicit `profile=`
means replacing a dictionary file cannot silently change cards you already have.

See `anki-mywords.conf.example` for the full key list and the selector syntax.

## Checking your configuration

Config files never fail to load: a setting that can't be used keeps its default and the rest
of the file still applies. Anything ignored is reported with its file and line, so a typo
doesn't disappear silently:

```
anki-mywords.conf:14: unknown setting "dict_example" - ignored (typo, or does it belong in another conf file?)
anki-mywords.conf:22: max_senses="abc" is not a whole number - keeping the default
```

To see what's actually in force after defaults and section overrides are merged — useful when
a setting doesn't seem to be taking effect — run:

```sh
kobo-vocab -print-config
```

## Launcher scripts

- **start.sh** — Production launcher for NickelMenu. Kills Nickel, runs the app with crash recovery (max 5 restarts), reboots on exit. Restarting Nickel in place instead of rebooting has been tried twice and reverted both times — see the comment at the top of the file before attempting it again. The hardware watchdog needs no handling here — it's kept fed by an in-kernel thread regardless of what userspace is doing.
- **start-debug.sh** — Debug launcher for SSH. Same Nickel handling but doesn't reboot on exit (use `reboot` manually when done).
- **start-fbink.sh** — Minimal launcher, no Nickel management.
- **start-server.sh** — Starts the web server mode.
- **start-vocab.sh** — Standalone vocabulary lookup, runs alongside Nickel (no Nickel management needed). Shows live progress via a NickelDBus modal dialog with a Cancel button.

### A note on suspending/restoring Nickel

On this device (Kobo Clara BW, model P356, FW 4.45.23697) I could not find a safe way to
suspend Nickel and hand control back to it without a full reboot. The standard community
approach — kill `nickel`/`hindenburg`, run your own app, then respawn them directly, as
koreader.sh and Plato/Cadmus's own `nickel.sh` do — was reimplemented carefully against those
projects' actual source (env restoration, watchdog handling, wifi state) and cross-checked by
running Cadmus's own unmodified restart path directly. Both crashed Nickel on this hardware.
`start.sh` reboots on exit instead; see the comment at its top for the full history before
retrying this.

Along the way this surfaced an interesting, undocumented piece of Kobo firmware behavior: if
Nickel crashes repeatedly shortly after boot with a given `/usr/local/Kobo/imageformats/*.so`
plugin loaded — this is the mechanism NickelMenu and similar patches use to inject themselves —
the firmware silently renames that plugin from `.so` to `.so.failsafe`, disabling it, to break
the boot-crash loop. The device itself isn't bricked and Nickel boots fine once the offending
plugin stops loading, but its side effect (NickelMenu's own menu vanishing) looks alarming if
you don't know it's happening. Fix: rename `*.so.failsafe` back to `*.so` and reboot. I haven't
seen this documented anywhere in the usual community references.

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
