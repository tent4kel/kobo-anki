# On-Device Folder Layout Cleanup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Reorganize the deployed kobo-anki app folder on the Kobo from a flat pile of files into `config/`, `logs/`, and `samples/` subfolders (keeping only the `start*.sh` launchers, `bin/`, `templates/`, and `words/` at the root), and fix a pre-existing bug in `deploy-ftp.sh` that nests the app under an extra redundant `kobo-anki/` directory.

**Architecture:** Every binary (`kobo-vocab`, `cmd/fbink`, `cmd/server`, `cmd/apkgimport`) resolves its config/asset paths as hardcoded literals relative to the process's working directory, which is always the app root (every start script `cd`s there first). This plan changes those literals to point under `config/`, updates the start scripts' `-conf` arguments and log redirects to match, fixes `deploy-ftp.sh` to stage and upload the new layout directly as the app root (no double nesting), and updates the README's tree diagram and adds a manual migration note for the existing on-device install.

**Tech Stack:** Go (stdlib only for the touched code), POSIX shell (`start*.sh`, `deploy-ftp.sh`), Markdown (README).

**Spec:** `docs/superpowers/specs/2026-09-02-on-device-layout-design.md`

## Global Constraints

- New on-device folders are named `config/` and `logs/` (not `conf/`/`config-and-data/` etc).
- The four `anki-*.conf` files keep their current filenames — only their directory changes.
- No automated on-device migration code. The existing install gets a documented manual migration step only.
- `bin/`, `templates/`, and `words/` do not move. `words/` is never touched by `deploy-ftp.sh`.
- `cmd/fbink`'s second, undocumented `anki-fbink.conf` fallback path (inside `words/`) is left untouched.
- The dead `-count-new`/`auto_parse` logic in the start scripts is not removed in this plan — only the *paths* those lines reference are updated. Removing the dead logic itself is separately deferred, already-known-broken work.

---

## Task 1: Update Go path defaults across all four binaries

**Files:**
- Modify: `kobo-vocab/main.go:19`
- Modify: `cmd/fbink/main.go:458`, `cmd/fbink/main.go:1316`, `cmd/fbink/main.go:1321`
- Modify: `cmd/server/main.go:439`, `cmd/server/main.go:443`
- Modify: `cmd/apkgimport/main.go:55`

**Interfaces:** None — this is a pure literal-string change. No function signatures, exported names, or types are touched. Nothing here is a dependency for any other task in this plan; Tasks 2-4 are edits to different files with no shared interface.

This task has no new behavior to test with a unit test: it relocates hardcoded path literals that no existing test constructs or depends on (confirmed during design: every test that touches config loading passes an explicit path, e.g. `loadConfig("")` or a `t.TempDir()`-built path, never the bare literal default). The regression check is the existing test suite plus a real build.

- [ ] **Step 1: Edit `kobo-vocab/main.go`**

Change line 19 from:
```go
	confPath := flag.String("conf", "anki-mywords.conf", "path to config file")
```
to:
```go
	confPath := flag.String("conf", "config/anki-mywords.conf", "path to config file")
```

- [ ] **Step 2: Edit `cmd/fbink/main.go`**

Change line 458 from:
```go
	paths := []string{"./anki-fbink.conf", filepath.Join(dataDir, "anki-fbink.conf")}
```
to:
```go
	paths := []string{"./config/anki-fbink.conf", filepath.Join(dataDir, "anki-fbink.conf")}
```

Change line 1316 from:
```go
	coreCfg := core.LoadCoreConfig("anki-core.conf")
```
to:
```go
	coreCfg := core.LoadCoreConfig("config/anki-core.conf")
```

Change line 1321 from:
```go
	if css, err := os.ReadFile("cards.css"); err == nil {
```
to:
```go
	if css, err := os.ReadFile("config/cards.css"); err == nil {
```

- [ ] **Step 3: Edit `cmd/server/main.go`**

Change line 439 from:
```go
	coreCfg := core.LoadCoreConfig("anki-core.conf")
```
to:
```go
	coreCfg := core.LoadCoreConfig("config/anki-core.conf")
```

