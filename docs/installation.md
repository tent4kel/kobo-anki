# Installation

## Requirements

- A Kobo e-reader (tested on Clara BW)
- [NickelMenu](https://pgaskin.net/NickelMenu/), for launching the app from the device's menu
- [FBInk](https://github.com/NiLuJe/FBInk) (GPL-3.0) — a prebuilt binary is included in release packages

## Building from source

Requires Go 1.22+.

```sh
git clone https://github.com/tent4kel/kobo-anki.git
cd kobo-anki
sh build.sh
```

This cross-compiles four ARM binaries into `bin/`:

- `kobo-anki-fbink` — e-ink flashcard UI
- `kobo-anki-server` — web flashcard UI
- `kobo-vocab` — vocabulary extractor
- `kobo-anki-apkgimport` — Anki `.apkg` importer

`build.sh` does not build `fbink` itself; download a build for your device from the
[FBInk releases page](https://github.com/NiLuJe/FBInk/releases) and place it at `bin/fbink`.

## Installing on the device

Copy the `.example` config files and rename them (drop the `.example` suffix), then edit
each to match your setup — see the per-module docs linked from the [README](../README.md)
for what each file's settings do.

Then copy the files onto the device (however you prefer — USB mass storage, FTP, `scp`
if your device has an SSH server enabled) into `.adds/`, giving you this layout:

```
/mnt/onboard/.adds/kobo-anki/
├── bin/
│   ├── kobo-anki-fbink
│   ├── kobo-anki-apkgimport
│   ├── kobo-anki-server
│   ├── kobo-vocab
│   └── fbink
├── start.sh
├── start-debug.sh
├── start-fbink.sh
├── start-server.sh
├── start-vocab.sh
├── start-apkg.sh
├── config/
│   ├── anki-core.conf      ← copied from the .example files
│   ├── anki-fbink.conf
│   ├── anki-mywords.conf
│   ├── cacert.pem          ← needed for the online dictionary providers
│   ├── cards.css           ← shared styling for html/apkg cards
│   └── APIs/               ← provider credentials, only if you use them
├── dict/                   ← optional: dicthtml-*.zip dictionaries kept beside the app
├── templates/              ← only needed for server mode
├── samples/
│   └── french.csv          ← sample deck; copy into words/ to try the app
├── logs/                   ← created automatically at first run
├── apkg/                   ← drop .apkg files here for start-apkg.sh to import
└── words/                  ← flashcard CSVs live here
```

### NickelMenu

Add these lines to your NickelMenu configuration file:

```
menu_item:main:Kobo Anki:cmd_spawn:quiet:/mnt/onboard/.adds/kobo-anki/start.sh
menu_item:main:Update Vocabulary:cmd_spawn:quiet:/mnt/onboard/.adds/kobo-anki/start-vocab.sh
menu_item:main:Import Apkg:cmd_spawn:quiet:/mnt/onboard/.adds/kobo-anki/start-apkg.sh
```

### Upgrading from an older, flat layout

Installs from before the `config/`/`samples/`/`logs/` layout existed had every file loose
at the app-folder root. To move to the current layout, on the device:

```sh
cd /mnt/onboard/.adds/kobo-anki
mkdir -p config logs samples
mv anki-core.conf anki-fbink.conf anki-mywords.conf cacert.pem cards.css config/
[ -f anki-apkgimport.conf ] && mv anki-apkgimport.conf config/
[ -d APIs ] && mv APIs config/
[ -f french.csv ] && mv french.csv samples/
rm -f *.log
```

Then copy over the newer binaries and start scripts.

## Launcher scripts

- **start.sh** — Production launcher for NickelMenu. Kills Nickel, runs the app with crash
  recovery (up to 5 restarts), reboots on exit. Restarting Nickel in place instead of
  rebooting is unreliable on at least some hardware; `start.sh` reboots instead. The
  hardware watchdog needs no handling here — it's kept fed by an in-kernel thread
  regardless of what userspace does.
- **start-debug.sh** — Debug launcher for an SSH session. Same Nickel handling, but does
  not reboot on exit (run `reboot` manually when done).
- **start-fbink.sh** — Minimal launcher with no Nickel management, for testing alongside
  a running Nickel.
- **start-server.sh** — Starts the web server mode.
- **start-vocab.sh** — Standalone vocabulary lookup, runs alongside Nickel. Shows live
  progress (including dictionary-load percentage) via a NickelDBus modal dialog with a
  Cancel button; runs silently if NickelDBus isn't available.
- **start-apkg.sh** — Standalone apkg importer, runs alongside Nickel. Scans `apkg/` for
  `.apkg` files and imports each into a deck named after the file; successfully imported
  files move to `apkg/imported/`.

## Checking your configuration

None of the config files fail to load on a bad setting — a setting that can't be used
keeps its default, and the rest of the file still applies. Anything ignored is reported
with its file and line:

```
anki-mywords.conf:14: unknown setting "dict_example" - ignored (typo, or does it belong in another conf file?)
anki-mywords.conf:22: max_senses="abc" is not a whole number - keeping the default
```

`kobo-vocab -print-config` prints the effective settings after defaults and section
overrides are merged — useful when a setting doesn't seem to be taking effect.

## Troubleshooting: NickelMenu disappears after a crash loop

If Nickel crashes repeatedly shortly after boot with a given
`/usr/local/Kobo/imageformats/*.so` plugin loaded — the mechanism NickelMenu and similar
patches use to inject themselves — some Kobo firmware renames that plugin from `.so` to
`.so.failsafe`, disabling it, to break the boot-crash loop. The device itself isn't
bricked; Nickel boots fine once the offending plugin stops loading, but NickelMenu's own
entries vanish as a side effect. Fix: rename `*.so.failsafe` back to `*.so` and reboot.
