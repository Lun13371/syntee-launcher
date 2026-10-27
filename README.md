# syntee-launcher

Bootstrap updater for the SynteeHelper desktop client (Windows).

`syntee-launcher.exe` installs, updates and repairs SynteeHelper. Desktop,
Start Menu and autostart entries launch `current/Syntee Helper.exe` directly.
The launcher is not a permanent supervisor of directly launched clients.

Its lifecycle is:

1. Recover interrupted installation transactions and reconcile installed
   version metadata before checking for updates.
2. Check the gateway for a newer build. A failed check leaves the installed
   client available; known-bad releases must not be automatically reinstalled.
3. Download with persistent partial-file resume, verify size and SHA256,
   validate/extract into staging, then replace the old client. The old build is
   held only temporarily during the swap and is not retained as a version
   backup. GUI updates are applied after user acceptance; forced updates do not
   require a decision. Headless mode applies forced updates only.
4. Launch the client independently of launcher lifetime. Startup monitoring
   can detect observed early failures, but complete crash-loop detection on
   direct shortcut launches requires client-side integration. Missing ready
   markers alone are not proof that a running client crashed.

## Install layout

```
<install_dir>/
├── syntee-launcher.exe          ← this binary
├── launcher.json                ← config: channel, current_version, ...
├── syntee-launcher.log          ← diagnostic log (created on first run)
├── current/                     ← active client build
│   ├── Syntee Helper.exe
│   └── ...
└── staging/                     ← scratch: download + swap workspace
    ├── update.zip.part           ← persistent incomplete download
    ├── update.zip.part.json      ← artifact identity and resume validators
    ├── update.zip                ← fully verified archive only
    ├── new/                      ← validated package awaiting promotion
    └── apply.json                ← interrupted promotion recovery journal
```

## `launcher.json`

```json
{
  "update_server": "https://syntee.fun/api/public/updates",
  "channel": "stable",
  "current_version": "0.1.0",
  "pinned_version": null,
  "bad_versions": [],
  "crash_count": 0,
  "max_crash_retries": 3,
  "check_timeout_sec": 5,
  "download_timeout_sec": 0,
  "client_exe": "Syntee Helper.exe",
  "ready_marker": ".ready"
}
```

The file lives next to the binary so the whole install is self-contained — a
clean uninstall is just "delete the folder". A UTF-8 BOM is tolerated.

## Self-healing

Every launch (before any update check) the launcher repairs its own install:

1. **`update_server` migration** — if `launcher.json` points at anything other
   than the baked-in `DefaultUpdateServer` (e.g. the old raw IP from pre-domain
   builds), it is forcibly rewritten to the baked default and saved. The JSON
   wins over the baked default, so without this old installs would poll a dead
   server forever.
2. **`current/updater.exe` sync** — the running binary checks itself against
   `current/updater.exe` on every launch (not only after applying an update),
   replacing it only if different via flushed temporary file and Windows
   replace/write-through semantics. The desktop client spawns `updater.exe` for its
   in-app "restart to update" flow, so it must always be the freshest build.
3. **Self-install** — running the binary from anywhere else (e.g. a fresh
   download from the website) overwrites `<install_dir>/syntee-launcher.exe`
   with itself and relaunches from there, so a manual re-download upgrades the
   whole install in one double-click.

## Build

```bash
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w -H windowsgui -X github.com/Lun13371/syntee-launcher/internal/config.DefaultUpdateServer=https://syntee.fun/api/public/updates" -o syntee-launcher.exe .
```

`-H windowsgui` suppresses the console window. Installation and update screens
use WebView2; fallback errors use `MessageBox`.

### Brand assets / Windows icon

The canonical ribbon S assets live in `internal/gui/assets/`: `logo.svg`,
`logo.png` (raster source), and `favicon.ico`, copied from Syntee-Gateway's
`frontend-user/public`. The UI embeds SVG and favicon data URIs so it works
from its temporary HTML file without network access. The launcher currently
has a fixed dark theme; its logo animation and layout are preserved.

`syntee-launcher.syso` is the checked-in Windows resource (amd64, icon group 1),
used by the exe/installer and its window. Shortcuts still launch the client,
but their icon and the uninstall entry's icon come from the launcher.
After changing `favicon.ico`, regenerate the resource before building:

```powershell
go run github.com/akavel/rsrc@v0.10.2 -arch amd64 -ico internal/gui/assets/favicon.ico -o syntee-launcher.syso
go build -ldflags="-s -w -H windowsgui" -o syntee-launcher.exe .
```

Direct builds only write the local executable. `dev.bat` / `prod.bat` also
copy it into SynteeHelper and Syntee-Gateway and modify local install config.

## Gateway endpoints

The launcher talks to public endpoints on Syntee-Gateway. Production builds
use HTTPS. SHA256 verifies download integrity against the manifest; it does
**not** authenticate the release publisher if the manifest is compromised.
Signed releases require a gateway signing pipeline and a trusted public key;
no synthetic keys or fake signature verification are used.

- `GET /api/public/updates/check?channel=stable&current=X.Y.Z` — 204 (up to
  date) or 200 with a `ReleaseManifest` JSON body.
- `GET /api/public/updates/download/{channel}/{version}` — the zip artifact
  (supports HTTP Range for resume).
- `GET /api/public/updates/changelog` — release history (used by the in-app
  changelog tab, not the launcher itself).

## Testing

Run `go test ./...` and `go vet ./...` on Windows. CI runs tests and vet before
building the HTTPS production artifact.

See [STABILITY_TEST_PLAN.md](STABILITY_TEST_PLAN.md) for manual fault-injection
acceptance tests. Tests with simulated HTTP servers or filesystem fixtures do
not replace real Windows checks with Defender, WebView2 and the shipped client.
Never run destructive fault-injection tests against a working installation.