Change line 443 from:
```go
	if css, err := os.ReadFile("cards.css"); err == nil {
```
to:
```go
	if css, err := os.ReadFile("config/cards.css"); err == nil {
```

- [ ] **Step 4: Edit `cmd/apkgimport/main.go`**

Change line 55 from:
```go
		splitTagsRaw = loadSplitTagsConfig("anki-apkgimport.conf")
```
to:
```go
		splitTagsRaw = loadSplitTagsConfig("config/anki-apkgimport.conf")
```

- [ ] **Step 5: Build and test**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: all packages build; `go vet` reports nothing; every test package reports `ok` (same as before this change — no test depends on these literals).

- [ ] **Step 6: Commit**

```bash
git add kobo-vocab/main.go cmd/fbink/main.go cmd/server/main.go cmd/apkgimport/main.go
git commit -m "$(cat <<'EOF'
Point config/asset path defaults at config/ for the on-device layout cleanup

kobo-vocab's confDir-relative resolution (cacert.pem, APIs/*.json) follows
the -conf flag's new default automatically; only the literal default paths
in each binary needed to change.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019BbtGXAmBvB98XUTdqbbip
EOF
)"
```

---

## Task 2: Update the start scripts' config and log paths

**Files:**
- Modify: `start.sh`
- Modify: `start-debug.sh`
- Modify: `start-fbink.sh`
- Modify: `start-server.sh`
- Modify: `start-vocab.sh`

**Interfaces:** Consumes: the new `config/anki-mywords.conf` default path from Task 1 is not relied upon here — every `-conf` argument below is passed explicitly, so this task is independent of Task 1's completion (both can be reviewed in either order, but Task 1 should land first so a deploy after Task 2 alone doesn't reference a binary that still expects the old default in code paths that omit `-conf`... there are none here, every invocation passes `-conf` explicitly, so order truly doesn't matter). Produces: nothing consumed by later tasks except that Task 3's `deploy-ftp.sh` must ship these updated scripts unchanged (already true — `deploy-ftp.sh` copies them by filename, not content).

These are shell scripts with no automated test suite in this repo; verification is `sh -n` (syntax check) plus a careful diff read, with the real functional check deferred to Task 5's on-device test.

- [ ] **Step 1: Edit `start.sh`**

Change:
```sh
cd "$(dirname "$0")"
LOG="$(pwd)/kobo-anki.log"
echo "=== start $(date) ===" >>$LOG
```
to:
```sh
cd "$(dirname "$0")"
mkdir -p logs
LOG="$(pwd)/logs/kobo-anki.log"
echo "=== start $(date) ===" >>$LOG
```

Change:
```sh
AUTO_PARSE=false
if [ -f anki-mywords.conf ]; then
    val=$(grep -E '^[[:space:]]*auto_parse[[:space:]]*=' anki-mywords.conf | tail -1 | cut -d= -f2 | tr -d '[:space:]')
    [ "$val" = "true" ] && AUTO_PARSE=true
fi
```
to:
```sh
AUTO_PARSE=false
if [ -f config/anki-mywords.conf ]; then
    val=$(grep -E '^[[:space:]]*auto_parse[[:space:]]*=' config/anki-mywords.conf | tail -1 | cut -d= -f2 | tr -d '[:space:]')
    [ "$val" = "true" ] && AUTO_PARSE=true
fi
```

Change:
```sh
    NEW_COUNT=$(./bin/kobo-vocab -conf anki-mywords.conf -count-new 2>>$LOG)
```
to:
```sh
    NEW_COUNT=$(./bin/kobo-vocab -conf config/anki-mywords.conf -count-new 2>>$LOG)
```

Change:
```sh
            ./bin/kobo-vocab -conf anki-mywords.conf >>$LOG 2>&1
```
to:
```sh
            ./bin/kobo-vocab -conf config/anki-mywords.conf >>$LOG 2>&1
```

- [ ] **Step 2: Edit `start-debug.sh`**

Change:
```sh
cd "$(dirname "$0")"
LOG=kobo-anki.log
echo "=== start-debug $(date) ===" >>$LOG
```
to:
```sh
cd "$(dirname "$0")"
mkdir -p logs
LOG=logs/kobo-anki.log
echo "=== start-debug $(date) ===" >>$LOG
```

