# On-device folder layout cleanup

## Context

The project grew feature by feature (fbink UI, web server, vocab lookup,
apkg import, then a standalone vocab launcher) and the on-device install
grew the same way: every new config file, credential, and static asset
landed as one more loose file at the app-folder root, next to the launcher
scripts that NickelMenu invokes directly. Today `/mnt/onboard/.adds/kobo-anki/`
mixes five `start*.sh` entry points, four `.conf` files, `cacert.pem`,
`cards.css`, `APIs/*.json` credentials, a sample deck, and (at runtime)
several scattered log files, all at the same level as `bin/` and `templates/`.

`.gitignore` already conceptually separates "user data," "config files,"
"API credentials," and "logs" into their own sections — the on-device
layout just never caught up to that mental model. The goal here is
specifically the **on-device** layout (what gets deployed and what the
binaries read at runtime), not the dev repo's own root file layout, which a
quick check confirmed isn't read by any test or documented local-run
workflow and can stay as-is.

Decisions already made in discussion:
- New folders are named `config/` and `logs/`.
- The four `anki-*.conf` files keep their current names (just relocated) —
  the folder alone gives the context the prefix used to.
- No automated migration code. The existing on-device install gets a
  documented manual migration step; this is a personal install, not a
  multi-user rollout.

## Target on-device tree

```
/mnt/onboard/.adds/kobo-anki/                 (or $KOBO_FOLDER, see below)
├── start.sh, start-debug.sh, start-fbink.sh, start-server.sh, start-vocab.sh
├── bin/            (unchanged)
├── templates/      (unchanged - server-mode HTML templates)
├── config/
│   ├── anki-core.conf, anki-fbink.conf, anki-mywords.conf, anki-apkgimport.conf
│   ├── cacert.pem
│   ├── cards.css
│   └── APIs/*.json
├── samples/
│   └── french.csv   (copy into words/ to try it - same purpose, new home)
├── words/          (unchanged - deploy-ftp.sh still never touches it)
└── logs/           (created at first run; not shipped by deploy-ftp.sh)
```

`cards.css` goes under `config/` rather than `templates/`: it's shared by
both `fbink` and `server` (unlike `templates/`, which the README already
documents as server-only), and it's a visual tunable in the same spirit as
`anki-fbink.conf`'s font settings. `samples/` is new and deliberately
minimal (one file) so `french.csv` isn't misfiled as config just because it
has nowhere else to go now that root is start-scripts-only.

## Go code changes

`kobo-vocab`'s config loader already resolves `cacert.pem`, `lexicala_conf`,
`pons_conf`, and `chatgpt_conf` relative to whichever directory
`anki-mywords.conf` itself lives in (the `confDir` logic in `loadConfig`,
`kobo-vocab/config.go`). Once the `-conf` flag points at
`config/anki-mywords.conf`, all four follow automatically with **no code
change** beyond the flag's default. The literal-path edits actually needed:

- `kobo-vocab/main.go`: default `-conf` flag value →
  `"config/anki-mywords.conf"`.
- `cmd/fbink/main.go`:
  - `core.LoadCoreConfig("anki-core.conf")` → `"config/anki-core.conf"`
  - `loadConfig()`'s primary lookup path → `"config/anki-fbink.conf"`
  - `os.ReadFile("cards.css")` → `os.ReadFile("config/cards.css")`
- `cmd/server/main.go`:
  - `core.LoadCoreConfig("anki-core.conf")` → `"config/anki-core.conf"`
  - `os.ReadFile("cards.css")` → `os.ReadFile("config/cards.css")`
- `cmd/apkgimport/main.go`:
  - `loadSplitTagsConfig("anki-apkgimport.conf")` →
    `loadSplitTagsConfig("config/anki-apkgimport.conf")`

**Left alone:** `cmd/fbink`'s `loadConfig()` has a second, undocumented
fallback path, `filepath.Join(dataDir, "anki-fbink.conf")` (i.e. inside
`words/`). Only the primary path is updated; this fallback is out of scope
for a path-relocation pass since its original intent isn't documented
anywhere and removing it isn't necessary to achieve the goal here.

**Also left alone:** the `-count-new` flag and `auto_parse` config-key
support were already removed from `kobo-vocab` in a prior pass (see the
Go-side cleanup work). The start scripts still reference both — that
remains the separately-deferred shell-script cleanup and is not touched by
this spec beyond updating the *paths* those dead references point at (see
below), so the pre-existing brokenness doesn't get a second, unrelated
cause layered on top of it.

