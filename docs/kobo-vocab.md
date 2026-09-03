# kobo-vocab: vocabulary extraction

`kobo-vocab` reads the words you've looked up on your Kobo (`KoboReader.sqlite`'s word
list), looks each one up in a dictionary or online provider, and writes the results as
flashcard decks.

It never runs automatically — none of the app launchers trigger a lookup on startup. Run
**start-vocab.sh** (see [Installation](installation.md)) whenever you want new words looked
up: it does a fast, network-free check of how many are pending, then — only if there are
any — runs the real lookup, showing live progress in a NickelDBus modal dialog with a
Cancel button.

## Configuration

`anki-mywords.conf`:

```ini
dict_dir=/mnt/onboard/.kobo/custom-dict   # where the dictionaries live
db=/mnt/onboard/.kobo/KoboReader.sqlite
out=words                   # output directory for generated CSVs
max_senses=1                # senses to include per word
max_chars=150                # cap on each card face, in visible characters

[lang:nl]                   # how an nl word is reduced to a form worth looking up
strip=en
prefix_strip=ge

[pair:nl-de]                # the nl->de dictionary, and the cards built from it
profile=vandale
dict_examples=true
providers=dict,pons,lexicala,chatgpt
```

Sections are named for the narrowest thing their settings depend on. A `[lang:xx]` section
holds stemming and nothing else — Dutch reduces the same way whether the target is German
or English. Everything about a dictionary lives in `[pair:xx-yy]`: its profile and
selectors, plus `max_senses`, `max_chars`, `dict_examples` and `providers`, which are
global defaults and overridable per pair. This split matters because `nl-de` and `nl-en`
are different dictionaries from different sources — PONS, for example, publishes a
Dutch↔German dictionary but no Dutch↔English one, so the two pairs need different
provider lists.

`kobo-vocab` supports both the flat Kobo dictionary layout (stock/official dicts) and the
nested `<ol>/<li>` layout produced by tools like
[dictgen](https://github.com/pgaskin/dictutil) from richer sources (multiple senses,
example sentences). `max_senses`/`max_chars` trim entries from richer dictionaries down to
something readable on the e-ink screen — by default only the first sense is kept.

Definitions are stored as Markdown, not plain text: `<b>`/`<i>` become
`**bold**`/`*italic*`, multiple senses become a `- ` list, and `<br>` becomes a line break.
`max_chars` counts visible characters (not raw Markdown syntax) and never cuts inside an
emphasis marker or leaves a dangling list bullet.

See `anki-mywords.conf.example` for the full key list.

## Dictionaries

`kobo-vocab` reads Kobo-format dictionaries (`dicthtml-*.zip`) — the same format the
reader itself uses. Copy yours to `/mnt/onboard/.kobo/custom-dict/`, which is where
`dict_dir` points by default.

That location is deliberate: a dictionary in `custom-dict` is one Nickel lists, so it's
the one used when you long-press a word while reading — and therefore the one whose name
gets written into `WordList.DictSuffix`. Pointing `dict_dir` at the same folder means
every word you looked up is a word `kobo-vocab` can also look up. A private copy elsewhere
works too, but the two can drift out of sync.

Dictionary files themselves aren't included with this project — `dicthtml-*.zip` files are
commonly derived from copyrighted sources and generally aren't freely redistributable.

To check whether a dictionary file parses correctly before deploying it (and see a sample
of the entries it produces):

```sh
kobo-vocab -test-dict path/to/dicthtml-xx-yy.zip
```

### Dictionary profiles

The Kobo format fixes the outside of a dictionary but not the inside — generators disagree
about where the translation sits, whether senses are numbered, which half of an example is
the source language. That structure is described by a **profile**: a set of CSS selectors
held as config data, so a dictionary this project hasn't seen before can be supported by
writing a config section instead of code.

