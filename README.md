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

If they're installed, `build.bat` then:

- compresses the exe with [UPX](https://upx.github.io), from about 9 MB to under
  3 MB. Some antivirus programs distrust UPX-packed files; if yours flags the exe,
  build with UPX off your PATH.
- packs it into `dist\TwitchMiner-<version>-windows-<arch>.rar` with
  [WinRAR](https://www.win-rar.com). WinRAR's install folder is found even when it
  isn't on your PATH.

Each step is skipped, with a note, when its tool is missing. Quit TwitchMiner from the
tray before building: Windows locks a running `dist\twitchminer.exe`.

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
channels. It asks the game's drops-enabled streams and its most-watched streams which
campaigns they offer, and watches a channel that offers the campaign. Twitch's
drops-enabled tag isn't reliable, which is why both lists are asked. These campaigns
show as **found on stream** in Inventory. Twitch doesn't report whether your game
account is linked for them, so check on Twitch. Games that aren't on your watch list
can only be mined once you've started their campaign on Twitch.

A campaign can only be found once it has started and one of its channels is live.
While a listed game has nothing to mine, TwitchMiner looks again every 5 minutes by
default; change this in **Settings → Checks & connection**, or press **Check for
campaigns** to look right away. A campaign you've already finished stops being
offered by its channels, so it won't be found again.

## Use it

- **Dashboard** shows the drop being mined, the mining queue, the console, and the
  tracked channels. The drop card says when the drop and its campaign should be
  done, and the queue shows how much watching each game has left and roughly
  when it finishes if the games above it go first. These assume the channel stays
  live. Click a channel to watch it. Picking a channel of another game
  switches to manual mode until you go back to auto. **Pause** stops watching until
  you resume. Drops Twitch reports as ready are still claimed while paused. **Check
  for campaigns** reloads the inventory now; next to it you can see when campaigns
  were last checked and when the next check is due.
- **Games** is your watch list. Search to add games; it suggests the ones with
  campaigns first, then any matching Twitch category. When Twitch hides its campaign
  list from your login, a newly added game's live channels are searched for campaigns
  right away. Each game shows its cover art, even when it has no campaign running.
  Drag games to set their priority. The list also has two switches:
  - **Mine the campaign that ends soonest first** orders your listed games by their
    earliest-ending campaign, so time-limited rewards aren't missed. List order breaks
    ties.
  - **Only mine games on this list**: turn it off to mine other eligible games once
    the listed ones run out.
- **Inventory** lists campaigns with their drops, progress and account-link status.
  A campaign that has ended drops off the list after a delay you set in Settings
  (24 hours by default; 0 hides it as soon as it ends).
- **Settings** chooses which reward types count, ignores drops by name, sets how
  often to reload the inventory and look for new campaigns, how long ended campaigns
  stay in Inventory, sets campaign alerts, and sets a proxy.
  - A drop that needs more watching than the time left before it ends is marked
    **won't finish** in the queue and Inventory, and the console says so once.
    Turn on **Skip drops that can't finish in time** to skip such drops and mine
    something that can still finish instead.

TwitchMiner looks after itself while it runs:

- After the computer wakes from sleep, it reconnects to Twitch and checks for
  campaigns right away, instead of waiting for its next scheduled check.
- If a stream ends without Twitch saying so, it notices within about 30 seconds and
  switches channel.
- Channels that banned or timed out your account can't earn drops, so they're
  skipped and tagged in the channel list. Bans are checked when channels are
  loaded, and again for the watched channel when Twitch stops counting progress
  there. A timed-out channel is used again once the timeout ends.
- An unexpected internal error is logged and recovered from, instead of closing the
  app. If mining itself hit the error, mining restarts after 30 seconds.
- When Twitch changes something TwitchMiner relies on, the dashboard shows a "needs
  an update" banner with a link to the latest release, and you get an alert.

### Alerts

TwitchMiner can send Telegram or Discord messages. In **Settings → Alerts**, tick the
events you want:

- **Campaign started:** a new drops campaign for a game you'd mine.
- **Drop received:** each reward the miner claims.
- **Campaign completed:** every drop in a campaign is claimed.
- **Login needed:** Twitch logged the miner out. The message includes the new
  activation code, so you can log back in from your phone.
- **Update needed:** Twitch stopped accepting a request TwitchMiner relies on, so
  mining may not work until you update.

All five are on by default. Then connect a channel and use **Send test**:

- **Telegram:** create a bot with [@BotFather](https://t.me/BotFather), paste its
  token and press **Save** with the chat ID empty. Then send the bot any message: it
  replies with your chat ID, and your chat appears under the fields with a **Use**
  button that saves it. For a channel, add the bot to it and post there, or enter
  `@channelname`. While it waits for that message, TwitchMiner reads the bot's
  messages, so it can't work if another program reads them too (it says so if that
  happens).
- **Discord:** create a webhook in Server Settings → Integrations → Webhooks and paste
  its URL.

The token and webhook URL stay on the server; the dashboard only shows whether each is
set. On the first run, campaigns that are already running are recorded without
alerting, so you only hear about new ones. Campaign alerts only cover campaigns
Twitch shows this login (see [Log in](#log-in)). Logging out yourself doesn't send a
login alert.

## Docker

Each release publishes a multi-arch image (amd64 and arm64) to GitHub's container
registry:

```bash
docker pull ghcr.io/andreykarm/twitchminer:latest
```

To run it with Docker Compose, save this as `compose.yaml` in an empty folder (it's
also in the repository as [`compose.image.yaml`](compose.image.yaml)):

```yaml
services:
  twitchdropsminer:
    image: ghcr.io/andreykarm/twitchminer:latest
    container_name: twitchdropsminer
    ports:
      - "8080:8080"
    volumes:
      - ./data:/data
    restart: unless-stopped
    stop_grace_period: 15s
```

Then start it, and later update it to the newest release:

```bash
docker compose up -d
docker compose pull && docker compose up -d
```

Or without Compose:

```bash
docker run -d --name twitchdropsminer -p 8080:8080 -v ./data:/data --restart unless-stopped ghcr.io/andreykarm/twitchminer:latest
```

To pin a release, use its version as the tag, for example
`ghcr.io/andreykarm/twitchminer:0.2.0`. `latest` follows the newest release.

To build the image from source instead, use the repository's `compose.yaml`:

```bash
docker compose up -d --build
```

The dashboard is at <http://localhost:8080>, and settings and the login are kept in
`./data`. Both compose files read `TWITCHMINER_PORT` for another host port, for
example `TWITCHMINER_PORT=8091 docker compose up -d`, and `TWITCHMINER_DATA_DIR` for
another data folder; `compose.image.yaml` also reads `TWITCHMINER_TAG` to pin a
release. Run `docker compose logs -f` to follow the log and `docker compose down` to
stop it.

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
- `internal/notify` sends Telegram and Discord alerts, and reads the Telegram bot's
  messages to find a chat ID.

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
3. Pushes a multi-arch Docker image to `ghcr.io/andreykarm/twitchminer`, tagged with
   the version and `latest`.

## Not ported yet

These Python features aren't in the Go version yet: claimed-drop history and CSV
export, desktop notifications (claims are alerted through Telegram or Discord),
mining hours, a dashboard password, diagnostics dumps, and translations.
