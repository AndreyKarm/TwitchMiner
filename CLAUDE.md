# TwitchMiner

A Go rewrite of DevilXD's Python TwitchDropsMiner: one binary that mines Twitch
drops and serves a plain HTML/JS dashboard. README.md covers usage and layout.

## Commands

```bash
go test ./...
go vet ./...
gopls check -severity=hint $(git ls-files '*.go')   # unused code, modernize hints
GOOS=linux go vet ./...                              # the Docker image is Linux
```

- `build.bat` builds `dist\twitchminer.exe` (icon, version, then UPX and a `.rar`
  when those tools exist). From PowerShell, call it by full path
  (`cmd /c "E:\GitHub\TwitchMiner\build.bat"`); a relative name isn't found. It
  must keep CRLF line endings, or cmd's labels and `goto` break.
- The user runs `dist\twitchminer.exe` from the tray with data in `dist\data`.
  While it runs, Windows locks the exe and the build fails: check with
  `Get-Process twitchminer` and ask them to quit it from the tray first.
- `.claude/launch.json` has two dev servers: `twitchminer` (port 8090, `data/`,
  logged in to the user's account) and `twitchminer-ui` (port 8092, `.devdata/`,
  logged out). Prefer `twitchminer-ui` for UI checks; it needs no account. Only
  use `twitchminer` when the user's exe isn't running, so two miners never mine on
  one account. Static files are embedded, so restart the server after editing them.

## Reference implementation

DevilXD's Python miner is checked out at `E:\GitHub\TwitchDropsMiner`. Compare
against it (`twitch.py`, `channel.py`, `inventory.py`, and its git log for recent
fixes) when Twitch behaviour is in doubt.

## Twitch quirks

- The miner logs in as the Android TV client, the only device-code client Twitch
  still accepts. Twitch hides `currentUser.dropCampaigns` and
  `DropCampaignDetails` from it, so campaigns for listed games are found by asking
  live channels (`internal/miner/discover.go`). A campaign is only visible once it
  has started, a channel carrying it is live, and the viewer hasn't finished it.
- The directory's `DROPS_ENABLED` filter misses channels that do carry campaigns;
  discovery also asks the top unfiltered streams.
- Persisted GQL hashes live in `internal/config/operations.go`. When Twitch
  retires one, requests fail with `PersistedQueryNotFound` and the dashboard shows
  a "needs an update" banner; take the new hash from DevilXD's `constants.py`.
- The telemetry endpoint is `spade_url`, not `beacon_url` (the settings script
  lists beacon first).

## Code rules

- `Miner.mu` guards all miner state. Functions ending in `Locked` expect it held.
  Never do network I/O while holding it.
- Start goroutines with `m.goSafe(name, fn)`, or defer `m.recoverPanic(name)` in
  `wg.Go`/`time.AfterFunc` bodies. Session loops use `m.sessionTask`, which ends
  the session on a panic. `Recovered` exits the process if a panic leaked `m.mu`.
  PubSub handlers are wrapped with `m.safeHandler` and run one topic at a time.
- Dashboard state flows through `ui.Publish(section, value)` (directly, or via
  the `dirty` flags in `uiLoop`). Each section needs a matching entry in the
  `renderers` map in `static/app.js`.
- A new setting needs: the field and default in `config.Settings`/`Defaults`, a
  clamp in `Normalize`, a `policy` field (and `sameMining`) if it changes what is
  mined, handling in `SettingsChanged`, and the form field in `index.html` and
  `app.js`. New alert events go in `config.AlertEvents` and `ALERT_EVENTS` in
  `app.js`; missing events default to on in existing settings files.
- Tests build state with `testMiner`, `testCampaign` and `nopUI`
  (`internal/miner/*_test.go`). Timing and endpoints that tests shorten are
  package variables (`lockLeakWait`, `exitProcess`, `listenRetry`, `pubsubURL`).
- The frontend has no framework or build step. Colours come from the tokens in
  `static/app.css`; check layouts at desktop width and at 375 px.
