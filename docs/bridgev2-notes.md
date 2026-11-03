# Notes on the mautrix `bridgev2` layer

Contracts in [mautrix-go](https://maunium.net/go/mautrix)'s `bridgev2` that this bridge has to respect,
each with the symptom you see when you don't. Useful if you are adding a feature here; not a substitute
for mautrix's own [bridge docs](https://docs.mau.fi/bridges/).

## `GetCapabilities` is a gate, not a hint

If `RoomFeatures` does not claim a message type, bridgev2 answers the user itself —

> ⚠️ Your message was not bridged: unsupported message type

— and `HandleMatrixMessage` is never called, so there is no `kknet:` log to follow. Claim exactly what
the send path implements: `TestCapabilitiesMatchWhatCanBeSent` fails if the two drift. A type that is
bridged with a loss (a caption sent as a second message, a container Kakao re-encodes) is
`CapLevelPartialSupport`, and a field that is dropped is `CapLevelDropped`.

One blind spot: an event with no `info.mimetype` cannot be matched against a capability, so it is
refused before reaching the connector.

## Message identity, and why one Kakao message needs two rows

The `message` table has two unique constraints: `(bridge_id, mxid)` and
`(bridge_id, room_receiver, id, part_id)`. Inbound dedup asks `GetAllPartsByID(receiver, remoteID)`, so
every remote message that will be echoed back must have a row before it arrives.

Sending a Matrix photo with a caption produces **two** Kakao messages (Kakao has no photo captions —
`docs/protocol-notes.md` §9), and the caption echo would otherwise appear as a new Matrix message. It
gets its own row for its own `logId`, written in `MatrixMessageResponse.PostSave` — after bridgev2 saved
the photo's row, because writing it before that races with the framework over the same MXID:

> pq: duplicate key value violates unique constraint "message_mxid_unique"

The caption's row carries a synthetic MXID (`$kakaobridge-caption-<logId>`) that is never sent to the
homeserver; it exists only so the echo is recognised as a duplicate.

## Read receipts and those synthetic rows

A `DECUNREAD` names a `logId`; the bridge picks the nearest older *bridged* message. A caption is not a
Matrix event, so a caption's log id must not be offered as a receipt target, or the homeserver answers

> M_NOT_FOUND (HTTP 404): Event not found.

The caption's dedup row and its receipt-target entry are therefore deliberately different things.

## bridgev2 does not connect a login that a login flow created

`NetworkAPI.Connect` is called for logins the framework *loads* — at startup, and again after an
`UNKNOWN_ERROR` bridge state. It is **not** called for a login created by a successful login flow:
`User.NewLogin` fills `UserLogin.Client` and returns, and its documentation expects `Connect` to happen
later. Nothing does it. So the connector starts it itself, in the background, right after `NewLogin`
(`kknet/login.go`) — the login command should answer first, and the room should then watch the portals
appear.

Because one Kakao device credential carries exactly one LOCO session, `Client.Connect` is guarded
(`claimConnect`/`endConnect`): a second concurrent connect would `KICKOUT` the first.

`Connect` must also not return an error for ordinary connection trouble — report it as bridge state
(`TRANSIENT_DISCONNECT`, …) and retry, or the login gets torn down for a network blip.

## Room names and avatars the framework cannot infer

* A DM is normally named from the *other* member's ghost. A Kakao MemoChat (the self-memo chat) has no
  other member, so the connector supplies `ChatInfo.Name` and `ChatInfo.Avatar` — Kakao itself titles
  that chat after the account's nickname.
* Member profile images come in three sizes; a room avatar should use `fullProfileImageUrl`, not the
  110×110 thumbnail (`docs/protocol-notes.md` §7).
* A personal-filtering space's icon comes from `BridgeName.NetworkIcon`. Supply it, and fix up spaces
  that already exist (reading current room state needs `MatrixConnectorWithArbitraryRoomState`, as
  `MatrixAPI` has no `GetStateEvent`). Use a PNG: SVG room avatars render inconsistently across
  clients.
* The bridge **bot's** display name and avatar are operator config (`appservice.bot.*`). `avatar` must
  be an `mxc://` URI, and the literal string `remove` clears the picture. The bridge ships a default
  avatar (hosted on the author's homeserver, as `mxc://` URIs are tied to one server); set your own or
  `remove` if you would rather not depend on it.

## Identifiers that cannot change later

`BridgeID` (`kakaotalk`) keys every row in the database and appears in `m.bridge` events. Login step ids
must be stable across logins of the same flow and unique to this bridge. Both are load-bearing; changing
them means a database migration.