Change:
```sh
./bin/kobo-vocab -conf anki-mywords.conf 2>&1 | tee -a $LOG
```
to:
```sh
./bin/kobo-vocab -conf config/anki-mywords.conf 2>&1 | tee -a $LOG
```

- [ ] **Step 3: Edit `start-fbink.sh`**

Change:
```sh
#!/bin/sh
cd "$(dirname "$0")"

# --- Translate vocabulary (runs in background so the UI isn't blocked) ---
# Mirrors start.sh's auto_parse gating - kept here since this script (unlike
# start.sh) never touches Nickel at all.
AUTO_PARSE=false
if [ -f anki-mywords.conf ]; then
    val=$(grep -E '^[[:space:]]*auto_parse[[:space:]]*=' anki-mywords.conf | tail -1 | cut -d= -f2 | tr -d '[:space:]')
    [ "$val" = "true" ] && AUTO_PARSE=true
fi
```
to:
```sh
#!/bin/sh
cd "$(dirname "$0")"
mkdir -p logs

# --- Translate vocabulary (runs in background so the UI isn't blocked) ---
# Mirrors start.sh's auto_parse gating - kept here since this script (unlike
# start.sh) never touches Nickel at all.
AUTO_PARSE=false
if [ -f config/anki-mywords.conf ]; then
    val=$(grep -E '^[[:space:]]*auto_parse[[:space:]]*=' config/anki-mywords.conf | tail -1 | cut -d= -f2 | tr -d '[:space:]')
    [ "$val" = "true" ] && AUTO_PARSE=true
fi
```

Change:
```sh
    NEW_COUNT=$(./bin/kobo-vocab -conf anki-mywords.conf -count-new 2>>vocab.log)
```
to:
```sh
    NEW_COUNT=$(./bin/kobo-vocab -conf config/anki-mywords.conf -count-new 2>>logs/vocab.log)
```

Change:
```sh
            ./bin/kobo-vocab -conf anki-mywords.conf >>vocab.log 2>&1
```
to:
```sh
            ./bin/kobo-vocab -conf config/anki-mywords.conf >>logs/vocab.log 2>&1
```

- [ ] **Step 4: Edit `start-server.sh`**

Change:
```sh
#!/bin/sh
cd "$(dirname "$0")"

# Translate new vocabulary words
./bin/kobo-vocab -conf anki-mywords.conf 2>>vocab.log
```
to:
```sh
#!/bin/sh
cd "$(dirname "$0")"
mkdir -p logs

# Translate new vocabulary words
./bin/kobo-vocab -conf config/anki-mywords.conf 2>>logs/vocab.log
```

- [ ] **Step 5: Edit `start-vocab.sh`**

Change:
```sh
cd "$(dirname "$0")"
LOG=vocab.log
QNDB_LOG=qndb.log
```
to:
```sh
cd "$(dirname "$0")"
mkdir -p logs
LOG=logs/vocab.log
QNDB_LOG=logs/qndb.log
```

Change:
```sh
NEW_COUNT=$(./bin/kobo-vocab -conf anki-mywords.conf -count-new 2>>$LOG)
```
to:
```sh
NEW_COUNT=$(./bin/kobo-vocab -conf config/anki-mywords.conf -count-new 2>>$LOG)
```

Change:
```sh
./bin/kobo-vocab -conf anki-mywords.conf >>"$LOG" 2>&1 &
```
to:
```sh
./bin/kobo-vocab -conf config/anki-mywords.conf >>"$LOG" 2>&1 &
```

- [ ] **Step 6: Syntax-check all five scripts**

Run: `sh -n start.sh && sh -n start-debug.sh && sh -n start-fbink.sh && sh -n start-server.sh && sh -n start-vocab.sh && echo ALL_OK`
Expected: `ALL_OK` with no syntax errors printed.

- [ ] **Step 7: Diff-review**

Run: `git diff start.sh start-debug.sh start-fbink.sh start-server.sh start-vocab.sh`
Expected: only the lines shown above changed — no other lines touched (in particular, the `-count-new`/`auto_parse` *logic* itself must be untouched, only the paths it references).

