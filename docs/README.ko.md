# kakaobridge

[English](../README.md)

Matrix ↔ [KakaoTalk](https://www.kakao.com) 퍼펫팅(puppeting) 브리지입니다. [mautrix-go](https://maunium.net/go/mautrix)의 `bridgev2`를 기반으로 구축되었습니다.

KakaoTalk은 공개된 봇이나 메시징 API가 없기 때문에, 이 브리지는 실제 앱들이 사용하는 프로토콜을 그대로 사용합니다. 안드로이드 및 윈도우 클라이언트(`loco`, `kakao`, `media`)를 역공학하여 얻어낸 LOCO 채팅 소켓과 계정 REST 엔드포인트를 사용합니다. Matrix 측면에서는 일반적인 앱서비스(appservice)이므로, 포털, 고스트, 암호화, 관리 룸 및 프로비저닝 기능은 이 저장소가 아닌 `bridgev2`에서 제공됩니다.

로그인하면 브리지가 귀하의 카카오 계정의 **모바일 서브 디바이스**로 등록됩니다. 4자리 코드가 표시되면, 이미 로그인되어 있는 기기의 카카오톡에서 해당 코드를 입력하세요. 그러면 브리지는 스스로 갱신할 수 있는 인증 정보를 보유하게 됩니다. 미리 알아두어야 할 제약 사항은, 카카오는 **계정당 하나의 모바일 서브 디바이스**만 허용한다는 점입니다. 따라서 하나의 카카오 계정은 하나의 브리지 인스턴스만 연결할 수 있습니다.

## 현재 지원되는 기능

| | KakaoTalk → Matrix | Matrix → KakaoTalk |
|---|---|---|
| 텍스트 | ✅ | ✅ (`m.text`, `m.notice`, `m.emote`) |
| 사진, 스티커 | ✅ (움직이는 스티커도 유지됩니다) | ✅ JPEG/PNG/GIF/WebP |
| 비디오, 파일 | ✅ | ✅ mp4는 완벽 지원; webm/mkv/mov는 카카오에 의해 재인코딩됨; 파일은 카카오의 자체 확장자 목록에 따라 달라집니다 |
| 음성 메시지 | ✅ | ❌ 미구현 |
| 캡션 | — | ⚠️ 카카오에는 사진에 대한 캡션 기능이 없으므로, Matrix 캡션은 별도의 메시지로 전달됩니다 |
| 읽음 확인 | ✅ (`m.read`) | ❌ 미구현 |
| 수정, 삭제 | ❌ | ❌ |
| 히스토리 | ✅ 로그인 시 채팅 목록과 최근 메시지가 브릿지됩니다 | — |
| 채팅방 | 1:1 채팅, 나에게 보내기 채팅, **그룹 채팅** (일반 및 팀 채팅, 멤버들이 설정한 이름과 사진, 팀 채팅의 경우 호스트가 작성한 소개가 룸 토픽으로 포함), 오픈채팅방 (메시지와 멤버 — 카카오는 채팅 프로토콜을 통해 오픈채팅에 대한 이름을 보내지 않습니다) | ⚠️ Matrix에서 카카오 채팅방을 생성하는 기능은 미구현 상태입니다 |
| 그룹 입장 및 퇴장 | ✅ 룸 멤버십으로 처리 (카카오는 이를 읽는 사람의 언어로 된 공지로 보내므로, 공지 텍스트 자체는 브릿지되지 않습니다) | ⚠️ Matrix의 입장/퇴장은 카카오에 알림되지 않습니다 |

종단간 암호화(E2EE), 더블 퍼펫팅(`login-matrix`), 릴레이 모드 및 멀티 유저 상태 머신은 mautrix의 기능이며, 다른 bridgev2 브리지들과 동일하게 작동합니다.

## 요구 사항

* 앱서비스를 지원하는 홈서버 (Tuwunel, Synapse, …).
* PostgreSQL — 이 브리지는 PostgreSQL을 기준으로 개발 및 실행됩니다. mautrix는 SQLite도 지원하므로 작동할 수 있지만, 여기서는 테스트되지 않았습니다.
* 카카오 계정, 그리고 4자리 코드를 입력할 수 있는 이미 로그인된 카카오톡 기기 하나.
* Go 1.27 또는 Docker.

## 시작하기

```bash
git clone https://github.com/libhwang50/kakaobridge && cd kakaobridge
make build                                   # -tags goolm: libolm 없이 순수 Go로 구현된 olm 사용
./kakaobridge --config config.yaml --generate-example-config
$EDITOR config.yaml                          # 데이터베이스, 홈서버, 네트워크 설정 (아래 섹션 참조)
./kakaobridge --config config.yaml --generate-registration   # registration.yaml 작성
```

`registration.yaml`을 평소처럼 홈서버에 설치하고([앱서비스 등록하기](https://docs.mau.fi/bridges/general/registering-appservices.html) 참조), 브리지를 시작한 뒤 봇과 DM을 여세요:

```
login
```

봇이 카카오 계정 ID와 비밀번호를 요청한 뒤, 이미 로그인된 기기의 카카오톡에 입력할 4자리 코드를 보여줍니다. 승인이 완료되면 브리지가 세션을 시작하고, 채팅 목록을 가져오며, 채팅방으로 초대합니다.

### Docker 사용 시

```dockerfile
# docker-compose.yaml (예시)
services:
  kakaobridge:
    build: .
    volumes:
      - ./data:/data
    restart: unless-stopped
```

이미지는 uid 65532 (`nonroot`)로 빌드되므로, `/data` 아래에 작성되는 모든 것(`config.yaml`, `registration.yaml` 등)은 해당 사용자의 소유가 됩니다.

### 설정 (Configuration)

중요한 부분은 `network:` 섹션입니다. 그 외의 모든 것은 mautrix의 [브리지 설정](https://docs.mau.fi/bridges/go/setup.html)을 따릅니다. `docs/configuration.md`에 두 설정 모두에 대한 설명이 있으며, `--generate-example-config` 명령어로 예시 설정 파일을 생성할 수 있습니다.

| 키 | 기본값 | 설명 |
|---|---|---|
| `ask_for_credentials` | `false` | `true`인 경우 관리 룸에서 계정 ID와 비밀번호를 묻는 로그인 흐름이 진행됩니다. 어느 쪽이든 정보는 필요합니다. 설정에 없으면 브리지가 물어봅니다. `false`로 설정하고 두 정보가 모두 있으면, 두 번째 Matrix 사용자는 로그인할 수 없습니다. |
| `account` / `password` | — | 귀하의 카카오 계정. 비밀번호는 첫 로그인에만 쓰이는 것이 아닙니다. 카카오는 **등록된** 기기를 계정 비밀번호로 갱신하므로, 브리지가 세션을 유지하려면 비밀번호가 필요합니다. 이는 데이터베이스에 로그인 정보와 함께 저장됩니다. |
| `agent` | `android` | `android`는 스스로 갱신할 수 있는 지속적인 모바일 서브 디바이스를 등록합니다. `win32`는 PC 클라이언트 네임스페이스로, 세션이 일회성입니다. 실험 중이 아니라면 `win32`를 사용할 이유는 없습니다. |
| `app_version` | `26.9.1` | 알릴 클라이언트 빌드 버전입니다. 기기가 등록될 때의 빌드와 일치해야 합니다. 카카오는 불일치를 거부합니다. 또한 이 버전은 프로토콜을 측정할 때 사용한 버전이기도 합니다. 카카오가 무언가를 변경했다면 가장 먼저 확인해야 할 부분입니다. |
| `device.name` | `kakaobridge` | 휴대폰의 계정 기기 목록에 표시될 이름입니다. |
| `device.model`, `device.os_version`, `x_vc`, `language`, `mccmnc` | 합리적인 기본값 | 실제 클라이언트가 보내는 필드들입니다. `mccmnc`는 한국 통신사 코드가 기본값입니다. 한국이 아니라면 직접 설정하세요. |
| `tokens_file`, `tokens_mxid` | 비어 있음 | passcode 흐름을 다시 거치는 대신 이미 등록된 기기를 가져옵니다 — `cmd/locoauth`가 작성한 JSON 파일입니다. 카카오는 계정당 하나의 모바일 서브 디바이스만 허용하므로, 데이터베이스를 초기화했다면 이전 ID를 가져오거나 새로 등록해야 합니다. |

봇의 Matrix 표시 이름과 아바타는 mautrix의 `appservice.bot.displayname` / `appservice.bot.avatar` (`mxc://` URI; `remove`로 삭제 가능)를 따릅니다. 기본 아바타가 포함되어 있으며, 값을 설정하면 덮어씁니다.

## 문제가 발생할 경우

브리지는 관리 룸에 현재 상태(`CONNECTED`, `TRANSIENT_DISCONNECT`, `BAD_CREDENTIALS`, …)를 텍스트로 보고합니다. 알아두면 좋은 상태들은 다음과 같습니다:

* **`status 12`** — 카카오가 계정 ID 또는 비밀번호를 수락하지 않았습니다. 보통 오래된 `network.password`가 원인입니다.
* **`-100`** — 기기를 더 이상 사용할 수 없습니다: 등록이 해제되었거나, 계정에서 *다른* 모바일 기기를 승인하여 이 기기가 교체되었습니다. `login`을 다시 실행하세요.
* **`KICKOUT`** — 다른 무언가가 동일한 기기 인증 정보로 세션을 시작했습니다. 기기당 하나의 세션만 가능하므로, 다른 세션을 중지하세요 (브리지의 토큰으로 `cmd/locoprobe`를 실행하면 정확히 이 작업이 수행됩니다).
* **홈서버가 앱서비스에 도달할 수 없음** — 보통 재시작 후 앱서비스 URL이 더 이상 존재하지 않는 주소를 가리키고 있을 때 발생합니다. 컨테이너에 고정된 주소를 부여하거나, 등록 정보를 해당 주소로 변경하세요.
* **`testdata` 누락에 관한 테스트 실패** — `*/testdata/` 아래의 캡처 파일들은 개인 계정의 트래픽이며 의도적으로 공개하지 않았습니다; 따라서 해당 테스트들은 건너뜁니다(skip). `loco/testdata/README.md`에서 직접 기록하는 방법을 설명합니다.

## 문서 (Documentation)

* `docs/configuration.md` — 모든 `network:` 키와 중요한 홈서버/앱서비스 키에 대한 설명.
* `docs/deployment.md` — 빌드, 컨테이너, compose 레이아웃, 앱서비스 등록, 데이터베이스 초기화 시 유지되는 것들.
* `docs/troubleshooting.md` — 증상과 원인 매칭.
* `docs/protocol-notes.md` — 측정된 카카오 프로토콜의 실제 동작과 코드가 이에 어떻게 의존하는지에 대한 내용. 이것은 구현이 따르는 계약(contract)입니다. 카카오의 프로토콜은 문서화되어 있지 않고 예고 없이 변경될 수 있으므로, 무언가 깨졌을 때는 현재 클라이언트와 비교해 보세요.
* `docs/bridgev2-notes.md` — 이 브리지를 확장하려는 사람들을 위한, mautrix 프레임워크 계약.

## 기여하기 (Contributing)

`AGENTS.md`는 이 프로젝트에서 작업하는 사람과 에이전트를 위한 요약 가이드입니다: 필수 플래그를 포함한 명령어, 측정된 프로토콜 계약이 있는 위치, 프로토콜 사실을 추측하는 대신 측정해야 하는 규칙, 그리고 버그로 "발견"되지 않도록 의도적으로 미구현된 기능들이 담겨 있습니다.

## 카카오와 관련이 없습니다 (Not affiliated with Kakao)

KakaoTalk은 KakaoCorp의 상표입니다. 이 프로젝트는 카카오와 제휴하거나, 보증받거나, 후원을 받는 것이 아닙니다.

## 생성형 AI 사용 공개

이 프로젝트는 로컬로 호스팅된 Qwen3.8-Flash-Next를 사용하여 개발했습니다.
해당 README.md 번역본은 로컬로 호스팅된 Gemma 4 26B-A4B를 사용했습니다.

## 라이선스

AGPL-3.0-or-later. [LICENSE](LICENSE)를 참조하세요.
