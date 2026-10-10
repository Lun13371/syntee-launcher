# Launcher stability acceptance tests (Windows)

Run against a disposable installation or VM, never the user's working client.

## Automated checks

```powershell
go test ./...
go vet ./...
go build -ldflags="-s -w -H windowsgui -X github.com/Lun13371/syntee-launcher/internal/config.DefaultUpdateServer=https://syntee.fun/api/public/updates" -o syntee-launcher.exe .
```

## Download recovery

- Disconnect the network halfway through a download. The partial bytes must remain; automatic retries must request the matching byte offset.
- Kill the launcher during download, restart with the same release, and verify that it continues rather than restarting.
- Change the release manifest while a partial download exists. Bytes from different releases must not be combined.
- Test a server that ignores Range (200), returns a wrong Content-Range, returns 416, truncates a response, returns 404/429/503, or stalls without closing the socket.
- Cancel while waiting for retry. Cancellation must return promptly without deleting resumable data.
- Verify SHA256 mismatch never reaches extraction or replaces current/.

## Apply and recovery

- Use a ZIP without the client executable, with path traversal, Windows ADS paths, symlinks, or excessive unpacked size. The existing installation must remain untouched.
- Test insufficient disk space and read-only staging/current files.
- Hold current/ open with another process; verify bounded rename retry and a clear error.
- Kill the launcher after old current/ is moved to backups but before new/ is promoted. On restart the installation must recover from its journal before a fresh installation starts.
- Kill it after promotion but before launcher.json is saved. On restart the installed version must be reconciled with the version metadata.
- Corrupt launcher.json; verify backup recovery. Corrupt both config and backup; verify a clear error, not silent version reset.
- Verify rollback skips known-bad or structurally invalid backups.

## Process and user settings

- Launch two installers simultaneously. Only one may mutate staging/config/current.
- Run an unrelated executable with the same basename as the client. Updating must not terminate it.
- Check graceful client exit before forced termination; verify updater never kills itself.
- Select no desktop shortcut/no autostart during installation, then restart the launcher. Those choices must remain respected.
- Click Retry after update failure. It must retry, not exit.
- Close/cancel during download and while waiting for an action. No goroutine should remain waiting indefinitely within a running launcher.
- Start a fake client that signals ready, one that exits before ready, and one that exits normally. Only confirmed early startup failures should contribute to crash-loop rollback.
- Close the launcher after client startup. The client must survive launcher exit.

## Security and integration limits

SHA256 detects corruption but is not a release signature. Manifest signing requires the gateway release pipeline to sign canonical release metadata and a real trusted public key in the launcher. Do not treat checksum verification as proof of publisher identity.

Direct desktop/autostart shortcuts launch the client, so monitoring every client launch requires client cooperation or a deliberate change to shortcut architecture. Client readiness/graceful-quit markers must be implemented and verified in SynteeHelper; comments in launcher source alone are not sufficient evidence.

Also test Defender/SmartScreen delays, WebView2-unavailable fallback, cold startup on a slow disk, redirected Desktop folders, and normal uninstall after update.
