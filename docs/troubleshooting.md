# Troubleshooting

Symptom first, cause second. Protocol-level meanings are in `docs/protocol-notes.md`; framework-level
ones in `docs/bridgev2-notes.md`.

## The bridge exits soon after starting

| message | what it means |
|---|---|
| `Homeserver -> appservice connection is not working` (fatal, repeated) | the homeserver cannot reach the appservice. Usually the registration points at an address that no longer exists, or the container inherited the host's DNS search domain, or the homeserver was not up yet. Fixed addresses, healthchecks and `dns_search: ["."]` are in `docs/deployment.md`. |
| `appservice.as_token not configured. Did you forget to generate the registration?` | run `--generate-registration` against that config. |
| `Duplicate id` while registering | another appservice already uses that `id`. |
| `fatal error: olm/olm.h: No such file or directory` | build without `-tags goolm`; see `make build`. |
| linker `disk quota exceeded` | the cgo link step needs room: `GOTMPDIR=/dev/shm make build`. |

## Login does not work

* **`status 12`, or "check your account or password"** — either the password, or the **account-id form**:
  the Android namespace sends `010…` and the PC namespace `8210…`, and the wrong one is rejected. Store
  `network.account` in the international form and let the namespace derive the rest
  (`docs/protocol-notes.md` §3).
* **The code is shown and typing it does nothing** — it must be typed into KakaoTalk on a device that is
  *already signed in* to that account, before it expires; the bridge polls and reports the outcome.
* **`-100`, now or after a while** — this device is no longer usable: the account approved a different
  mobile device, or the device was deregistered. Run `login` again; if the mobile-device slot is held by
  the old identity, deal with that first (`docs/protocol-notes.md` §4).
* **Login succeeds but no rooms appear** — the login row exists but its session was never started. Check
  the management room state: it should move to `CONNECTED` without a restart. If it doesn't, that build
  predates the fix described in `docs/bridgev2-notes.md`.
* **`TRANSIENT_DISCONNECT`, then `RECONNECTING` forever** — the network, or something else holding the
  same Kakao device credential. One LOCO session per device.
* **`KICKOUT`** — a second session started with the same credential: a second bridge instance, or a
  `cmd/locoprobe`/`cmd/locoauth` run against the bridge's tokens. Stop the other one.

To check a credential without touching the running bridge, use a *separate* device or accept that the
probe will kick the bridge: `go run ./cmd/kkrenew` says whether the token pair still renews itself.

## A message from Matrix is not delivered

* **"⚠️ Your message was not bridged: unsupported message type"** — mautrix refused it before the
  connector saw it, because the capability list does not claim that type/mimetype. Look at
  `kknet.Client.GetCapabilities` and `TestCapabilitiesMatchWhatCanBeSent`. An event with no
  `info.mimetype` cannot match a capability and is refused for this reason.
* **A file is refused with `EXTENSION_NOT_ALLOWED` or `INVALID_MEDIA_TYPE`** — that is Kakao's own
  gateway answer and it is quoted on purpose. Try a different extension or send it as a document type
  the client would accept; `audio/*` is not bridged as audio (send it as a file).
* **A photo arrives, the caption arrives a moment later as a separate message** — intended: Kakao has no
  caption on a photo (`docs/protocol-notes.md` §9).
* **A video send appears to fail and then succeed** — `needReady` tickets answer the first commit with
  `-306` and the bridge retries (`docs/protocol-notes.md` §10). Only a persistent failure is a problem.
* **Nothing at all, no log lines** — check that the room is a portal of a `CONNECTED` login
  (`list-logins` in the bot DM), and that the message is not `m.notice` from another bridge.

## Kakao messages look wrong on Matrix

* **A photo or sticker shows "Media unavailable" in the client** — the source URL in the old Kakao log
  is signed and has expired. Kakao keeps the entry; the bytes are gone.
* **A sticker renders as a still or a broken image** — Kakao's animated emoticons are WebP with an
  encrypted 128-byte header; if that is not decrypted the upload is not a valid image
  (`docs/protocol-notes.md` §11).
* **The same Kakao message appears twice** — the inbound dedup row for that `logId` was not written. The
  constraints involved are in `docs/bridgev2-notes.md`.
* **`Failed to bridge read receipt … M_NOT_FOUND`** — a receipt was pointed at a synthetic message row.
* **The self-memo chat has no name or picture** — that chat has no other member to name it after; the
  connector supplies its name and avatar, which an older build did not.
* **A group room has no name** — two different things: a group that was never named has no name in
  Kakao either (the phone titles it from its members, so the bridge composes a name of its own), and a
  group whose *members* changed may show a stale title until the chat is resynced (`sync-portal` in the
  room).
* **An Open Chat room has no name, topic or picture** — that profile is not on the chat: the bridge reads
  it from the room's Open Link with a second call (`docs/protocol-notes.md` §7b). If the room stayed
  unnamed, look for `Kakao did not describe this open chat room` in the log: that call was refused and the
  room was left as it was. `!kakaotalk sync-portal` in the room asks again. Renaming the room or editing
  its intro on the phone updates the Matrix room by itself: Kakao pushes the new link (`SYNCLINKUP`).
* **A Team Chat's "About team" text is not the room topic** — that text is not in the chat protocol at
  all, it comes from a second Kakao service (`docs/protocol-notes.md` §7a). If the topic stayed empty,
  look for `Kakao's team service did not describe this room` in the log: the request was refused and the
  room was left as it was. `!kakaotalk sync-portal` in the room asks again, which is also how an edit
  made on the phone is picked up.
* **A group room has no members' avatars, or two rooms for one chat** — the member list and the chat's
  class both come from separate calls; if one is refused the room is built with what is known. Two rooms
  for one chat is fixed in the connector (it used to key an unidentified chat as a shared room); remove
  the extra one with `!kakaotalk delete-portal` run inside it.
* **Someone joined or left a group and the Matrix room did not follow** — Kakao sends membership as a
  type-0 room notice rather than an event, and the bridge re-reads the chat when one arrives (the member
  list is full, so bridgev2 turns the difference into joins and leaves). If the room did not change,
  look for `Kakao reports a change to a room` in the log and the `feed_type` it names — an unrecognised
  notice is logged rather than guessed. `!kakaotalk sync-portal` in the room forces the re-read.
* **No space, just rooms** — `bridge.personal_filtering_spaces: true`.
* **A friend's room has no avatar** — a Kakao account with the default avatar has no profile image URL.
  That is not a failed fetch.

## Test failures

Tests that want a file under `loco/testdata/` or `testdata/` skip when it is missing: those captures are
traffic from a personal account and are not published. `make test` is expected to pass without them; the
READMEs in those directories explain how to record your own.

## Reporting a problem

Include:

* the version the bridge reports (`--version-json`, and `network.app_version` from its config);
* the management room's state message (`CONNECTED` / `BAD_CREDENTIALS` / …), and the bridge log lines
  around the failure at `DEBUG` level — `logging:` in the config;
* for a send failure: the event type and mimetype, and the exact text the bridge answered;
* for a protocol change: whether a current KakaoTalk client still behaves as described in
  `docs/protocol-notes.md` — that usually decides whether it is Kakao or the bridge.