- [ ] **Step 8: Commit**

```bash
git add start.sh start-debug.sh start-fbink.sh start-server.sh start-vocab.sh
git commit -m "$(cat <<'EOF'
Point start scripts at config/anki-mywords.conf and logs/ for the layout cleanup

Path-only change: every -conf argument and log redirect now targets the new
config/ and logs/ subfolders. The dead -count-new/auto_parse logic itself is
untouched here - that cleanup is separately deferred.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019BbtGXAmBvB98XUTdqbbip
EOF
)"
```

---

## Task 3: Fix `deploy-ftp.sh` staging layout and the double-nesting bug

**Files:**
- Modify: `deploy-ftp.sh`

**Interfaces:** Consumes: the file names produced by `build.sh` (`bin/kobo-anki-fbink`, `bin/kobo-anki-apkgimport`, `bin/kobo-anki-server`, `bin/kobo-vocab`, optionally `bin/fbink`) and the repo-root files (`anki-core.conf`, `anki-fbink.conf`, `anki-mywords.conf`, optionally `anki-apkgimport.conf`, `cacert.pem`, `cards.css`, `templates/*.html`, optionally `french.csv`, optionally `APIs/*.json`) — all unchanged from before. Produces: the on-device tree that Task 2's scripts and Task 1's binaries expect to find at their own root (`config/`, `templates/`, `bin/`, `start*.sh` all directly under `.adds/$KOBO_FOLDER/`, no wrapping `kobo-anki/` subfolder).

- [ ] **Step 1: Update the top-of-file comment**

Change:
```sh
# Never touches words/ (your live flashcard progress) or logs on-device -
# only the files that ship in the tarball (bin/, APIs/, templates/,
# start*.sh, *.conf, french.csv, cacert.pem, cards.css) get uploaded/overwritten.
```
to:
```sh
# Never touches words/ (your live flashcard progress) or logs/ on-device -
# only the files that ship in the tarball (bin/, templates/, config/,
# samples/, start*.sh) get uploaded/overwritten.
```

- [ ] **Step 2: Fix the deploy-target echo line**

Change:
```sh
echo "Deploying to ftp://$KOBO_USER@$KOBO_HOST:$KOBO_PORT/.adds/$KOBO_FOLDER/kobo-anki/"
```
to:
```sh
echo "Deploying to ftp://$KOBO_USER@$KOBO_HOST:$KOBO_PORT/.adds/$KOBO_FOLDER/"
```

- [ ] **Step 3: Restructure the staging block**