## Start script changes

Every `-conf anki-mywords.conf` (used by `start.sh`, `start-fbink.sh`,
`start-server.sh`, `start-debug.sh`, `start-vocab.sh`, plus the dead
`grep`-for-`auto_parse` line in `start.sh`/`start-fbink.sh` that reads the
same file by path) becomes `-conf config/anki-mywords.conf`. Log redirects
move under `logs/`:

- `start.sh`: `LOG="$(pwd)/kobo-anki.log"` → `LOG="$(pwd)/logs/kobo-anki.log"`
- `start-debug.sh`: `LOG=kobo-anki.log` → `LOG=logs/kobo-anki.log`
- `start-fbink.sh`, `start-server.sh`, `start-vocab.sh`: `vocab.log` →
  `logs/vocab.log`
- `start-vocab.sh`: `qndb.log` → `logs/qndb.log`

Since `deploy-ftp.sh` never ships a `logs/` directory (it's created by the
app at runtime, not part of the install payload), each script needs a
`mkdir -p logs` guard before its first log write, so a fresh install
doesn't fail on the first `>>logs/...` redirect.

## `deploy-ftp.sh` changes

Two independent things change here:

**1. Config/logs/samples staging.** `mkdir -p` gains `config` (and
`samples` when `french.csv` is present); the `cp` destinations for the four
`.conf` files, `cacert.pem`, `cards.css`, and `APIs/*.json` all move to
`$STAGE/kobo-anki/config/...`; `french.csv`'s destination moves to
`$STAGE/kobo-anki/samples/`.

**2. Fix the pre-existing double-nesting bug.** Today the script stages
everything under `$STAGE/kobo-anki/` and uploads to
`$FTP_BASE/$f` where `$FTP_BASE = .adds/$KOBO_FOLDER` — landing files at
`.adds/$KOBO_FOLDER/kobo-anki/...`, a redundant extra `kobo-anki/` level
that doesn't match the README's own NickelMenu example
(`.../.adds/kobo-anki/start.sh`, no inner `kobo-anki/`). This is fixed by
staging files as the app-root contents directly (`$STAGE/bin/`,
`$STAGE/config/`, `$STAGE/start.sh`, etc. — no wrapping subfolder) and
uploading via `find . -type f` from inside `$STAGE`, so `$KOBO_FOLDER`
(default `kobo-anki`, or e.g. `kobo-anki-beta` for a side-by-side test
install) *is* the on-device root, matching the tree diagram exactly. This
bug fix is independent of the config/logs/samples reorganization but shares
the same lines of the same script, so it's done in the same pass.

## README changes

- Replace the on-device tree diagram with the new layout.
- Add a short "upgrading an existing install" note: SSH/FTP in,
  `mkdir config logs samples`, `mv anki-*.conf cacert.pem cards.css
  config/`, `mv APIs config/`, `mv french.csv samples/` (if present),
  delete the old stray root `*.log` files. Manual, one-time, no code.
- No other content changes (e.g. the existing `dict/` mention in the tree
  diagram documents an alternative `dict_dir` setting, not a bug — left
  alone).

## Out of scope

- The dev repo's own root file layout (`anki-*.conf`, `cacert.pem`,
  `cards.css`, `APIs/`, `french.csv` at the repo root) — not read by any
  test or documented local-run workflow, so it stays flat; only
  `deploy-ftp.sh`'s staging output is reorganized.
- Removing the dead `-count-new`/`auto_parse` references from the start
  scripts — separately deferred shell-script cleanup.
- `cmd/fbink`'s undocumented `words/`-relative `anki-fbink.conf` fallback
  path.
- Any automated on-device migration tooling.

## Verification

- `go build ./... && go vet ./... && go test ./...` after the Go path
  changes (no test currently depends on the old hardcoded paths, per
  investigation, so none should need updating).
- Run `deploy-ftp.sh` against the real Kobo (see `kobo-deploy-workflow`
  memory: ping-before-deploy, real-device testing) and confirm the
  resulting tree matches this spec exactly, with `start.sh` still reachable
  at `.adds/kobo-anki/start.sh` for the existing NickelMenu entry.
- Confirm on-device: vocab lookup finds its dictionaries/credentials
  (`config/anki-mywords.conf` → `config/APIs/*.json`, `config/cacert.pem`),
  the web server picks up `config/anki-core.conf` and `config/cards.css`,
  and logs land under `logs/`.
