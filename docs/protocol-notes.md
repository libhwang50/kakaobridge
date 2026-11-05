# Protocol notes

Everything this bridge assumes about KakaoTalk, written down as measurements rather than as folklore.
Kakao's protocols are undocumented and change without notice, so each note says how it was established
and which code depends on it. Where a note says "the phone" or "the PC client", that is a shipped
KakaoTalk client of the build named in `network.app_version`.

How the measurements were taken:

* the account REST API — mitmproxy with the system CA installed, driven from the PC client (a Wine
  prefix) and from a rooted Android device;
* the LOCO socket — in-process hooks on the Android client's TLS reads/writes, and raw frames from the
  bridge's own registered device via `cmd/locoprobe`;
* anything marked "verified with the bridge's own credential" was reproduced with tokens the bridge
  itself holds, which is the only way to be sure a request shape is accepted rather than merely seen.

## 1. Two networks, not one

A KakaoTalk account talks to two services:

| | account REST | LOCO |
|---|---|---|
| transport | HTTPS, JSON | TLS socket, length-prefixed BSON frames |
| used for | registration, token renewal, friends, the chat "drawer", media upload tickets | the chat session: sending, history, presence, pushes |
| auth | `authorization: <access_token>-<device_uuid>` | token + device id inside the login frame |
| implemented by | `kakao/rest.go`, `kakao/auth.go`, `kakao/media.go` | `loco/` |

The API hosts are not one host: the media gateway answers under
`https://talk-pilsner.kakao.com/messaging/…` (`pilsnerBase`), and LOCO handshakes give a host and port
per session rather than a fixed one. Hard-coding either is a mistake waiting to happen.

## 2. Registration: the passcode flow

`kakao/auth.go`, `kknet/login.go`.

A new device asks `passcodeLogin/generate` for a code, the user types that code into KakaoTalk on a
device already signed in, and the new device polls `passcodeLogin/check` until it is approved; the
answer carries the device's token pair. Two details that are easy to get wrong:

* the poll response also tells the client **how long to wait** — honour it;
* the fields that identify the *requesting* device differ from those of the *approving* device; sending
  the approving device's identity is what makes the flow succeed.

QR is the other registration path in the shipped clients (`/qr` endpoints); the bridge implements the
passcode path because that is the one a headless process can drive from a chat.

## 3. Namespaces: `android` and `win32` are different clients

`network.agent`, `kknet/kknet.go`, `loco/auth.go`.

Kakao distinguishes client classes, and the differences are not cosmetic:

| | `android` (mobile sub-device) | `win32` (PC client) |
|---|---|---|
| account id form accepted | local, `010…` | international, `8210…` |
| login frame fields | one field set (plus a radio code, `mccmnc`) | a different set |
| lifetime of the device | **registered and durable** — it can renew itself | a session that dies when it ends |
| visible on the account's device list | yes | yes |

`network.account_form` overrides the id form only if a client changes its mind; by default the namespace
chooses it, because the wrong form is simply not accepted.

`mccmnc` (a mobile country/carrier code) is required by an android LOCO session. The default in
`Config.Fill` is the Korean code recorded from the phone used during development; set it to your own if
you are not on a Korean carrier. `X-VC` is a client build constant, not a per-installation secret —
`passcodeLogin/generate` answers `-500` without it — and both shipped values are accepted in both
namespaces.

## 4. Tokens, renewal, and the answers that mean "no"

`kknet/client.go`, `kknet/login.go`, `kakao/auth.go`.

* A **registered** android device re-mints its token pair with `login.json` and the **account
  password**. That is why the bridge keeps the password it was given (in `LoginMeta` in the database):
  without it, a login cannot survive a session end. There is no refresh token in the OAuth sense.
* **One mobile sub-device per account.** Approving a second mobile device replaces the first; the
  replaced device then answers `login.json` with **`-100`**. This is also why wiping the bridge's
  database and re-running the passcode flow fails — the slot is still held. `network.tokens_file`
  exists to carry the old identity into a new database.