Change:
```sh
mkdir -p "$STAGE/kobo-anki/bin" "$STAGE/kobo-anki/templates"
cp bin/kobo-anki-fbink bin/kobo-anki-apkgimport bin/kobo-anki-server bin/kobo-vocab "$STAGE/kobo-anki/bin/"
# fbink itself is only uploaded if you already have one built/placed in
# bin/ locally - it needs an image-support-enabled build (see README), and
# this script won't silently overwrite a working device copy with nothing.
[ -f bin/fbink ] && cp bin/fbink "$STAGE/kobo-anki/bin/"
cp start.sh start-debug.sh start-fbink.sh start-server.sh start-vocab.sh "$STAGE/kobo-anki/"
cp anki-core.conf anki-fbink.conf anki-mywords.conf "$STAGE/kobo-anki/"
# apkgimport's split_tags config - optional, most imports don't use it.
[ -f anki-apkgimport.conf ] && cp anki-apkgimport.conf "$STAGE/kobo-anki/"
# CA bundle for the network providers' TLS verification - some Kobo
# firmware ships with no system CA store at all (see ca_cert in
# anki-mywords.conf.example), so this is deployed unconditionally.
cp cacert.pem "$STAGE/kobo-anki/"
cp templates/*.html "$STAGE/kobo-anki/templates/"
[ -f french.csv ] && cp french.csv "$STAGE/kobo-anki/"
# Shared styling for html/apkg cards (core.InjectIntoHead) - not overwritten
# blindly by anything else, safe to redeploy after editing it.
[ -f cards.css ] && cp cards.css "$STAGE/kobo-anki/"
# APIs/ (provider credentials, e.g. lexicala.json) - gitignored and only
# deployed if you've actually populated it; providers=lexicala on-device
# needs this to work at all.
if [ -d APIs ] && ls APIs/*.json >/dev/null 2>&1; then
    mkdir -p "$STAGE/kobo-anki/APIs"
    cp APIs/*.json "$STAGE/kobo-anki/APIs/"
fi
```
to:
```sh
mkdir -p "$STAGE/bin" "$STAGE/templates" "$STAGE/config"
cp bin/kobo-anki-fbink bin/kobo-anki-apkgimport bin/kobo-anki-server bin/kobo-vocab "$STAGE/bin/"
# fbink itself is only uploaded if you already have one built/placed in
# bin/ locally - it needs an image-support-enabled build (see README), and
# this script won't silently overwrite a working device copy with nothing.
[ -f bin/fbink ] && cp bin/fbink "$STAGE/bin/"
cp start.sh start-debug.sh start-fbink.sh start-server.sh start-vocab.sh "$STAGE/"
cp anki-core.conf anki-fbink.conf anki-mywords.conf "$STAGE/config/"
# apkgimport's split_tags config - optional, most imports don't use it.
[ -f anki-apkgimport.conf ] && cp anki-apkgimport.conf "$STAGE/config/"
# CA bundle for the network providers' TLS verification - some Kobo
# firmware ships with no system CA store at all (see ca_cert in
# anki-mywords.conf.example), so this is deployed unconditionally.
cp cacert.pem "$STAGE/config/"
cp templates/*.html "$STAGE/templates/"
if [ -f french.csv ]; then
    mkdir -p "$STAGE/samples"
    cp french.csv "$STAGE/samples/"
fi
# Shared styling for html/apkg cards (core.InjectIntoHead) - not overwritten
# blindly by anything else, safe to redeploy after editing it.
[ -f cards.css ] && cp cards.css "$STAGE/config/"
# APIs/ (provider credentials, e.g. lexicala.json) - gitignored and only
# deployed if you've actually populated it; providers=lexicala on-device
# needs this to work at all.
if [ -d APIs ] && ls APIs/*.json >/dev/null 2>&1; then
    mkdir -p "$STAGE/config/APIs"
    cp APIs/*.json "$STAGE/config/APIs/"
fi
```

- [ ] **Step 4: Fix the upload loop to stop wrapping in `kobo-anki/`**

Change:
```sh
FTP_BASE="ftp://$KOBO_HOST:$KOBO_PORT/.adds/$KOBO_FOLDER"
cd "$STAGE"
find kobo-anki -type f | while read -r f; do
    echo "  $f"
    curl -sf --max-time 30 -T "$f" --ftp-create-dirs "$FTP_BASE/$f" --user "$KOBO_USER:" || {
        echo "FAILED: $f" >&2
        exit 1
    }
done
```
to:
```sh
FTP_BASE="ftp://$KOBO_HOST:$KOBO_PORT/.adds/$KOBO_FOLDER"
cd "$STAGE"
find . -type f | while read -r f; do
    f="${f#./}"
    echo "  $f"
    curl -sf --max-time 30 -T "$f" --ftp-create-dirs "$FTP_BASE/$f" --user "$KOBO_USER:" || {
        echo "FAILED: $f" >&2
        exit 1
    }
done
```

- [ ] **Step 5: Syntax-check**

Run: `sh -n deploy-ftp.sh && echo OK`
Expected: `OK`.

- [ ] **Step 6: Dry-run the staging logic without a real device**

