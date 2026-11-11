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
| `failed to read config: open /data/config.yaml: permission denied` (the container restarts over and over) | the config in the volume is not readable by the user the image runs as (`nonroot`, uid and gid 65532). A file put there with `docker cp` keeps the ownership of whoever copied it. Chown it back; the one-line recipe is in `docs/deployment.md`. |

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
* **`Reconnecting to KakaoTalk (retry in …)` for hours, with `LOGINLIST inner status -950` in the log** —
  the session token was no longer accepted. `CHECKIN` answers ok because it carries no token, so the socket
  opens and only the handshake fails. The bridge asks Kakao for a new pair and retries with it; if the new
  one is refused too, it stops retrying and reports `BAD_CREDENTIALS`, because what is needed
  then is `login` again on a device already signed in to the account
  (`docs/protocol-notes.md` §4). Before that, this state retried for thirteen hours on its own.
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
* **Someone in an Open Chat room is shown by a name you don't recognise** — that is the *open profile* this
  room uses, which is what Kakao shows there too. Names come from the room's own member list, and someone
  that list did not carry is asked about in the room where they spoke (`docs/protocol-notes.md` §7). Those
  identities stay apart: a room's participant ids belong to that room — the same room bot measured as
  `4983893056747400519` in one room and `7515847906077057080` in another — so an Open Chat participant and
  a person you friended are separate Matrix users, as they are in Kakao. Only your own identity carries
  across: your messages arrive with your account id even in an Open Chat room, and are bridged as you.
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
* **A Kakao message was edited and Matrix still shows the old text** — an edit arrives as its own push
  carrying the whole new text, and the bridge turns it into an edit of the same Matrix message
  (`docs/protocol-notes.md` §8a). It reaches Matrix only if the bridge was connected when the edit was made;
  an edit made while it was away is not applied afterwards, because the history read carries the new text
  but no signal that something already bridged has changed.
* **A message was deleted in Kakao and is still in Matrix** — Kakao sends nothing when a message is
  deleted (measured on a listening session), so the bridge applies the delete the next time it reads that
  chat's history. "Delete only for me" is not bridged at all: it leaves no trace in what the server sends.
* **A reply arrived as a plain message** — a Kakao reply is a pointer at the message it quotes, not quoted
  text. If that message was never bridged (older than the bridged history), Matrix has nothing to point at.
* **A styled Kakao message arrived plain** — bold, italic, strikethrough and spoilers are ranges kept beside
  the text (§8a). A range that does not fit the text is dropped rather than moved, and a style the bridge has
  not measured on the wire (underline, highlight, colour) is not read yet.
* **A styled Matrix message arrived plain in Kakao** — the marks are read out of the message's HTML (§8a), so
  a client that sends only a body, its Markdown as plain text, gives the bridge nothing to read; the marks
  Kakao has no room for (underline, code, quotes, lists) are sent as their words on purpose. A bridge older
  than this feature sent every style as plain text.
* **A reaction in Kakao does not appear in Matrix** — Kakao sends nothing when someone reacts (measured on a
  listening session, and in a busy Open Chat room), so the bridge reads reactions itself: with each page of
  history, and again a couple of minutes after a room has been active. A reaction on a message the bridge
  never bridged has nothing to attach to and is skipped. Kakao's reactions are mini emoticons rather than
  emoji, so the character shown is a stand-in — chosen from what Kakao's own client draws, and a reaction
  the bridge has not seen drawn keeps the Korean name Kakao gives it; each event names the item behind it
  under `cc.libhwang.kakaobridge.reaction` (`docs/protocol-notes.md` §12). Set
  `network.reaction_images: true` to show Kakao's own picture instead of a character; a picture the bridge
  cannot fetch falls back to the character with a warning, and a client that cannot draw custom emoji shows
  the `mxc://` string where the picture would be. Reacting in Matrix does nothing
  in Kakao yet — the write call has not been measured, so a Matrix reaction is refused with
  `Reactions are not supported`.
* **No space, just rooms** — `bridge.personal_filtering_spaces: true`.
* **A friend's room has no avatar** — a Kakao account with the default avatar has no profile image URL.
  That is not a failed fetch.

## Media asked from Kakao

With `direct_media` on, a photo, video, voice note or file is not stored on the homeserver: its event
names the Kakao message, and the bridge resolves that when the media is fetched. That changes what a
problem looks like.

* **Media is fine for a while and then shows an error** — the bridge is the only thing that can resolve
  the address, so check the log at the moment of the failure, not at bridging time. `no connected Kakao
  account can be asked about chat N` means no Kakao session was up (the account logged out or is
  reconnecting); it works again once it is. `Kakao did not return chat N message M` means Kakao no longer
  serves that message to this account — deleted, or the account left the room.
* **`… copying media to the homeserver instead`** — the bridge could not make a direct address, so it went
  back to storing the bytes. Said once per session; the media itself is fine.
* **Stickers and emotes are still copied** — deliberate. Their bytes have an encrypted header and some are
  not a format a client can open, so the bridge must hold them to decide what to send
  (`docs/protocol-notes.md` §11).
* **The public media URL 404s** — the media proxy shares the appservice listener, so the reverse proxy in
  front of it must pass the path through unchanged (`/_matrix/media/v3/download/…`). Changing
  `direct_media.server_name` does not change the URIs already written into old events, which is why it is
  a value to set once.

With `direct_media` off (the default), media is copied at bridging time, and a photo that Kakao will no
longer serve then is unrecoverable — see `Media unavailable` in the section above.

## A room's queue is full

`Portal event channel is full, queue will block` means one room produced events faster than the bridge could
put them into Matrix. That is normal for a public Open Chat room with a thousand members, where every message
and every read receipt is an event. Nothing is dropped: the bridge waits, and keeps waiting every five
seconds with `Portal event channel is still full` if the room stays busy. Raise `bridge.portal_event_buffer`
(a few hundred) if it happens often, and check whether the bridge is doing needless work per event — it reads
the friend list once per connection and asks Kakao about a stranger once per person, not once per message.

The buffer decides how much of a burst is absorbed before the bridge starts waiting, not whether anything is
lost. Measured with a 1486-member room and a 512 buffer: replaying that room's history at startup filled the
queue for about two seconds — 152 lines of that message — and then cleared on its own. Raising the number
shortens the pause; it does not make the message disappear for a room big enough to outrun it.

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
