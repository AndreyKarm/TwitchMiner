# TwitchMiner

Earn timed Twitch Drops without watching. TwitchMiner picks a live channel for the
games you care about, makes Twitch count you as a viewer, and claims finished drops.
It never downloads video or audio: it reads the stream playlist and sends `HEAD`
requests for its segments.

It's a single Go binary with a built-in web dashboard. It's a Go rewrite of
[rangermix/TwitchDropsMiner](https://github.com/rangermix/TwitchDropsMiner), which is
based on [DevilXD/TwitchDropsMiner](https://github.com/DevilXD/TwitchDropsMiner).

## Run it

```bash
go run ./cmd/twitchminer
```

The dashboard opens at <http://127.0.0.1:8080>. Settings and your login are saved in
`./data`.

| Flag | Environment variable | Default | Meaning |
| --- | --- | --- | --- |
| `-addr` | `TWITCHMINER_ADDR` | `127.0.0.1:8080` | Dashboard address. Use `0.0.0.0:8080` to reach it from other devices. |
| `-data` | `TWITCHMINER_DATA` | `data` | Folder for `settings.json` and `auth.json` |
| `-open` | `TWITCHMINER_NO_BROWSER` (set to disable) | on | Open the dashboard in your browser on start |
| `-v` | | off | Debug logging |

To build a standalone binary:

```bash
go build -o twitchminer.exe ./cmd/twitchminer
```

## Log in

The dashboard shows a code. Open <https://www.twitch.tv/activate>, enter the code, and
approve. TwitchMiner saves the token in `data/auth.json` and renews it on its own.
Treat that file like a password. **Settings → Log out of Twitch** forgets it.

TwitchMiner logs in as Twitch's Android TV app, one of the few clients Twitch still
allows for device-code login. Twitch may hide its full campaign list from this login
type. If it does, the console says so, and only campaigns you've already started on
Twitch can be mined.

## Use it

- **Dashboard** shows the drop being mined, the mining queue, the console, and the
  tracked channels. Click a channel to watch it. Picking a channel of another game
  switches to manual mode until you go back to auto.
- **Inventory** lists campaigns with their drops, progress and account-link status.
- **Settings** sets the games to watch, in priority order. Turn off **Only mine games
  on this list** to mine other eligible games when the listed ones run out. You can
  also choose which reward types count, ignore drops by name, and set a proxy.

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

The frontend uses no framework or build step. `static/app.css` is the shared base
style, and `static/dashboard.css` builds on its color tokens.

## Not ported yet

These Python features aren't in the Go version yet: claimed-drop history and CSV
export, Telegram and Discord notifications, mining hours, a dashboard password,
channel ban detection, diagnostics dumps, and translations.
