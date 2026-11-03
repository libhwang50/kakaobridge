# Notes for agents working in this repository

KakaoTalk ↔ Matrix bridge: `bridgev2` (mautrix-go) owns the Matrix side, this repository owns
KakaoTalk. Read `README.md` for what it does, and these two before touching protocol or framework code —
they are the contract, written from measurements:

* `docs/protocol-notes.md` — what Kakao's REST + LOCO protocols actually do, section by section.
* `docs/bridgev2-notes.md` — the mautrix contracts this bridge relies on and the symptoms of breaking them.

## Commands

```bash
make build      # go build -tags goolm ./...
make test       # go test  -tags goolm ./...
make vet
```

* **Always `-tags goolm`** (the Makefile sets it). A plain `go build ./...` fails with
  `fatal error: olm/olm.h: No such file or directory`: mautrix's crypto defaults to C libolm.
* CGO stays on (mautrix links `go-sqlite3`). If the link step says `disk quota exceeded`, the Makefile's
  `GOTMPDIR=/dev/shm` is the fix — keep it.
* There is no lint step. `gofmt -l .` must be empty before committing.
* Tests never need the network, a homeserver, or Kakao. What they cover is the decisions - the conversion
  functions, the capability list, the config and upgrade rules, the read-receipt target choice - plus one
  wire level: `loco/session_test.go` drives a real `Session` over a `net.Pipe()` and decodes what it
  actually sends, which is why changing a `WRITE`/`WRITEMEDIA` body must come with a change there.
  There is no fake `loco.Session`/`kakao.Client` in the tree; `loco.Session` is a struct, not an
  interface. If a new test genuinely needs to fake one, introduce the interface deliberately rather than a
  one-off stub.

## Map

| path | role |
|---|---|
| `kknet/` | the bridgev2 connector: login flows, the LOCO pump, portal/ghost/room handling, Matrix→Kakao sending, capabilities, room branding |
| `kakao/` | account REST (registration, token renewal, `prepare` upload tickets) |
| `loco/` | the LOCO session: frames, BSON, login, `WRITE`/`WRITEMEDIA`, chat-log and attachment parsing |
| `cmd/kakaobridge/` | the bridge binary (`mxmain` entry point, default bot profile) |
| `cmd/locoprobe`, `cmd/locoauth`, `cmd/kkdump`, `cmd/kkread`, `cmd/kksessions`, `cmd/kkrenew`, `cmd/bsondump` | protocol probes, not part of the bridge; they are how a claim gets measured before it becomes code |
| `docs/` | the durable explanations. `README.md` is the front page, not the place for detail |

## Non-negotiables

1. **Do not invent protocol identifiers.** Resource paths, `mediaType` values, `hfac`, `checksum`, field
   names, ports: they must come from a measured client flow or a gateway response. Measure first with
   `cmd/locoprobe` (types spelled out — LOCO answers `status -300` for the wrong BSON type) and read the
   body with `cmd/kkdump`/`cmd/bsondump`. Then write the code, then cite the section of
   `docs/protocol-notes.md` you implemented.
2. **Claim only what the send path implements.** `GetCapabilities` gates everything: if it drifts from
   reality, users get mautrix's `unsupported message type` before our code runs.
   `capabilities_test.go` fails when they diverge — treat that as the intended behaviour, not an obstacle.
3. **One LOCO session per device credential.** Starting a second one `KICKOUT`s the first. A probe run
   with the bridge's tokens will kick the running bridge; a second bridge instance on the same Kakao
   account cannot work at all (Kakao allows one mobile sub-device per account).
4. **Frozen identifiers:** `BridgeID` (`kakaotalk`), the `cc.libhwang.*` login-step ids, the module path,
   and config key names. Changing any of them is a database migration, not a rename.
5. **Message identity has three rules** (details in `docs/bridgev2-notes.md`): one remote message = one
   DB row before its echo comes back; secondary rows are written in `PostSave` with a synthetic MXID;
   synthetic rows are never read-receipt targets.
6. **No personal data in the repository.** No real names, phone numbers, or account-specific ids in code,
   comments, fixtures, or commit messages — use the synthetic forms already in use (`821012345678`,
   `tokens.json`, `example.org`). Captured Kakao traffic is gitignored and the tests that want it
   `t.Skip` when it is absent; keep it that way, and never hand-edit a binary/hex capture.
7. **Comments may only refer to this repository** (`docs/…`, `cmd/…`, package names). The research
   material this bridge grew out of lives in a separate private tree (`findings/…`, `tools/*.py`,
   `apk/jadx/…`) — it is not a citation here. Keep comments to ~100 columns, and say *why*, with the
   evidence, the way the existing ones do.
8. Commit messages explain the reason and the symptom that motivated the change; a bare "fix" is not
   useful six months later. `git log` on `main` is intentionally short — do not push the research history
   here (it lives on the `dev` branch of the private remote).

## Against the live deployment

The bridge runs as a Docker Compose service named `kakaobridge` (image `local/kakaobridge:v2`) beside the
homeserver and Postgres, in one compose project with fixed addresses.

* Deploy by git: push, then on the server `git fetch && git reset --hard origin/main` and build the image.
  Never rsync over the deployment checkout — it leaves HEAD behind and makes a deployed fix look
  undeployed.
* Watch it with `docker logs kakaobridge` and the management room's state message, not by restarting it.
* Don't wipe the bridge database to debug something: a login is one row in `user_login` holding a Kakao
  device that may be the account's only mobile-sub-device slot. Read it instead:
  `psql -U mautrix -d kakaobridge -c 'select id, user_mxid, remote_name from user_login'` (the credential
  and account password are in its `metadata` jsonb).
* Working looks like: `LOCO session online with a chat list …` → `Chat list bridged: … rooms=N` →
  `"state_event":"CONNECTED"`, `restarts=0`. A login made through `login` must reach that without a
  restart.

## Deliberately not implemented

So they are not "discovered" as bugs: voice notes from Matrix to Kakao (the commit body has never been
measured), Matrix→Kakao read receipts, edits/deletions in either direction, creating a Kakao chat from
Matrix, and outbound presence. Known gaps worth picking up: the bridge can read a `tokens_file` but has no
way to **export** one for a device it registered itself; Kakao voice-note commits; more `attachType`s.

## Docs and translations

`README.md` and `docs/*.md` in English are the source of truth. If translations are added, keep them
beside the original (`docs/ko/…`, `README.ko.md`) and treat them as derived: never edit a translation to
change a fact — change the English and note which translations need refreshing. Protocol facts must match the code, and the code must
match `docs/protocol-notes.md`; if you find a disagreement, the measurement wins — re-measure and fix
both.
