# TwitchMiner

Earn timed Twitch Drops without watching. TwitchMiner picks a live channel for the
games you care about, makes Twitch count you as a viewer, and claims finished drops.
It never downloads video or audio: it reads the stream playlist and sends `HEAD`
requests for its segments.

It's a single Go binary with a built-in web dashboard. It's a Go rewrite of
[rangermix/TwitchDropsMiner](https://github.com/rangermix/TwitchDropsMiner), which is
based on [DevilXD/TwitchDropsMiner](https://github.com/DevilXD/TwitchDropsMiner).

## Download

Each [release](../../releases) has builds for Windows, Linux and macOS, plus a
`SHA256SUMS` file. On Windows, unzip it and run `twitchminer.exe`. It runs in the
system tray, with no console window, and opens the dashboard in your browser. The
tray menu has **Open dashboard**, **Pause mining**, **Start with Windows** and
**Quit**. Clicking the icon also opens the dashboard. The log goes to
`data/twitchminer.log`.

**Start with Windows** adds TwitchMiner to your sign-in programs (the per-user `Run`
registry key, so no admin rights are needed). At sign-in it starts in the tray with
the same data folder, without opening the browser. Untick it to remove the entry. If
you move `twitchminer.exe`, the entry is updated the next time you start it from the
new place. The option is greyed out when running with `go run`, because that
executable is temporary.

## Run from source

```bash
go run ./cmd/twitchminer
```

The dashboard opens at <http://127.0.0.1:8080>. Settings, your login and the log are
saved in `./data`.

| Flag | Environment variable | Default | Meaning |
| --- | --- | --- | --- |
| `-addr` | `TWITCHMINER_ADDR` | `127.0.0.1:8080` | Dashboard address. Use `0.0.0.0:8080` to reach it from other devices. |
| `-data` | `TWITCHMINER_DATA` | `data` | Folder for settings, login and the log |
| `-open` | `TWITCHMINER_NO_BROWSER` (set to disable) | on | Open the dashboard in your browser on start |
| `-tray` | `TWITCHMINER_NO_TRAY` (set to disable) | on (Windows) | Show the tray icon |
| `-v` | | off | Debug logging |

On Windows, run `build.bat` to build `dist\twitchminer.exe` with the app icon and
version details embedded, as the tray app with no console window:

```bat
build.bat
```

`build.bat arm64` builds for Windows on ARM, and `build.bat console` keeps the
console window so you can watch the log. They can be combined. The first run
downloads the [go-winres](https://github.com/tc-hib/go-winres) resource tool through
Go's module proxy.

On other systems, or for a plain build without the icon:

```bash
go build -o twitchminer ./cmd/twitchminer
```

## Log in

The dashboard shows a code. Open <https://www.twitch.tv/activate>, enter the code, and
approve. TwitchMiner saves the token in `data/auth.json` and renews it on its own.
Treat that file like a password. **Settings → Log out of Twitch** forgets it.

TwitchMiner logs in as Twitch's Android TV app, one of the few clients Twitch still
allows for device-code login. Twitch hides its campaign list from this login type, so
TwitchMiner finds campaigns for the games on your watch list through their live
channels. It asks each drops-enabled stream which campaigns it offers, and watches a
channel that offers the campaign. These show as **found on stream** in Inventory.
Twitch doesn't report whether your game account is linked for them, so check on
Twitch. Games that aren't on your watch list can only be mined once you've started
their campaign on Twitch.

## Use it

- **Dashboard** shows the drop being mined, the mining queue, the console, and the
  tracked channels. Click a channel to watch it. Picking a channel of another game
  switches to manual mode until you go back to auto. **Pause** stops watching until
  you resume. Drops Twitch reports as ready are still claimed while paused.
- **Games** is your watch list. Search to add games; it suggests the ones with
  campaigns. Drag games to set their priority. The list also has two switches:
  - **Mine the campaign that ends soonest first** orders your listed games by their
    earliest-ending campaign, so time-limited rewards aren't missed. List order breaks
    ties.
  - **Only mine games on this list**: turn it off to mine other eligible games once
    the listed ones run out.
- **Inventory** lists campaigns with their drops, progress and account-link status.
- **Settings** chooses which reward types count, ignores drops by name, sets campaign
  alerts, and sets a proxy.

### Alerts

TwitchMiner can send Telegram or Discord messages. In **Settings → Alerts**, tick the
events you want:

- **Campaign started:** a new drops campaign for a game you'd mine.
- **Drop received:** each reward the miner claims.
- **Campaign completed:** every drop in a campaign is claimed.
- **Login needed:** Twitch logged the miner out. The message includes the new
  activation code, so you can log back in from your phone.

All four are on by default. Then connect a channel and use **Send test**:

- **Telegram:** create a bot with [@BotFather](https://t.me/BotFather) and paste its
  token. Message the bot once, then enter your chat ID, or add the bot to a channel
  and enter `@channelname`.
- **Discord:** create a webhook in Server Settings → Integrations → Webhooks and paste
  its URL.

The token and webhook URL stay on the server; the dashboard only shows whether each is
set. On the first run, campaigns that are already running are recorded without
alerting, so you only hear about new ones. Campaign alerts only cover campaigns
Twitch shows this login (see [Log in](#log-in)). Logging out yourself doesn't send a
login alert.

## Docker

Build and start it with Docker Compose:

```bash
docker compose up -d --build
```

The dashboard is at <http://localhost:8080>, and settings and the login are kept in
`./data`. To use another host port, set `TWITCHMINER_PORT`, for example
`TWITCHMINER_PORT=8091 docker compose up -d --build`. To use another data folder, set
`TWITCHMINER_DATA_DIR`. Run `docker compose logs -f` to follow the log and
`docker compose down` to stop it.

Don't run a second miner, such as `go run`, against the same data folder at the same
time; both would mine on the same account.

On Linux, the container runs as the distroless `nonroot` user (UID 65532), so give it
the data folder first: `mkdir -p data && sudo chown 65532:65532 data`.

The container's health check uses `/healthz`. It returns the miner's state as JSON,
for example `mining`, `idle`, `paused` or `waiting_for_login`. It answers 503 when
the miner is logged in but the inventory has stopped loading: none in the first 10
minutes, or no successful reload for three reload intervals.

The dashboard has no password yet. Only expose it on a network you trust.

## Development

```bash
go test ./...
go vet ./...
```

The code is laid out as follows:

- `cmd/twitchminer` starts everything.
- `internal/config` holds settings, client IDs and GraphQL operations.
- `internal/twitch` has the HTTP client with retries, the device-code login, and
  the GraphQL client.
- `internal/pubsub` keeps the Twitch PubSub websockets for live drop progress and
  stream events.
- `internal/miner` has the state machine, campaign and drop models, channel
  selection and watching.
- `internal/web` has the dashboard server, which pushes live state over
  Server-Sent Events, and the plain HTML, CSS and JavaScript frontend in `static/`.

- `internal/notify` sends Telegram and Discord alerts.

The frontend uses no framework or build step. `static/app.css` is the shared base
style, and `static/dashboard.css` builds on its color tokens.

### Releases

Push a version tag to publish a release:

```bash
git tag v0.2.0
git push origin v0.2.0
```

The [release workflow](.github/workflows/release.yml) then:

1. Runs the tests on Linux and Windows.
2. Builds Windows, Linux and macOS binaries for amd64 and arm64 and attaches them to
   a GitHub release with checksums.
3. Pushes a multi-arch Docker image to `ghcr.io/<owner>/<repo>`, tagged with the
   version and `latest`.

## Not ported yet

These Python features aren't in the Go version yet: claimed-drop history and CSV
export, claim notifications, mining hours, a dashboard password, channel ban
detection, diagnostics dumps, and translations.