This verifies the staged tree shape without needing network access to a Kobo. Run:
```sh
sh -c '
set -e
cd /home/jonas/Hacking/kobo-anki
STAGE=$(mktemp -d)
mkdir -p "$STAGE/bin" "$STAGE/templates" "$STAGE/config"
touch bin/kobo-anki-fbink bin/kobo-anki-apkgimport bin/kobo-anki-server bin/kobo-vocab 2>/dev/null || true
cp bin/kobo-anki-fbink bin/kobo-anki-apkgimport bin/kobo-anki-server bin/kobo-vocab "$STAGE/bin/" 2>/dev/null || echo "(binaries not built - skip in this dry run)"
cp start.sh start-debug.sh start-fbink.sh start-server.sh start-vocab.sh "$STAGE/"
cp anki-core.conf anki-fbink.conf anki-mywords.conf "$STAGE/config/" 2>/dev/null || echo "(real .conf files not present locally - expected on a fresh clone)"
cp templates/*.html "$STAGE/templates/" 2>/dev/null || true
find "$STAGE" -type f | sed "s|$STAGE/||" | sort
rm -rf "$STAGE"
'
```
Expected output: a flat listing with `start.sh`, `start-debug.sh`, `start-fbink.sh`, `start-server.sh`, `start-vocab.sh` at the top level, `bin/...` and `templates/...` as subpaths, and `config/...` present if you have real (gitignored) `.conf` files checked out locally — no `kobo-anki/` prefix on any line. If your repo has no real `.conf` files yet (fresh clone, only `.example` files), the config copy lines will report the expected "not present" message rather than fail the whole dry run.

- [ ] **Step 7: Commit**

```bash
git add deploy-ftp.sh
git commit -m "$(cat <<'EOF'
Fix deploy-ftp.sh's redundant kobo-anki/kobo-anki nesting and stage config/logs/samples

Files now stage as the app-root contents directly, so $KOBO_FOLDER (default
"kobo-anki") is genuinely the on-device root, matching the README's own
NickelMenu example. Config files, cacert.pem, cards.css, and APIs/ move
under config/; french.csv moves under samples/.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019BbtGXAmBvB98XUTdqbbip
EOF
)"
```

---

## Task 4: Update the README

**Files:**
- Modify: `README.md`

**Interfaces:** None — documentation only, no code dependency.

- [ ] **Step 1: Replace the on-device tree diagram**

Change (README.md, the block currently at lines 51-70):
```
/mnt/onboard/.adds/kobo-anki/
├── bin/
│   ├── kobo-anki-fbink
│   ├── kobo-anki-apkgimport
│   ├── kobo-anki-server
│   ├── kobo-vocab
│   └── fbink              ← from FBInk releases
├── start.sh
├── anki-core.conf          ← copy from the .example files
├── anki-fbink.conf
├── anki-mywords.conf
├── cacert.pem              ← needed for the online providers, see below
├── cards.css               ← shared styling for html/apkg cards, see below
├── dict/                   ← dicthtml-*.zip dictionaries
├── APIs/                   ← provider credentials, only if you use them
├── templates/              ← only needed for server mode
├── french.csv              ← sample deck; copy into words/ to try it
└── words/                  ← flashcard CSVs go here
```
to:
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

- [ ] **Step 2: Add the migration note**

