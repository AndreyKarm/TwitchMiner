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

- On Windows, `build.bat` builds `dist\twitchminer.exe` (icon, version, then UPX
  and a `.rar` when those tools exist). From PowerShell, run it through cmd with
  its full path (`cmd /c "<repo>\build.bat"`); a relative name isn't found. It
  must keep CRLF line endings, or cmd's labels and `goto` break.
- The person you're working with may be running a built miner, usually
  `dist\twitchminer.exe` from the tray with its data in `dist\data`. While it
  runs, Windows locks the exe and the build fails: check with
  `Get-Process twitchminer` and ask them to quit it from the tray first.
- `.claude/launch.json` has two dev servers: `twitchminer` (port 8090, `data/`)
  and `twitchminer-ui` (port 8092, `.devdata/`). Each data folder holds its own
  login, if any. Prefer `twitchminer-ui` for UI checks; logged out, it needs no
  account. Never run a dev server on an account another miner is already mining
  on: two miners on one account fight over it. Static files are embedded, so
  restart the server after editing them.
- `data/`, `.devdata/` and `dist/` are git-ignored. Their `auth.json` holds a
  live Twitch token: never print, commit or share it. When probing Twitch's API
  by hand, read requests with it are fine; don't send anything that changes the
  account.

## Reference implementation

DevilXD's Python miner, <https://github.com/DevilXD/TwitchDropsMiner>, is the
reference when Twitch behaviour is in doubt: `twitch.py`, `channel.py`,
`inventory.py`, `constants.py`, and its commit log for recent fixes. Use a local
checkout if there is one (ask where), or read it on GitHub.

## Twitch quirks

- The miner logs in as the Android TV client, the only device-code client Twitch
  still accepts. Twitch hides `currentUser.dropCampaigns` and
  `DropCampaignDetails` from it, so campaigns for listed games are found by asking
  live channels (`internal/miner/discover.go`). A campaign is only visible once it
  has started, a channel carrying it is live, and the viewer hasn't finished it.
- The TV client accepts raw GQL query text, which the web client's persisted
  hashes don't cover (`ChannelCampaigns`, `SearchGames` and `BanStatuses` in
  `internal/config/operations.go`). Check a new field against the live API
  before relying on it.
- The directory's `DROPS_ENABLED` filter misses channels that do carry campaigns;
  discovery also asks the top unfiltered streams.
- Persisted GQL hashes live in `internal/config/operations.go`. When Twitch
  retires one, requests fail with `PersistedQueryNotFound` and the dashboard shows
  a "needs an update" banner; take the new hash from DevilXD's `constants.py`.
- The telemetry endpoint is `spade_url`, not `beacon_url` (the settings script
  lists beacon first).
- A viewer banned or timed out in a channel's chat earns no drops there.
  `user.self.banStatus` reports it (null when not banned, `expiresAt` null for a
  permanent ban); `internal/miner/bans.go` checks new channels and rechecks the
  watched one while Twitch reports no progress.

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
- Which drops are mined is decided in `evaluateIgnorePolicy` (`ignore.go`), cached
  per campaign by `Campaign.evaluation`. A new reason to skip drops goes there,
  and into that cache key if it can change without a claim or settings save.
- Tests build state with `testMiner`, `testCampaign` and `nopUI`
  (`internal/miner/*_test.go`). Timing and endpoints that tests shorten are
  package variables (`lockLeakWait`, `exitProcess`, `listenRetry`, `pubsubURL`).
- The frontend has no framework or build step. Colours come from the tokens in
  `static/app.css` and the top of `static/dashboard.css`; check layouts at
  desktop width and at 375 px.
- The design is square: never add `border-radius`. Structure uses the neutral
  greys (`--surface*`, `--border*`); pink is only for things you can act on and
  for what's active, shown as a bar (tab underline, left edge of a row) rather
  than a pink box. Text on pink uses `--on-primary`, because white on this pink
  is too faint to read. Warnings use `--warn`. `app.css` is the shared base
  style; override it in `dashboard.css` instead of editing it.
- Keep README.md in step with user-visible behaviour, including its "Not ported
  yet" list.
