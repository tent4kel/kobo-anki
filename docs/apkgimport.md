# cmd/apkgimport: importing real Anki decks

`kobo-anki-apkgimport` converts a real Anki `.apkg` export into this app's CSV deck
format — HTML, images, and Cloze cards included. It resolves each card's question/answer
HTML once, at import time, and writes ordinary deck rows (see [core](core.md)); the
scheduler and both review UIs never need to know a card came from an apkg rather than
being hand-written.

## Usage

```sh
kobo-anki-apkgimport -apkg path/to/deck.apkg -deck "Deck Name" -datadir words
```

Or drop `.apkg` files into an `apkg/` folder next to the app and run **start-apkg.sh** —
see [Installation](installation.md) — which imports every file it finds, naming each deck
after its filename.

Re-running an import against the same deck name merges by card, keeping existing review
history and only refreshing the rendered faces (and tags) in case the source `.apkg`
changed.

## Media

A deck's images, per-model stylesheets, and an archived copy of the source `.apkg` are
stored together in one uncompressed zip per deck, next to its CSV, rather than as loose
files in a folder. This keeps individual media files from being independently visible to
the device's own content indexing. The archive is stored uncompressed, so reading an entry
back at render time costs no more than reading a loose file would.

## cards.css

Shared CSS for HTML/apkg cards — plain-text cards from `kobo-vocab` or hand-written decks
never touch this; they render through goldmark's Markdown path instead (see
[fbink](fbink.md)). It's injected into every HTML card right before `</head>`, so it
cascades after — and can override at equal specificity — the deck's own CSS that
`kobo-anki-apkgimport` bakes in at import time. Edit it and restart the app (or reload the
page in server mode); no need to re-run the importer. Empty by default.

A deck that embeds its own font via `@font-face` (declared in its own CSS, not
`cards.css`) is honoured automatically — the referenced font file loads from the deck's own
media, the same way `<img>` sources do. Without one, four bundled sans-serif faces
(regular, bold, italic, bold-italic) cover everything else. A font is only picked up if a
`@font-face` rule actually names it.

**Using a Kobo system font.** `@font-face` in `cards.css` can reference one of the fonts
already on the device (`/mnt/onboard/fonts/extra-kobo/*.ttf` etc.) by its literal absolute
path — `src: url('/mnt/onboard/fonts/extra-kobo/KF_Literata-Regular.ttf')`.

**Per-deck styling.** Each of a deck's note types' CSS is written to its own
`style-<model>.css` entry in the deck's media archive (a deck usually has one or two note
types) instead of being baked into every single card, which meaningfully reduces deck file
size on decks with many cards sharing one note type. Edit that entry to restyle just that
deck; it's written once and never overwritten by re-importing the same `.apkg` (delete it
to reset to the deck's original CSS on the next import).

## Splitting an import by tag

`kobo-anki-apkgimport -split-tags Ch1,Ch2` writes a separate deck per tag — `<deck> [Ch1]`,
`<deck> [Ch2]` — pulling cards with that tag out of the base deck. A card matching more
than one of the given tags is written into each matching tag's deck; a card matching none
of them stays in the base deck. `-split-tags all` (or `-split-tags "*"`) splits by every
tag actually found in the deck instead of naming them.

`split_tags=` in `anki-apkgimport.conf` (see `anki-apkgimport.conf.example`) sets a default
so it doesn't need retyping every import; a `-split-tags` flag, when given, overrides the
config file rather than adding to it.
