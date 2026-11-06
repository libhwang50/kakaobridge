# kakaobridge

[한국어](./docs/README.ko.md)

A Matrix ↔ [KakaoTalk](https://www.kakao.com) puppeting bridge, built on
[mautrix-go](https://maunium.net/go/mautrix)'s `bridgev2`.

KakaoTalk has no public bot or messaging API, so this bridge speaks the protocol the shipped apps
speak: the LOCO chat socket and the account REST endpoints, reverse-engineered from the Android and
Windows clients (`loco`, `kakao`, `media`). On the Matrix side it is an ordinary appservice, so
portals, ghosts, encryption, the management room and provisioning come from `bridgev2` rather than
from this repository.

Signing in registers the bridge as a **mobile sub-device** of your Kakao account: it shows a 4-digit
code, you type that code in KakaoTalk on a device that is already signed in, and the bridge then holds
a credential it can renew by itself. That is also the constraint to know up front — Kakao allows **one
mobile sub-device per account**, so one Kakao account can be bridged by one bridge instance.

## What bridges today

| | KakaoTalk → Matrix | Matrix → KakaoTalk |
|---|---|---|
| text | ✅ | ✅ (`m.text`, `m.notice`, `m.emote`) |
| photos, stickers | ✅ (animated stickers stay animated) | ✅ JPEG/PNG/GIF/WebP |
| video, files | ✅ | ✅ mp4 fully; webm/mkv/mov are re-encoded by Kakao; files depend on Kakao's own extension list |
| voice notes | ✅ | ❌ not implemented |
| captions | — | ⚠️ Kakao has no caption on a photo, so a Matrix caption arrives as its own message |
| read receipts | ✅ (`m.read`) | ❌ not implemented |
| replies | ✅ (`m.in_reply_to` — Kakao keeps the quote out of the text and points at the message it quotes) | ❌ not implemented |
| edits | ✅ (an edit of that same message; Kakao pushes the whole new text) | ❌ not implemented |
| deletions | ⚠️ applied when that chat's history is read again — Kakao sends no push for a delete | ❌ not implemented |
| text styles (bold, italic, strikethrough, spoilers) | ✅ (Kakao keeps them as ranges beside the plain text) | ✅ the marks in the message's HTML are read and sent as those ranges, so Markdown typed in a Matrix client shows up styled in Kakao |
| reactions | ✅ read from Kakao, including who reacted — they appear when the bridge next reads that room, because Kakao sends no push for a reaction. Kakao's reactions are mini emoticons, so the character shown is a stand-in and the item behind it travels with the event | ❌ not implemented (Kakao's write call has not been measured) |
| history | ✅ the chat list and its recent messages are bridged on login | — |
| rooms | 1:1 chats, the self-memo chat, **group chats** (Regular and Team: the name and picture the members gave them, and for a Team Chat the intro the host wrote, which becomes the room topic), **Open Chat rooms** (name, picture, and the room's intro as the topic — Kakao keeps those on the room's Open Link rather than on the chat, and the bridge reads them) | ⚠️ creating a Kakao chat from Matrix is not implemented |
| group joins and leaves | ✅ as room membership (Kakao sends them as a notice in the reader's language, so the notice text is not bridged) | ⚠️ a Matrix join or leave is not announced in Kakao |
| the people in a room | ✅ names and pictures, taken from that room's member list; someone a big room's list did not carry is asked about in the room they spoke in, which is how an Open Chat room shows its open profiles | ⚠️ Kakao never pushes a profile change, so a person's ghost is refreshed when that room is read again |

End-to-end encryption, double puppeting (`login-matrix`), relay mode and the multi-user state machine
are mautrix features and work here as they do in other bridgev2 bridges.

## Requirements

* A homeserver with appservice support (Tuwunel, Synapse, …).
* PostgreSQL — that is what this bridge is developed and run against; mautrix also speaks SQLite and
  it should work, but it is not tested here.
* A Kakao account, and one device already signed in to KakaoTalk where you can type the 4-digit code.
* Go 1.27 or Docker.

## Getting started

```bash
git clone https://github.com/libhwang50/kakaobridge && cd kakaobridge
make build                                   # -tags goolm: pure-Go olm, no libolm needed
./kakaobridge --config config.yaml --generate-example-config
$EDITOR config.yaml                          # database, homeserver, and the network: section below
./kakaobridge --config config.yaml --generate-registration   # writes registration.yaml
```

Install `registration.yaml` into your homeserver the usual way
(see [registering appservices](https://docs.mau.fi/bridges/general/registering-appservices.html)), then
start the bridge and open a DM with the bot:

```
login
```

The bot asks for your Kakao account id and password, then shows a 4-digit code to type in KakaoTalk on
a device that is already signed in. When that is approved, the bridge starts a session, brings in your
chat list, and invites you to the rooms.

### With Docker

```dockerfile
# docker-compose.yaml (sketch)
services:
  kakaobridge:
    build: .
    volumes:
      - ./data:/data
    restart: unless-stopped
```

The image builds as uid 65532 (`nonroot`), so everything it writes under `/data` — `config.yaml`,
`registration.yaml` — belongs to that user.

### Configuration

The interesting part is the `network:` section; everything else is mautrix's own
[bridge config](https://docs.mau.fi/bridges/go/setup.html). `docs/configuration.md` documents both, and
writes the example config to disk with `--generate-example-config`.

| key | default | what it does |
|---|---|---|
| `ask_for_credentials` | `false` | the login flow asks for account id and password in the management room. Either way they are needed: if they are not in the config, the bridge asks. With `false` and both set here, a second Matrix user cannot log in. |
| `account` / `password` | — | your Kakao account. The password is not only for the first login: Kakao renews a **registered** device with the account password, so the bridge needs it to keep the session alive. It is stored in the database against your login. |
| `agent` | `android` | `android` registers a durable mobile sub-device that can renew itself; `win32` is the PC-client namespace, whose sessions are throwaway. There is no reason to use `win32` unless you are experimenting. |
| `app_version` | `26.9.1` | the client build to announce. It must match the build the device was registered with; Kakao rejects a mismatch. This is also the build the protocol was measured against — if Kakao changes something, this is the first thing to check. |
| `device.name` | `kakaobridge` | what your account's device list shows on the phone. |
| `device.model`, `device.os_version`, `x_vc`, `language`, `mccmnc` | sensible defaults | the fields the shipped clients send. `mccmnc` defaults to a Korean carrier code; set it if you are not in Korea. |
| `tokens_file`, `tokens_mxid` | empty | import an already-registered device instead of running the passcode flow again — the JSON written by `cmd/locoauth`. Kakao allows one mobile sub-device per account, so after wiping the database you either import the old identity or re-register. |

The bot's Matrix display name and avatar are mautrix's `appservice.bot.displayname` /
`appservice.bot.avatar` (an `mxc://` URI; `remove` clears it). A default avatar ships with the bridge;
setting a value overrides it.

## What can go wrong

The bridge reports its state to the management room (`CONNECTED`, `TRANSIENT_DISCONNECT`,
`BAD_CREDENTIALS`, …) with what it means in plain text. The ones worth knowing:

* **`status 12`** — Kakao did not accept the account id or password. Usually a stale `network.password`.
* **`-100`** — the device is no longer usable: it was deregistered, or the account approved *another*
  mobile device and this one was replaced. Re-run `login`.
* **`KICKOUT`** — something else started a session with the same device credential. One session per
  device, so stop the other one (running `cmd/locoprobe` with your bridge's tokens will do exactly
  this).
* **Your homeserver cannot reach the appservice** — usually the appservice URL points at an address
  that no longer exists after a restart. Give the container a stable address, or point the
  registration at it.
* **Test failures about missing `testdata`** — the captures under `*/testdata/` are traffic from a
  personal account and are deliberately not published; those tests skip. `loco/testdata/README.md`
  explains how to record your own.

## Documentation

* `docs/configuration.md` — every `network:` key, and the homeserver/appservice keys that matter.
* `docs/deployment.md` — building, the container, compose layout, registering the appservice, what
  survives wiping the database.
* `docs/troubleshooting.md` — symptoms to causes.
* `docs/protocol-notes.md` — what Kakao's protocols actually do, as measured, and which code depends on
  it. This is the contract the implementation follows; Kakao's protocol is undocumented and can change
  without notice, so compare against a current client when something breaks.
* `docs/bridgev2-notes.md` — the mautrix framework contracts this bridge relies on, for anyone extending
  it.

## Contributing

`AGENTS.md` is the brief for humans and agents working on this: the commands with their required flags,
where the measured protocol contracts live, the rules about measuring a protocol fact instead of guessing
it, and what is deliberately unimplemented so it isn't "discovered" as a bug.

## Related work

These are worth knowing about, whether you are choosing what to run or what to read next. What
follows is what their repositories said on 2026-11-05; ask them for their current state.

- **[mooo](https://github.com/frrad/mooo)** (MIT) — another mautrix bridgev2 KakaoTalk bridge with
  its own LOCO implementation and a large body of protocol research written against owned test
  accounts. It bridges message reactions and replies with attachments, which this bridge does not.
  This one bridges Kakao's text styles in both directions and names an Open Chat room the account
  owns, which mooo has not reached yet.
- **[kakao-beeper](https://github.com/RealLunarProductions/kakao-beeper)** (MIT) — a small bridgev2
  connector for Beeper that keeps the Kakao session in a Node sidecar built on
  [agent-messenger](https://github.com/agent-messenger/agent-messenger), for when you would rather
  the protocol were spoken by a JavaScript client.
- **[noa](https://github.com/entworacy/noa)** — not a bridge: a service that runs on a rooted
  Android and hooks the running KakaoTalk app, covering Open Chat management, reactions and voice
  calls. A different answer to the same protocol, and a good read for what the app itself will do.

Nothing here is copied from them, and no protocol fact in this repository comes from them: what
[docs/protocol-notes.md](docs/protocol-notes.md) claims was measured with the account and the
clients described there.

## Not affiliated with Kakao

KakaoTalk is a trademark of KakaoCorp. This project is not affiliated with, endorsed by, or sponsored by
them.

## Generative AI usage disclosure

This project was AI-assisted with locally hosted Qwen3.8-Flash-Next.

## License

AGPL-3.0-or-later. See [LICENSE](LICENSE).