* **`status 12`** — the account id or password was not accepted.
* **`KICKOUT`** (a LOCO push) — a second session started with the same device credential. One session
  per device; running `cmd/locoprobe` with the bridge's tokens will do this to the running bridge.
* The nonce seen in some login frames is a lead, not a requirement — the flows work without it.

## 5. LOCO frames

`loco/session.go` (frame encode/decode), `loco/bson.go` (the BSON reader).

A frame is a length prefix, a command name, a packet id, a status, and a BSON body. Two rules that
cost hours:

* **BSON types are checked.** Sending an array of the wrong BSON type is `status -300`, not a parse
  error. `cmd/locoprobe` therefore spells out types in its arguments (`chatId:i64=…`,
  `limitCount:i32=…`).
* **Field order is significant** for some verbs, which is why the request builders do not use maps.

The login frame carries the token and device id; `LOGINLIST` returns the chat list (§6); `PING` keeps
the session alive. The server pushes on the same socket: messages as `MSG`, read markers as `DECUNREAD`,
membership changes as `NEWMEM`/`DELMEM` (§7a), and an Open Chat room's name, intro or picture changing as
`SYNCLINKUP` (§7b). A verb the bridge does not handle is logged with its body
in hex, so its name and shape can be read off a real event rather than guessed at.
`Session.LastLoginlist` is exposed because the chat list arrives during the handshake, before any call
could ask for it.

## 6. The chat list, and why the bridge needs REST as well

`loco/chatdata.go`, `cmd/kksessions`.

`LOGINLIST` answers with `chatDatas` (rooms with their last-message summaries), `lastTokenID` and a
`revision`. It is a snapshot of the drawer, not a subscription: to know which of the account's *other*
sessions hold which chats, the client calls the drawer REST API (`/{agent}/friends/diff.json`). The
bridge reads the LOCO list; `cmd/kksessions` prints both so a mismatch can be seen.

Each entry's `t` names the chat class, and the classes differ in more than the label — measured values:

| `t` | what it is | what the entry carries |
|---|---|---|
| `DirectChat` | a friend chat | `i` member ids, `k` nicknames |
| `MemoChat` | the account's own notes | one member: the account |
| `PlusChat` | a channel / plus friend | `i`, `k` (the channel name) |
| `MultiChat` | a group chat — Regular or Team | `i`, `k`; name and picture live in `chatMetas` (§7a) |
| `OM` | an Open Chat room | **no** `i`/`k`, no title; carries `li`/`otk` — the name, intro and picture are on the Open Link, read with `SYNCLINK` (§7b) |

A group needs two or more people besides the account: inviting one friend gives a `DirectChat`, not a
group.

## 7. Rooms, members, read markers

`loco/roominfo.go`, `kknet/pump.go`.

* `ChatOnRoom` (`c`/`a`/`l`), `CHATINFO` (`chatInfo`, `displayMembers`), `GETMEM` (`members`,
  `nickName`) — the bridge needs all three because each answers something the others don't, and a
  parser that reads the wrong key fails silently as "a room with no members".
* Members carry three profile image sizes, spelled two ways: `CHATINFO`'s `displayMembers` use
  `profileImageUrl` / `fullProfileImageUrl` / `originalProfileImageUrl`, a `GETMEM` member uses the
  shared key dictionary — `pi` / `fpi` / `opi`. A Matrix room avatar should use the full-size one.
* A **`DECUNREAD`** push names a `logId` and says how far a member has read. Kakao has no
  per-message receipt, so the bridge maps it onto the nearest older bridged message
  (`docs/bridgev2-notes.md`).

### 7a. Group chats (`MultiChat`)

Measured on a Regular Chat and a Team Chat created for the purpose (`cmd/locoprobe` with the bridge's
own credential). A group chat's identity is not in the chat-list entry; it is in `chatMetas` on the
`CHATINFO` answer — a list of room-level settings, each `{type, revision, authorId, content,
updatedAt}`, changed by a member and replayed to everyone:

| `type` | meaning | `content` |
|---|---|---|
| `3` | the room name the members chose | plain text |
| `4` | the room picture (Team Chats can have one) | `{"imageUrl":…₁₁₀, "fullImageUrl":…}` |
| `15` | team state — **its presence is what makes a `MultiChat` a Team Chat** | `{"warehouse":true, "hostId":"…", "revision":N}` |

So a Regular Chat and a Team Chat are the same wire type; only the type-15 meta tells them apart, and
the host is `hostId`. There is no separate role list per member in `GETMEM` — its members carry
`type`/`mt`/`ptp` numbers whose meaning is not established.

**The intro is not on the socket.** A Team Chat can carry a short description (the client calls it
"About team"), and it is nowhere in LOCO: `CHATINFO`, `ChatOnRoom` and `GETMEM` were read for a room
whose intro was set, and the text appeared in none of them (`mcMetas` is an empty array there). The
client asks a second service for it, which its own code names *alcatraz*:

```text
POST https://talk-pilsner.kakao.com/alcatraz/v1/chat/info   {"chatId":<int64>}
→ {"chatId":…, "name":…, "description":…, "hostId":…, "status":"ACTIVE", "revision":N,
   "createdAt":…, "permission":{"INVITE":"STAFF","WRITE":"MEMBER","BOT":"MEMBER"},
   "staff":[…], "restrictions":[…], "accessibleLogId":…, "warehouseBackupStatus":"OFF", …}
```

with the same `authorization: <access_token>-<device_uuid>` credential as the media APIs. An edit is
`POST /alcatraz/v1/chat/update` with `{"chatId":…, "description":…}`, and the client re-reads `chat/info`
immediately after writing it, which is how both halves were captured. A chat that is not a Team Chat — a
Regular Chat or an Open Chat room — answers `404` `{"status":404,"code":"90001"}`; that is the test the
bridge uses rather than guessing. The bridge maps `description` to the Matrix room topic and keeps the
`chatMetas` title as the room name, because that is the name the chat list shows.

A group created **without** a name has no type-3 meta: the phone titles that case from the member list,
locally and in the user's language. It is a client string, not a protocol fact, which is why the bridge
composes its own label rather than quoting one.

Group rooms also carry system messages: `type 0` (`MsgFeed`) whose `message` is a JSON object, not chat
text — `{"feedType":1,"inviter":…, "members":[…], "byLink":false}` for invitations,
`{"feedType":2,"member":…, "kicked":true}` for a departure (`kicked` says removed rather than left),
`{"feedType":4,"members":[…]}` for a member arriving, `{"feedType":18,"eventType":1,
"eventUser":…}` for a team-chat event. They are the client's centred notices, written in the reader's
language, so there is nothing to translate; the room's *state* is the useful part. The bridge treats any
of them as a reason to re-read the chat: the member list is marked full, so bridgev2 turns the difference
into Matrix joins and leaves, and a renamed room or new room picture follows the same path. Unidentified
`feedType` values are logged with the event so they can be recognised later.

A change does not have to wait for history: inviting a member pushes `NEWMEM`, removing one pushes
`DELMEM`, and both bodies are ordinary chat logs of this kind — not `MSG`, which is why membership would
otherwise never reach the bridge live. `loco.ChatIDOfPush` reads the chat from such a body even when the
verb abbreviates the key, and says no rather than guessing a room.

The Team Chat management screen offers roles the wire does not show the bridge: invite/remove members,
manage access, assign or remove an admin, reassign the host. The team service names the *room's* rules
(`permission` above) and its admin list (`staff`, empty on a test room with no admins), but it does not
say which member holds which role, and only `hostId` names a person — so the bridge has no basis for
Matrix power levels beyond the host.

### 7b. Open Chat rooms (`OM`)

An Open Chat room appears in the chat list, and `CHATINFO`/`GETMEM` answer for it — but LOCO gives no
title, no picture and no member ids on the chat itself: `chatInfo` has `activeMembersCount`, an empty
`displayMembers`, the `chatDatas` entry has null `i`/`k`. What it does carry, and only rooms of this
kind carry, are two ids that belong to another service: `li` (the **Open Link** id) and `otk` (its
token). `GETMEM` does list the participants.

