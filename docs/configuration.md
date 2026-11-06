# Configuration

The bridge takes one YAML file: `--config /path/config.yaml`. Generate a fully commented one with

```bash
kakaobridge --config config.yaml --generate-example-config
```

mautrix rewrites that file on every start, filling in defaults and writing the generated `as_token` /
`hs_token` when you run `--generate-registration` — so keep it out of version control if you care about
diff noise, and back it up if you care about the tokens.

Most of the file is mautrix's own ([bridge config docs](https://docs.mau.fi/bridges/go/setup.html)).
This page covers the parts that are specific to Kakao or that are easy to get wrong.

## `network:` — the Kakao side

Every value here is what the bridge *announces to Kakao*, and Kakao cross-checks the combination
(`docs/protocol-notes.md` §12). The defaults were measured from one Android build; they are defaults,
not constants.

| key | default | notes |
|---|---|---|
| `account` | — | the account id, **in the international form** (`8210…`): that is what Kakao's own account screens show, and the local form the Android namespace needs is derived from it. Set `network.account` only if you turn `ask_for_credentials` off. |
| `password` | — | the account password. Not just for the first login: a registered device renews itself with `login.json` + this password (`docs/protocol-notes.md` §4), so a login without it cannot survive a session end. |
| `ask_for_credentials` | `false` | ask for the account and password in the management room instead of reading them from here. The bridge asks **either way** if `account`/`password` are unset, so this only matters once they are set — it is how a second Matrix user logs in. Note that a password typed in a room is visible in that room. |
| `agent` | `android` | `android` = a registered mobile sub-device, durable and self-renewing. `win32` = the PC-client namespace, whose sessions die when they end; it exists for experiments, not for production. |
| `app_version` | `26.9.1` | the client build to announce. Must match the build that minted this device's tokens; Kakao rejects calls that look older than the account's client. If the bridge breaks with no code change, compare this to a current client. |
| `language` | `ko` | the language the client announces. |
| `mccmnc` | `45006` | a mobile carrier code, required by an Android LOCO session. That default is a Korean carrier; set your own. |
| `account_form` | `""` | override the account-id form sent to Kakao: `international` forces `8210…`. Empty means "follow the namespace", and the wrong form is answered with `status 30`. |
| `device.name` | `kakaobridge` | what your account's device list shows on the phone. |
| `device.model` | `linux` | |
| `device.os_version` | `16` for android, `10.0.19045` for win32 | the OS string the client reports. |
| `device.x_vc` | the Windows client's value | a client build constant, not a secret; `passcodeLogin/generate` needs it. |
| `tokens_file` + `tokens_mxid` | `""` | import an already-registered device instead of the passcode flow. Kakao allows one mobile sub-device per account, so after wiping the database this is how you keep the identity — the JSON is what `cmd/locoauth` writes, and `tokens_mxid` is the Matrix user who owns it. If logins already exist in the database, the import is ignored. |

Where the credentials end up: a completed login stores its account, agent and password in the bridge
database, keyed by your Matrix user, and renews itself from there on the next start. Nothing Kakao
issues is written to disk by the bridge itself — `tokens_file` is an *input*, not an output. The file is
a small JSON object:

```json
{"userId": 0, "access_token": "…", "device_uuid": "…", "agent": "android",
 "countryIso": "kr", "recipe": "…"}
```

A device registered through `login` therefore exists only in the database. If you want a copy of it
before wiping that database, read those fields out of the `user_login` row; `cmd/locoauth -tokens
tokens.json` checks whether a file you have still opens a session (it will kick the running bridge, which
allows one session per device). An export path is a reasonable first contribution.

## `homeserver:`, `appservice:` — reaching each other

* `homeserver.address` must be reachable *from the bridge container*; `homeserver.domain` is the Matrix
  domain (they differ when the homeserver is behind a different name internally).
* `appservice.address` is what the bridge tells the homeserver to call it — in the registration file.
  Inside Docker, that must be a name the **homeserver** can resolve and a port it can reach
  (`http://kakaobridge:29315` in a shared compose network), not `localhost`. `appservice.public_address`
  is the public form, used for links; `hostname`/`port` are the local listen address.
* If the container's address changes on every restart, the homeserver keeps dialing a dead IP and the
  bridge eventually exits with `Homeserver -> appservice connection is not working`. Give it a fixed
  address — see `docs/deployment.md`.
* A container's DNS search domain is inherited from the host. If your host has one (a VPN often adds
  one), a bare container name can resolve to `<name>.<search-domain>` and fail; set an empty search
  domain on the network/services (`dns_search: ["."]` in compose).
* `appservice.bot.username` / `displayname` / `avatar` are the bridge bot's Matrix identity. `avatar`
  must be an `mxc://` URI; the string `remove` clears it. The default avatar the bridge ships is hosted
  on the author's homeserver — set your own, or `remove`, if you would rather not depend on it.

## `bridge:` — behaviour worth knowing

From the generated example config, the defaults that matter for this bridge:

| key | default | effect |
|---|---|---|
| `command_prefix` | `!kakaotalk` | commands in rooms other than the management room (`login`, `ping`, `import`, …). |
| `personal_filtering_spaces` | `true` | one space per login holding its portals. The KakaoTalk icon is set as its avatar by the connector. |
| `bridge_status_notices` | `errors` | how much of the bridge state machine is announced in the management room. With Kakao, `errors` is usually enough; raise it while debugging a login. |
| `unknown_error_auto_reconnect` / `unknown_error_max_auto_reconnects` | `null` / `10` | reconnect behaviour after an `UNKNOWN_ERROR` state. |
| `relay.*` | empty | relay mode: a Matrix user bridges a Kakao chat without a Kakao account of their own. |
| `permissions.*` | level defaults | who may run which command. |
| `deduplicate_matrix_messages` | `false` | leave this off; the bridge already deduplicates Kakao echoes by `logId`. |
| `portal_event_buffer` | `64` | how many of a room's events may wait while that room's events are handled one after another. A busy public Open Chat room can outrun the default; the log then says `Portal event channel is full, queue will block`. A few hundred is cheap and enough. |

## Encryption, provisioning, logging

`encryption:` controls end-to-end encryption (the default is fine; the build must use `-tags goolm`, or
link libolm). `provisioning:` enables the provisioning API/widgets. `logging:` is
[mau logging config](https://docs.mau.fi/bridges/go/logging.html). Everything there is mautrix's, and
the same across its bridges.
