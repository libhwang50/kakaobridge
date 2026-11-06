# Deployment

## Build from source

```bash
make build            # go build -tags goolm ./...
make test
```

Two things the build needs, and why:

* **`-tags goolm`** — mautrix's crypto defaults to C libolm; `goolm` selects the pure-Go implementation
  so no libolm headers are needed. Without it: `fatal error: olm/olm.h: No such file or directory`.
* **CGO enabled** — mautrix links `mattn/go-sqlite3`; `CGO_ENABLED=0` fails in `mxmain`. If your
  toolchain's scratch directory is small or quota'd, the cgo link step can die with `disk quota
  exceeded` — point `GOTMPDIR` somewhere roomy.

The bridge binary is `cmd/kakaobridge`. It takes `--config`, `--generate-example-config`,
`--generate-registration`, `--registration`, `--version` (single-dash `-config` is parsed as
`-c onfig`, so use the long form).

## Container

The `Dockerfile` builds with the tags above and runs on `distroless/base-debian12:nonroot`. Two
consequences worth knowing:

* the process runs as **uid 65532**, so anything it writes into a mounted `/data` (`config.yaml`,
  `registration.yaml`) lands as uid 65532 mode 0600 — the host user cannot edit it. Replace such files
  through a container, or `chown 65532:65532` afterwards;
* `appservice.hostname` must be `0.0.0.0` (or empty) in the container, or the homeserver cannot reach
  the appservice port.

## Compose: homeserver, database, appservice in one project

The failure this layout prevents: a bridge restart gives the container a new address, the homeserver
keeps dialing the old one, and mautrix exits fatally after repeated failed
homeserver→appservice pings (`Homeserver -> appservice connection is not working`) — which the restart
policy then loops.

```yaml
services:
  kakaobridge:
    build: ./kakaobridge
    volumes:
      - ./kakaobridge/data:/data
    networks:
      matrix-net:
        ipv4_address: 172.30.0.10        # stable: the registration points at this name
    dns_search: ["."]                    # do not inherit the host's search domain
    depends_on:
      postgres:  { condition: service_healthy }
      homeserver: { condition: service_healthy }
    restart: unless-stopped

networks:
  matrix-net:
    name: matrix-net
    ipam:
      config:
        - subnet: 172.30.0.0/24
```

* Put the homeserver and its database in the same project, with healthchecks, and use `depends_on:
  condition: service_healthy` — a bridge that starts before the homeserver is ready crash-loops.
* Declare the homeserver's data volume `external: true` if it predates this file, so
  `docker compose down -v` cannot delete the homeserver's store.
* `appservice.address` in the config (and the `url` in the registration) must be the name the
  **homeserver** can resolve: `http://kakaobridge:29315`.
* **The host's DNS search domain is inherited by every container.** A VPN or mesh tool that writes
  `search example.internal` into the host's `/etc/resolv.conf` makes the homeserver ask for
  `kakaobridge.example.internal`, get NXDOMAIN, and report `M_CONNECTION_FAILED` for a container that is
  up and listening. `dns_search: ["."]` on each service stops that. (Pointing the registration at the
  container's fixed IP is an alternative.)

## Registering the appservice

```bash
kakaobridge --config config.yaml --generate-registration
```

then load `registration.yaml` into the homeserver — `app_service_config_files` on Synapse, or
`!admin appservices register` in the admin room on Conduit/Tuwunel (paste the file contents; note that
Tuwunel's admin bot ignores `m.notice`, so send `m.text`).

Two traps in the generated file:

* `--generate-registration` writes a **random** `sender_localpart`, while the running bridge uses
  `appservice.bot.username` from the config. Set the registration's `sender_localpart` to the same
  value you configured for the bot, or the bot's actual identity will not match the registration's user
  namespace.
* The `id` must not collide with another appservice on the homeserver (`Duplicate id` on Tuwunel), and
  transaction delivery is `PUT /_matrix/app/v1/transactions/{txn}`.

## Deploying updates

Keep the deployment checkout a **git checkout** and update it with git:

```bash
git push origin main                     # development machine
git fetch && git reset --hard origin/main && docker build -t kakaobridge:local .   # server
```

Copying files over the checkout (rsync with `--exclude .git`) builds fine but leaves the repository's
HEAD behind: the next `git pull` there refuses because the tree looks dirty, and `git log -1` names an
older commit than what is running — which makes a deployed fix look undeployed.

## The config file is rewritten

mautrix writes the config back on every start unless you pass `--no-update`. A key that is not part of
the generated example config is dropped from the file on the way out. Keep your own copy of the diff you
care about, or set the values in a config that you regenerate from the example. Keys that are part of the
example — `portal_event_buffer` among them — survive a rewrite, so raising them in the file is safe.

Editing that file in place has one trap: the image runs as `nonroot` (uid and gid 65532), and `docker cp`
in leaves the file owned by whoever copied it. The bridge then crash-loops with
`failed to read config: open /data/config.yaml: permission denied`. Put the ownership back with a throwaway
root container over the same directory:

```bash
docker run --rm --user 0:0 -v ./kakaobridge/data:/data alpine:latest \
  chown 65532:65532 /data/config.yaml
```

## Credentials, database, and what survives a wipe

* Everything about a login lives in the bridge database: the Matrix user, the Kakao device, the account
  id and password it renews itself with, the chat list's portals, and the message id mappings.
* `network.tokens_file` is an *input* for bootstrapping an already-registered device; the bridge does
  not write token files. A device registered through `login` lives only in the database, so keep a copy
  of the fields (`userId`, `access_token`, `device_uuid`, `agent`) from the `user_login` row if you plan
  to wipe it. `cmd/locoauth -tokens tokens.json` verifies a file you have — and `KICKOUT`s the running
  bridge, since a device carries one session.
* **Wiping the database does not free Kakao's mobile-device slot.** Kakao allows one mobile sub-device
  per account, so a fresh database with the old device still registered will fail at the passcode step
  (and the old credential answers `-100` once the account has approved a different mobile device).
  Either import the old identity with `tokens_file`, or deregister/replace it on the account first.
* Changing `network.app_version` changes the client the device belongs to: a token pair is tied to the
  build that minted it, so an upgrade there means re-registering the device.

## What working looks like

```
INF KakaoTalk network configuration loaded agent=android app_version=26.9.1 …
INF Starting user login id=…
INF LOCO session online with a chat list chats=N push_cursor=…
INF Chat list bridged: rooms created/updated and messages loaded chats… rooms=N messages=M
DBG Sent new bridge state … "state_event":"CONNECTED"
```

A login made through `login` in the bot DM must reach `CONNECTED` **without a restart** — if `list-logins`
shows the login and nothing follows it, the session was never started
(`docs/bridgev2-notes.md`).