Insert a new subsection immediately after the NickelMenu config section (after the closing ` ``` ` that follows `menu_item:main:Kobo Anki:cmd_spawn:quiet:/mnt/onboard/.adds/kobo-anki/start.sh`, before the `## Configuration` heading):

```markdown

### Upgrading an existing install

If you already have kobo-anki installed under the old flat layout (every
file loose at the app-folder root), move things into place by hand once,
over SSH or an FTP client:

```
cd /mnt/onboard/.adds/kobo-anki
mkdir -p config logs samples
mv anki-core.conf anki-fbink.conf anki-mywords.conf cacert.pem cards.css config/
mv APIs config/
[ -f french.csv ] && mv french.csv samples/
rm -f *.log
```

Re-deploy after that so the binaries and start scripts land at their new
locations too.
```

- [ ] **Step 3: Update the `cacert.pem` cross-reference**

Change:
```
`pons`/`lexicala`/`chatgpt` all need working HTTPS, which some Kobo firmware can't do out
of the box - certain devices ship with no system CA store at all, so every TLS request fails
with `x509: certificate signed by unknown authority` regardless of provider. `cacert.pem`
(repo root — copy it to the device alongside the other files listed under
[Installation](#installation)) is a standard CA bundle appended to whatever trust store the
OS provides, fixing that - see `ca_cert` in `anki-mywords.conf.example`.
```
to:
```
`pons`/`lexicala`/`chatgpt` all need working HTTPS, which some Kobo firmware can't do out
of the box - certain devices ship with no system CA store at all, so every TLS request fails
with `x509: certificate signed by unknown authority` regardless of provider. `cacert.pem`
(repo root — `deploy-ftp.sh` installs it under `config/` on the device, see
[Installation](#installation)) is a standard CA bundle appended to whatever trust store the
OS provides, fixing that - see `ca_cert` in `anki-mywords.conf.example`.
```

- [ ] **Step 4: Update the `french.csv` cross-reference**

Change:
```
`french.csv` in the repo root is a small ready-to-use sample deck — copy it into your
`words/` directory to try the app before setting up any dictionaries.
```
to:
```
`french.csv` in the repo root is a small ready-to-use sample deck — `deploy-ftp.sh` installs
it under `samples/` on the device; copy it into `words/` to try the app before setting up any
dictionaries.
```

- [ ] **Step 5: Review the rendered diff**

Run: `git diff README.md`
Expected: exactly the four changes above, nothing else — in particular the `APIs/pons.json` etc. mentions in the provider table (config-key values, still relative to `anki-mywords.conf`'s own directory) are unchanged, since they correctly describe the config key's value rather than an absolute on-device path.

- [ ] **Step 6: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
Document the config/logs/samples on-device layout and add an upgrade note

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_019BbtGXAmBvB98XUTdqbbip
EOF
)"
```

---

## Task 5: Full verification (Go suite + real-device deploy)

**Files:** None modified — verification only.

**Interfaces:** Consumes the committed results of Tasks 1-4.

- [ ] **Step 1: Full Go verification**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: builds clean, vet clean, all packages `ok`.

- [ ] **Step 2: Check the physical device is reachable**

Per the `kobo-deploy-workflow` memory convention (ping before deploy), run:
```sh
ping -c 1 -W 2 "$KOBO_HOST"
```
(substituting the actual host from `deploy.conf`, or ask the user for it/to power on and connect the device if there's no reply). If unreachable, stop here and report that back rather than proceeding — do not guess at connectivity.

- [ ] **Step 3: Deploy**

Run: `./deploy-ftp.sh` (reads `deploy.conf`) or `./deploy-ftp.sh <kobo-ip>`.
Expected: every file uploads without a `FAILED:` line, and the final "Done." message prints.

- [ ] **Step 4: Confirm the on-device tree**

Over SSH (see `kobo-ssh-access` memory for how to get a shell from this sandbox), run on the device:
```sh
find /mnt/onboard/.adds/kobo-anki -maxdepth 2 | sort
```
Expected: matches the Task 4 tree diagram exactly — `start.sh` (and the other four start scripts) directly under `.adds/kobo-anki/`, `config/` containing the four `.conf` files plus `cacert.pem`, `cards.css`, and `APIs/` (if populated), `samples/french.csv` if present, no top-level `kobo-anki/` subfolder, and no stray `.conf`/`.pem`/`.css` files left over at the root from before the migration note's manual cleanup (confirm the user has run that, or run it yourself over the same SSH session if asked to).

- [ ] **Step 5: Smoke-test each entry point**

Re-run the NickelMenu entry (or launch `start.sh` directly over SSH) and confirm:
- The e-ink UI (`kobo-anki-fbink`) launches and shows a deck.
- If `auto_parse=true` is set in `config/anki-mywords.conf`, the startup vocab lookup still runs (writing to `logs/vocab.log`, not the old root `vocab.log`).
- `start-server.sh` (if used) serves the web UI and picks up `config/cards.css` styling.
- `start-vocab.sh` (if used) opens its NickelDBus dialog and completes a lookup, writing to `logs/vocab.log` and `logs/qndb.log`.

Report results back rather than assuming success — this step requires the real device and cannot be verified from the sandbox alone beyond what SSH access allows.

No commit for this task (verification only, no file changes expected). If any step surfaces a problem, fix it as a follow-up to the relevant task above (Task 1-4) with its own small commit, then re-run verification from Step 1.
