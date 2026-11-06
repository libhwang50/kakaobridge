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
(`docs/protocol-notes.md` §13). The defaults were measured from one Android build; they are defaults,
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
| `reaction_images` | `false` | how a Kakao reaction looks on Matrix. A Kakao reaction is a mini emoticon — a picture, not a character — so one of the two is a stand-in. Off: the nearest Unicode character, chosen by looking at each item's own picture; every client shows that. On: the item's picture, uploaded once and used as the reaction key, which is how mautrix-discord keys a custom emoji — a client without custom-emoji support then shows the `mxc://` string. Either way the reaction's identity is the Kakao item code, so switching later does not mix up who reacted to what; old reactions of the other form may stay until they are removed on Kakao's side. |

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

## `direct_media:` — media that stays at Kakao

Off by default, which means the bridge copies every photo, video, voice note and file into the
homeserver's media store. With it on, those messages instead carry an `mxc://` URI **the bridge serves
itself**, and the bytes are fetched from Kakao when a client asks for them:

```yaml
direct_media:
    enabled: true
    # A name for this media server; it becomes the server part of the mxc:// URI. Keep it stable:
    # it is already written into past events.
    server_name: kakao-media.example.com
    # A seed for the signing key. Empty generates one and saves it in the database; keeping your own
    # means the URIs in old events still work after a database restore.
    server_key: ""
    media_id_prefix: ""
```

This is mautrix's own mechanism, described for operators at
[docs.mau.fi/bridges/go/discord/direct-media.html](https://docs.mau.fi/bridges/go/discord/direct-media.html);
the same keys work here because the bridge is mautrix. What is Kakao-specific is what the bridge puts in
the id and what it does when one is fetched (`kknet/directmedia.go`):

* The id names **the message**, not Kakao's address for it, because that address is signed for a few
  days (`docs/protocol-notes.md` §8). Every request re-reads the message and gets a fresh one.
* The answer is a redirect to the signed URL, with a cache lifetime that ends shortly before the shorter
  of the two expiry windows Kakao states (the signature in the URL runs three days; the attachment's own
  `expire` runs two weeks — `docs/protocol-notes.md` §8). A client that asks twice in a day crosses the
  network to Kakao once, and the homeserver stores no media bytes for these messages. Measured on this
  deployment: `307` + `location: https://talk.kakaocdn.net/dna/…?signature=…`, with `max-age` five minutes
  short of the signature's own end.
* **What stays copied**: stickers, emotes and mini emoticons. Their bytes have an encrypted header that
  has to be decrypted, and some of them are not a format a client can open, so the bridge must hold them
  to decide what to send (§11). A message that has no media left to address stays a text placeholder as
  before.
* The media proxy shares the appservice HTTP listener (`appservice.port`, `29315` in the shipped
  compose), so a reverse proxy in front of that port is all the public setup needs — e.g. a name that
  resolves publicly pointing at `host:29315`. The homeserver itself does not need to reach that public
  name; it fetches the media the way it reaches the appservice.

The trade is honesty about where the media lives: with direct media, a message whose media Kakao no
longer serves shows an error at view time rather than at bridging time, and the bridge stays the only
place that can resolve its address. With it off, the bytes are yours even after Kakao deletes them.

## Encryption, provisioning, logging

`encryption:` controls end-to-end encryption (the default is fine; the build must use `-tags goolm`, or
link libolm). `provisioning:` enables the provisioning API/widgets. `logging:` is
[mau logging config](https://docs.mau.fi/bridges/go/logging.html). Everything there is mautrix's, and
the same across its bridges.