The room's profile is kept on that link, and it is read over the same LOCO socket:

| verb | what it does |
|---|---|
| `SYNCLINK` | no arguments. Answers `ols`: every link this account has — rooms it created or joined, plus its own profile link. |
| `UPDATELINK` | writes a room's intro and category: `{li, desc, adid, categoryId}`. |
| `SYNCLINKUP` | push: `{ol: <the link as it now is>}` — how a rename or a new intro reaches every client. |

`INFOLINK`, `GETLINK`, `SYNCLINKPF` and `SYNCLINKDL` were each asked the same `li` and refused it (inner
`-203` or `-300`); `SYNCLINK` is the one that answers, and it does not filter by `li` — asking it with
and without an id returns the same list.

Inside a link object (`loco/openlink.go`):

| key | meaning |
|---|---|
| `li` | the link id — the number `CHATINFO` puts in `li` |
| `ln` | the room name members see → Matrix room name |
| `desc` | the room intro, multi-line (the client allows 200 characters) → Matrix room topic |
| `liu` | the room picture, full size → Matrix room avatar |
| `lu` | the public join URL (`open.kakao.com/o/…`) |
| `lt` | what the link is: `8` is a chatroom, `1` is a person's own open-profile link |
| `otk` | link token; it changes when the profile is written |
| `olu` | the link owner's **open profile** (`nn`, `pi`/`fpi`/`opi`, `pli`) |
| `ml`, `omt.tags`, `category{id,name{ko,en,ja},icon}` | member limit, tags, category |

Two things matter when using that answer:

* It mixes the account's own profile link in with its rooms — in one measured answer the first entry was
  the account's profile link, its `ln` equal to the account's nickname. An entry is only used when its
  `li` is the one the chat named.
* Open Chat participants appear under an **open profile**, not the account's main profile: `olu` is a
  separate nickname and image set, kept precisely because main profiles carry real names. The bridge reads
  participants from `GETMEM` (§7a), which reports those open profiles; a main profile never appears in an
  Open Chat room.

The earlier leads are closed: the WebView at `open.kakao.com/c/link/…` is server-rendered Nuxt whose HTML
contains no room data, `c/link/settings` only answers categories and join requirements, and the REST
paths guessed under `openchat/v1/chats/…` answer `500` — the gateway's answer for a route it does not
have. The feed endpoints in that family (`openchat-tab/v1/tab/now`) answer `601` for a young account
without identity verification, which does not affect any of the above. The bridge keys the Matrix portal
by the Kakao `chatId` and only uses `li` to look the profile up.

## 8. Chat logs and attachment JSON

`loco/roominfo.go` (`ParseChatLogs`), `loco/attach.go`.

`MCHATLOGS` (per chat, paged by `sinces`) and `CHATLOGS` return `chatLogs` plus an `eof` marker. An
attachment is a JSON blob whose keys are abbreviations (`rsc`, `hfac`, `cs`, `name`, `du`, `pl`…);
`AttachmentMetaOf` decodes them and explains which key means what. Two practical traps:

* the CDN URLs in old logs are **signed and expire** (`expires`/`expire`), so an old photo may be
  unfetchable even though the log entry is fine;
* the numeric `attachType` values are what the client uses to choose a renderer; `attach.go` maps the
  ones this bridge converts and refuses the rest rather than guessing.

## 9. Sending: `WRITE` and `WRITEMEDIA`

`loco/session.go`.

`WRITE` is a text message: `chatId`, `message`, `type`, `attachType`, `extra`. `WRITEMEDIA` is the
media commit and its body differs by kind — measured from the phone and re-verified with the bridge's
own credential (`loco/session_test.go` decodes what the session actually puts on the wire):

| kind | `type` | media subdoc |
|---|---|---|
| photo | `2` | `photo{resource, resourceType, hfac, width, height}` + `extra={"cmt":""}` |
| video | `3` | `video{resource, resourceType, hfac}` — nothing else; Kakao computes dimensions and duration |
| file | `18` | `file{resource, resourceType, hfac, checksum, name}` where `checksum` is the **SHA-1** of the bytes |

