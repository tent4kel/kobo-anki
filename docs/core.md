# core: scheduling and deck format

`core` is the shared package behind both review UIs (`cmd/fbink`, `cmd/server`): the FSRS
scheduler, the session queue that decides what to show next, and the deck CSV format both
UIs read and write.

## Configuration

`anki-core.conf` holds settings shared by both review UIs:

```ini
data_dir=words              # directory containing flashcard CSVs
reverse=false               # swap front/back
request_retention=0.9       # FSRS target retention (0.0-1.0)
maximum_interval=36500      # max days between reviews
enable_short_term=true      # learning steps, see below
learning_pool=10            # max un-rated new cards admitted at once; 0 = no gate
step_gap=3                  # cards shown before an "Again" card comes back
```

## Scheduling

FSRS owns the long-term schedule — the due date, measured in days. It does not own what
you see next while you're actually sitting there; those are two different concerns.

A *session* decides the running order once, when you open a deck, and then walks it. Every
due card is shown exactly once per sitting, and a card still mid-learning-step goes back
into the same sitting `step_gap` cards further on, rather than disappearing until its due
date. This avoids three failure modes a random-redraw queue has: "Again" not reliably
bringing a card back (every FSRS interval is clamped to a minimum of one day, so a
random-redraw queue can drop a just-failed card for a full day), a never-seen card reading
as "due since the epoch" (dumping an entire freshly generated deck into one sitting with no
pacing), and a card coming up twice in a row while another due card is skipped.

### Learning mode

`learning_pool` caps how many *never-seen* words may be in progress at once — admitted
into the sitting but not yet graduated to a real interval. It paces new words, both how
fast they arrive and how many you're juggling while they're still being learned, so a
large new deck doesn't arrive all at once. It has no effect on reviews: every due review is
always included, unconditionally, and never competes with new words for a pool slot.

A word occupies its slot from admission until it graduates — until FSRS gives it a real
(nonzero) interval rather than "ask again in a few minutes." Under the long-term scheduler
that's every word's first rating, whatever grade it gets; under the short-term scheduler
(see Learning steps below) a word can take two or more ratings to graduate, keeping its
slot for all of them. Only once a slot frees does the next held word come in. By the end of
the sitting, every due word — seen or unseen — has been shown at least once. Set
`learning_pool=0` to admit every new word at once.

The deck list shows the real backlog — every card past its due date, unseen words included
— not the smaller opening batch a sitting starts with. The stats page shows both.

### Studying ahead

A deck can have nothing due for days at a stretch. When that happens, the done screen
offers to study ahead: cards that are not due yet, soonest-first, so the ones closest to
falling due are the ones pulled forward. There is no limit.

Study-ahead is offered rather than entered automatically — reviewing a card early costs it
some of the spacing that was making it stick. This is also the one place the running order
is sorted rather than shuffled, since with study-ahead the order is the point.

### Learning steps

`enable_short_term=true` puts FSRS in its learning-steps mode: a new word is answered
twice in one sitting before it gets a real interval (Good → ten minutes → Good → a few
days), and a lapsed word gets one extra look before going back on schedule. The session
queue hands those repeats over immediately, so the "ten minutes" costs no actual waiting —
you just answer the card twice.

With it off, you get one look per card per day, and a repeat within the same day earns
nothing: FSRS derives recall stability from how long you went without seeing the card, and
over zero elapsed days that term is zero — a Good right after an Again gives back none of
what the Again took away.

## Flashcard CSV format

Cards are stored as CSV, one file per deck:

```
front,back,due,stability,difficulty,elapsed_days,scheduled_days,reps,lapses,state,last_review,id,format,tags
hello,bonjour,2025-01-01,0,0,0,0,0,0,0,,hello,,
```

`front`/`back` are the two faces. The rest is FSRS scheduling state, plus:

- `id` — a stable identifier independent of how the card's faces render. `kobo-vocab` and
  `kobo-anki-apkgimport` both set it; without it, a card's identity is its front text, so
  re-rendering the same card (a different provider, a `card_format` change) would
  otherwise orphan its review history and re-add it as a duplicate. It's deliberately the
  second-to-last column, so a hand-written two-column file is never mistaken for
  `id,front`.
- `format` — `""` for Markdown (goldmark; see `cmd/fbink`/`cmd/server` docs), `html` for
  already-resolved HTML/CSS (apkg imports; see [apkgimport](apkgimport.md)).
- `tags` — space-separated tags carried over from an apkg import. Not consumed by anything
  yet; stored for future tag-based filtering.

Hand-written decks can use a minimal 2-column format — the app fills in the remaining
columns on first save:

```
hello,bonjour
merci,thank you
```

Any number of columns in between also works: a deck written by an earlier version keeps
its review history as later columns are added.

`french.csv` in the repo root is a small ready-to-use sample deck; copy it into `words/`
to try the app before setting up any dictionaries.

**Spreadsheet-friendly.** Decks are written as UTF-8 with a BOM and CRLF row terminators,
so they open correctly by double-click in Excel and Numbers — without the BOM, Excel
guesses the encoding and mangles accented characters (`Größe` → `GrÃ¶ÃŸe`). Card text can
legitimately contain line breaks, which appear as multi-line quoted fields; that's valid
RFC 4180 and spreadsheets render it as one cell, but a deck is not one-card-per-line for
tools like `grep`. Files written before this existed (no BOM) still load fine.

One caveat: Excel in a locale whose list separator is `;` (German, Dutch, French…) may put
everything in a single column on double-click. Use *Data → From Text/CSV* and pick comma,
or open with Numbers/LibreOffice, which honor the comma regardless of locale.
