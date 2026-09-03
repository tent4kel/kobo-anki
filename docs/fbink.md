# cmd/fbink: on-device e-ink UI

`kobo-anki-fbink` renders a touch-driven flashcard UI directly on the device's e-ink
panel, via [FBInk](https://github.com/NiLuJe/FBInk). No WiFi or browser needed.

## Configuration

`anki-fbink.conf`:

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
darkmode=false               # startup default only - the decks screen's own "Dark Mode"
                              # button overrides this at runtime, see "On-device settings"
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

## On-device settings

Two things are toggled from the UI itself rather than by editing config files, and persist
across relaunches in their own app-owned files under `config/` (not `words/`, which is the
deck-CSV directory the Kobo's own content scanner watches):

- **Dark Mode** - the button in the decks screen's action zone (where a global reverse
  toggle used to be). Global, applies to every deck; flips `darkmode` for this run and
  writes `config/state.json`. The `darkmode` key in `anki-fbink.conf` (including a
  `[deck:NAME]` override) only sets the startup default before this is read.
- **Per-deck reverse and tag filter** - tap "Edit" on a deck's row in the decks screen to
  open that deck's settings: a Reverse toggle (swaps front/back for that deck only) and a
  paged list of every tag used by its cards (the CSV's `tags` column, space-separated,
  matching Anki's own `note.tags` - see [core](core.md)), each toggled on/off. A deck with
  one or more tags selected shows only cards carrying at least one of them (OR, not AND)
  when reviewed, and its name in the decks list grows the selected tags in parentheses,
  e.g. `french (noun, verb)` - truncated with `…` if it doesn't fit the row, dropping back
  to the bare deck name if even that doesn't fit. Persisted to
  `config/decksettings/<deck>.json`, one file per deck that's been configured.

## Rendering architecture

The UI composes each screen into an in-memory image (Go's `x/image/font/opentype` — real
TrueType/OpenType rasterization, not a bitmap-font fallback) and blits it to the panel with
a single `fbink` call, instead of shelling out to `fbink` once per label or button.
Markdown card text (bold/italic/lists — see [kobo-vocab](kobo-vocab.md)) is parsed via
[goldmark](https://github.com/yuin/goldmark) and rendered with real mixed-style
line-wrapping. List lines always render left-aligned regardless of the surrounding block's
alignment, since a centered bullet list reads badly, and wrap with a hanging indent so a
long item's continuation lines stay in one column rather than running back under their own
bullet.

HTML/apkg cards (see [apkgimport](apkgimport.md)) render through a separate `litehtml`-based
path, described in that document.

**Block spacing**: `block_spacing` (default `0.5`) adds that fraction of a line height
after every paragraph and every list item, so consecutive senses read as separate entries
rather than one run-on. It follows Markdown's own rule for what a paragraph is — a *blank*
line. A single newline is a soft break and stays tight, which is why the default
`card_format` (see [kobo-vocab](kobo-vocab.md)) separates headword, alternatives, and
example with blank lines: that's what makes them eligible for the spacing. Wrapped
continuation lines are never spaced, and no space is added after the final block, since
trailing space would push vertically centred text off centre.

**Typography**: `x/image` applies real kerning — it reads GPOS pair positioning (and the
legacy `kern` table) for Latin text, and both the measuring and drawing paths use it, so
wrapping agrees with what lands on the panel. Kerning needs a *static* font file: an
unflattened variable font's GPOS reads as zero kerning for every pair, so put a static
instance in `font_dir`.

`x/image` does not do GSUB shaping, so a font's ligature glyphs are never reached on their
own. `ligature.go` closes that gap for the Latin f-ligatures without a full shaping engine:
it reads the font's own `liga` feature out of GSUB, keeps the substitutions whose result
glyph is also reachable as a Unicode presentation form (`U+FB00`–`U+FB06`), and rewrites
the text before measuring and drawing it. A font declaring no ligatures gets none — Go
Regular ships the `fi`/`fl` glyphs but no `liga` feature, and is correctly left alone.
Switch it off per deck with `ligatures=false`; German suppresses ligatures across morpheme
boundaries (`Auf|lage`), which needs a hyphenation dictionary to detect and isn't handled
here.

Genuine shaping beyond this — contextual alternates, mark positioning, non-Latin scripts,
ligatures with no codepoint — would mean moving the draw path onto
[go-text/typesetting](https://github.com/go-text/typesetting) (pure Go, no cgo, ARM-clean).
Nothing in this app currently needs it.
