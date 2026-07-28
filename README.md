# syntee-launcher

Bootstrap updater for the SynteeHelper desktop client (Windows).

`syntee-launcher.exe` is the single entry point of a SynteeHelper install:
shortcuts, the Start Menu, and the `HKCU\...\Run` autostart key all point at
this binary, **never** at `SynteeHelper.exe`. On every launch it:

1. **Crash-loop recovery** — if the current version is in `bad_versions`,
   restores the most recent backup before doing anything else.
2. **Check** — asks the Syntee-Gateway whether a newer build exists for the
   configured channel. Offline-first: a failed check never blocks launch.
3. **Apply** — downloads the zip (with HTTP Range resume), verifies SHA256,
   backs up `current/` → `backups/<old>/`, atomically swaps the new build in.
   - `force=true` (or `current < min_version`) → **blocking**: a MessageBox
     shows progress, the update is applied before the client starts.
   - `force=false` → **background**: the zip is staged in `staging/` and
     applied on the next launch (after `app.quit()` from the in-app updater).
4. **Run** — spawns `current/SynteeHelper.exe`, waits for it to exit, loops.
   A crash-marker (`.crash_marker`) written before spawn and deleted by the
   client on ready-state lets the launcher detect crashes and, after 3
   consecutive crash-loops, restore the backup and mark the version bad.

## Install layout

```
<install_dir>/
├── syntee-launcher.exe          ← this binary
├── launcher.json                ← config: channel, current_version, bad_versions, ...
├── syntee-launcher.log          ← diagnostic log (created on first run)
├── current/                     ← active client build
│   ├── SynteeHelper.exe
│   └── ...
├── backups/                     ← previous versions (for rollback)
│   └── 0.1.0_<unix_ts>/
└── staging/                     ← scratch: download + unzip before atomic swap
    ├── update.zip
    └── update.json              ← cached manifest (so apply knows the version)
```

## `launcher.json`

```json
{
  "update_server": "https://gateway.syntee.dev/api/public/updates",
  "channel": "stable",
  "current_version": "0.1.0",
  "pinned_version": null,
  "bad_versions": [],
  "crash_count": 0,
  "max_crash_retries": 3,
  "check_timeout_sec": 5,
  "download_timeout_sec": 0,
  "client_exe": "SynteeHelper.exe",
  "ready_marker": ".ready"
}
```

The file lives next to the binary so the whole install is self-contained — a
clean uninstall is just "delete the folder". A UTF-8 BOM is tolerated.

## Build

```bash
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui" -o syntee-launcher.exe .
```

`-H windowsgui` suppresses the console window (the launcher is a GUI-less
background process; user-facing messages go through `MessageBox`).

## Gateway endpoints

The launcher talks to three public endpoints on Syntee-Gateway (no auth —
integrity is guaranteed by SHA256 in the manifest):

- `GET /api/public/updates/check?channel=stable&current=X.Y.Z` — 204 (up to
  date) or 200 with a `ReleaseManifest` JSON body.
- `GET /api/public/updates/download/{channel}/{version}` — the zip artifact
  (supports HTTP Range for resume).
- `GET /api/public/updates/changelog` — release history (used by the in-app
  changelog tab, not the launcher itself).

## Testing

The launcher is end-to-end testable against a running gateway with a fake
client binary — see the test plan in the parent project's planning docs. Key
scenarios covered:

| Scenario | Expected behaviour |
|---|---|
| No active release on server | 204, launch current client |
| Non-force update available | Background-download + apply on next launch |
| Force update available | Blocking download with MessageBox + apply before launch |
| Client crashes 3× in a row | Restore latest backup, mark version bad |
| Bad version with no backup | Fatal MessageBox: "please reinstall manually" |
| Server unreachable | Launch current client (offline-first) |