`msgId` is chosen by the client (`ClientMsgID`); `hfac` is the `uploadKey` from the upload ticket (§10).

**Kakao has no caption on a photo.** A non-empty `extra.cmt` on a photo commit is ignored: the stored
chat log shows placeholder text (`text: "사진"`, `alt: ""`). Any caption a Matrix user wrote therefore
has to arrive as its own `WRITE`.

## 10. The media upload contract

`kakao/media.go`.

Three steps, and none of them is `SHIP` (which answers with an empty ticket for these kinds):

1. **Ticket** — `POST https://talk-pilsner.kakao.com/messaging/files/v1/prepare`, with
   `authorization: <access_token>-<device_uuid>` (a bare token is rejected with 401). The answer gives
   a signed upload URL, `resource`, `resourceType`, `chunkSize`, `uploadKey`, and `needReady`.
2. **Upload** — `multipart/form-data` to the signed URL: a `file_1` part holding the bytes, an `offset`
   part, and `finalize=true` on the last chunk only. Chunks are sized by `chunkSize`; the same URL is
   reused for each chunk. The part's `Content-Type` must be the concrete media type.
3. **Commit** — the LOCO `WRITEMEDIA` above, with `hfac` = `uploadKey`.

`mediaType` and the extension are gated server-side: a file with an extension on Kakao's disallow list
comes back as `EXTENSION_NOT_ALLOWED` (an `.apk` does), and a wrong `mediaType` as
`INVALID_MEDIA_TYPE`. Quote the gateway's reason rather than inventing one.

When `prepare` answers `needReady: true` (video does), the commit right after the upload is answered
with **`-306`** and the *identical* body a few seconds later is accepted; the upload reply is just
`{"msg":"ok"}`, so retrying the commit is the readiness mechanism (`commitOnceReady`).

Video is re-encoded server-side: the stored log shows `talkv_high.mp4` with its own dimensions and
duration, regardless of what the sender sent — which is why webm/mkv/mov are bridged but marked
partially supported.

## 11. Sticker and emote assets

`loco/digitalitem.go`, `kknet/media.go`.

Kakao's animated emoticons are ordinary **animated WebP** files whose first 128 bytes are encrypted
with a fixed key embedded in the app (`DecryptItemAsset`). Decrypted, they are real animated images and
can be uploaded to Matrix as-is; the item metadata carries no dimensions, so the bridge reads the
canvas size out of the VP8X chunk.

## 12. Client constants

`network.*`, `Config.Fill`.

`app_version`, `language`, `mccmnc`, `os_version`, `X-VC` are the fields the shipped clients send, and
Kakao cross-checks them: `app_version` must match the build that minted the device's tokens, and calls
that look older than the account's client are rejected. They are defaults measured from one specific
device and app build, not universal constants — when something breaks with no protocol change, this is
the first thing to compare against a current client.

## 13. Verbs the client knows and this bridge does not use

The mobile client's route table lists more LOCO verbs than this bridge calls (`loco/client.go`
comments). The unimplemented ones are the right verbs, not guesses — they simply have no
implementation behind them yet. `cmd/locoprobe` exists to test a verb body by hand before anyone writes
Go for it.

## 14. Re-measuring anything here

```bash
go run ./cmd/locoprobe -tokens <tokens.json> -- 'MCHATLOGS chatIds:i64list=… sinces:i64list=…'
go run ./cmd/kkdump -hex "$(cat body.hex)" -full        # decode a captured body
go run ./cmd/bsondump body.bin                          # raw BSON walk of any response
go run ./cmd/kksessions                                 # LOCO chat list vs the drawer REST
go run ./cmd/kkrenew                                    # does this credential still renew itself?
```

The response bodies `loco/testdata` and `testdata` would normally hold are traffic from a personal
account, so they are not published; each `testdata/README.md` says how to record your own.