Four profiles ship: `vandale` (structure fully marked up), `flat` (StarDict-style
conversions with no markup, where the gloss is found by position), `nested` (numbered
senses and nothing else), and `wikdict`
([download.wikdict.com](https://download.wikdict.com), a good free source).

Normally you configure nothing — the profile is detected from the markup. `-test-dict`
prints which one was chosen, formatted as config you can paste and adjust; `-print-config`
lists the profile in force for every dictionary in `dict_dir`. To pin or correct one, add a
section keyed by language pair:

```ini
[pair:de-fr]
profile=wikdict
example=div.ex ul li     # keep the base profile, override one selector
```

Writing the profile out explicitly is worth doing even when detection already gets it
right: it means replacing a dictionary file later can't silently change cards you already
have.

See `anki-mywords.conf.example` for the full selector syntax.

## Content providers

`providers=` controls which sources are tried, in order, for each word — the first one
that finds something wins.

| provider | cost | typical latency | notes |
|---|---|---|---|
| `dict` | free | instant | local `dicthtml-*.zip`, fully offline. Always tried first. With `dict_examples=true`, builds the same headword+alternatives+example card shape as `pons`/`lexicala`/`chatgpt` from the dictionary's own example phrases, for words it happens to illustrate — no API call. |
| `pons` | 1000 queries/month, plus a daily cap | ~0.7s | [PONS](https://en.pons.com/p/online-dictionary/developers/api) online dictionary — edited, not machine-translated, so one call yields senses *and* example collocations for both card faces; needs `APIs/pons.json`. Covers only the language pairs PONS publishes, and doesn't lemmatize inflected forms, so keep `dict` ahead of it. |
| `lexicala` | ~50 calls/day (RapidAPI) | ~0.5s | Online dictionary API; needs `APIs/lexicala.json`. |
| `chatgpt` | per-token (OpenAI or any compatible endpoint) | ~1.3s | LLM-generated card: headword, a few alternative meanings, and one example sentence, consistent between front (source language) and back (target language); needs `APIs/chatgpt.json`. |

See `anki-mywords.conf.example` for the full set of `chatgpt_*`/`lexicala_conf`/`pons_conf`
keys, and how to set `providers=` globally or per `[pair:xx-yy]` section.

Each provider's tradeoffs, briefly: `dict` is instant and free but limited to what your
dictionary actually contains and to regular inflections (its stemming rules don't reach
irregular forms). `pons` and `lexicala` are real dictionaries with tighter quotas.
`chatgpt` is the only provider producing full flashcard structure for essentially any word,
at the cost of occasional mistranslation on idioms and lexicalized compounds — as with any
LLM-generated content, spot-check cards it produces.

**PONS language pairs** (check `GET https://api.pons.com/v1/dictionaries` for the current
list — this changes over time): PONS publishes bilingual dictionaries paired with German,
plus a smaller set not involving German at all. Notably, Dutch appears only in the
Dutch↔German pair — there's no Dutch↔English pair, so an `nl-en` deck can't use this
provider. A pair PONS doesn't publish is recorded after one `404` and skipped for good
afterward, without counting against the shared circuit breaker that would otherwise
disable the provider for *every* pair after just a few such misses.

`lexicala` keeps a daily call counter in a `*.state.json` file beside its credentials, so
repeated runs in a day share one budget. `pons` keeps no counter — it has no usage header
and its monthly window resets on an account-specific date, so any locally tracked budget
would be counting against the wrong window. Instead, the API's own "out of allowance"
response stands the provider down until the next calendar day, persisted so the standdown
survives between runs. Delete `APIs/pons.state.json` to retry immediately.

The card layout (`[**{head}**][\n\n{alts}][\n\n*{ex}*]` by default) is controlled by
`card_format` — one shared template across `dict` (with `dict_examples=true`), `pons`,
`lexicala`, and `chatgpt`, so restyling it applies everywhere at once. `{head}`/`{alts}`/
`{ex}` are the placeholders; wrapping a chunk in `[...]` drops it entirely, not just blank,
when every placeholder inside is empty — see the comment above `card_format` in
`anki-mywords.conf.example` for the syntax.

`pons`/`lexicala`/`chatgpt` all need working HTTPS, which some Kobo firmware can't do out
of the box — some devices ship with no system CA store at all, so every TLS request fails
with `x509: certificate signed by unknown authority` regardless of provider. `cacert.pem`
is a standard CA bundle appended to whatever trust store the OS provides, fixing that — see
`ca_cert` in `anki-mywords.conf.example`.
