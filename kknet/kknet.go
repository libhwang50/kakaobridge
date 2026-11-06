// SPDX-License-Identifier: AGPL-3.0-or-later

// Package kknet is the bridgev2 network connector for KakaoTalk: it turns a KakaoTalk account into
// a bridgev2 "network", so the framework owns the Matrix side (portals, ghosts, rooms, E2EE,
// management-room commands, provisioning login) while this package owns KakaoTalk.
//
// The protocol side sits in this repo's own packages: kakao for the account REST endpoints, loco for
// the LOCO chat session, media for the DNA media containers. The contracts they rely on (device
// registration and token lifecycle, the android-vs-win32 namespace difference, LOCO frame encoding,
// the media upload contract) are documented from measurements in docs/protocol-notes.md §4,
// docs/protocol-notes.md §5 and docs/protocol-notes.md §1.
package kknet

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mau.fi/util/configupgrade"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/id"

	"github.com/libhwang50/kakaobridge/kakao"
	"github.com/libhwang50/kakaobridge/loco"
)

// BridgeID identifies this connector in the bridge database and in m.bridge events. It must never
// change: existing rows key on it.
const BridgeID = "kakaotalk"

const (
	// FlowPasscode is the only flow Kakao needs for a chat-driven login: the new device shows a
	// code, the user types it on the account's main device, we poll until it is approved.
	// bridgev2's display_and_wait step type is exactly this shape (see docs/bridgev2-notes.md).
	FlowPasscode = "passcode"
	// StepIDs must be stable across logins of the same flow and unique per bridge (bridgev2.LoginStep docs).
	StepIDPasscode    = "cc.libhwang.kakaobridge.passcode"
	StepIDCredentials = "cc.libhwang.kakaobridge.credentials"
)

// Config is the network section of the bridge config file.
type Config struct {
	// Account and Password are used when the bridge logs an account in without asking for them in
	// chat (Kakao's passcode flow needs the account password, unlike e.g. an OAuth bridge). This is
	// the same credential a Kakao client sends; docs/protocol-notes.md §2.
	Account  string `yaml:"account"`
	Password string `yaml:"password"`

	// Agent selects the Kakao namespace: "android" (a registered mobile sub-device, durable and
	// self-renewing - docs/protocol-notes.md §4) or "win32" (a PC client, whose sessions we
	// proved are NOT durable). The two namespaces differ in account-id form, device id format and
	// login.json fields; see docs/protocol-notes.md §2.
	Agent string `yaml:"agent"`
	// AppVer must match the build that minted the tokens; Kakao echoes the client's own version back
	// (mainDeviceAppVersion) and rejects calls that look older than the account's client.
	AppVer string `yaml:"app_version"`

	Language string `yaml:"language"`
	MCCMNC   string `yaml:"mccmnc"` // a radio code, required by LOCO for an android session
	// AccountForm overrides which account-id form is sent to Kakao. The shipped clients differ here
	// and the wrong form is not accepted (android: local 010…; win32: international 8210…,
	// docs/protocol-notes.md §2), so empty means "follow the namespace".
	AccountForm string       `yaml:"account_form"`
	Device      DeviceConfig `yaml:"device"`

	// AskForCredentials makes the login flow ask for the account id and password in the management
	// room instead of taking them from this file. Either way the password is kept afterwards (in
	// LoginMeta): Kakao's self-renewal is a login.json call with the account *password*, so a login
	// that lost it cannot renew itself. In chat the password is also plainly visible in the room -
	// the framework does not redact user_input answers - so this is a trade, not a win.
	AskForCredentials bool `yaml:"ask_for_credentials"`

	// TokensFile + TokensMXID carry an already-registered Kakao device into a fresh bridge database.
	// This is not a convenience: Kakao allows one mobile sub-device per account, so re-running the
	// passcode flow after a reinstall would fail unless the old device were deregistered first.
	// The file is the JSON our own tools write (see docs/protocol-notes.md §4).
	TokensFile string    `yaml:"tokens_file"`
	TokensMXID id.UserID `yaml:"tokens_mxid"`

	// ReactionImages decides what a Kakao reaction looks like on Matrix. A Kakao reaction is a mini
	// emoticon, a picture rather than a character, and both spellings below carry its item code as the
	// identity, so this only changes what a client draws.
	//
	// false (the default) uses the closest Unicode character, taken from a table built by looking at each
	// item's own picture. A client that knows nothing about custom emoji shows that.
	// true fetches the item's picture from Kakao's item CDN, uploads it once, and keys the reaction by its
	// mxc:// URI, which is how mautrix-discord keys a custom emoji. A client that cannot draw a custom
	// emoji then shows the URI as a string, which is why this is a choice rather than a default.
	// See docs/protocol-notes.md §12.
	ReactionImages bool `yaml:"reaction_images"`
}

// DeviceConfig names the device Kakao sees. These strings are what the account's device list shows,
// so they are user-facing.
type DeviceConfig struct {
	Name  string `yaml:"name"`
	Model string `yaml:"model"`
	// OSVersion is the OS version string the client reports, e.g. "16" for android (the phone's own
	// value) or "10.0.19045" for win32.
	OSVersion string `yaml:"os_version"`
	// XVC is the verification-code header; both values below are accepted in both namespaces, and
	// passcodeLogin/generate requires it (docs/protocol-notes.md §3).
	XVC string `yaml:"x_vc"`
}

// Fill applies the defaults the shipped Android client uses. Exported so the bootstrap CLI and any
// config upgrade path get the same values the bridge itself would use.
func (c *Config) Fill() {
	if c.Agent == "" {
		c.Agent = "android"
	}
	if c.AppVer == "" {
		c.AppVer = "26.9.1"
	}
	if c.Language == "" {
		c.Language = "ko"
	}
	if c.Agent == "android" && c.MCCMNC == "" {
		c.MCCMNC = "45006" // the value docs/protocol-notes.md §3, recorded from the phone used while developing
	}
	if c.Device.XVC == "" {
		// A client build constant, not a per-installation secret; login.json answers -500 without it.
		c.Device.XVC = kakao.XVCWindows
	}
	if c.Device.Name == "" {
		c.Device.Name = "kakaobridge"
	}
	if c.Device.Model == "" {
		c.Device.Model = "linux"
	}
	switch {
	case c.Device.OSVersion != "":
	case c.Agent == "android":
		c.Device.OSVersion = "16"
	default:
		c.Device.OSVersion = "10.0.19045"
	}
	if c.Device.XVC == "" {
		c.Device.XVC = kakao.XVCWindows
	}
}

// accountID returns the account id in the form the namespace expects: android sends the local form,
// win32 the international one (docs/protocol-notes.md §2, verified from both clients).
func (c *Config) accountID() string { return c.formOf(c.Account) }

// needsCredentialInput decides whether the login flow asks for the account in chat. Credentials in the
// config are a convenience for the single-account case; the chat path is the normal one and the only
// one that works for a second Matrix user.
func (c *Config) needsCredentialInput() bool {
	return c.AskForCredentials || c.Account == "" || c.Password == ""
}

// formOf turns the one account id in the config into the form this namespace expects. Configs should
// store the international form (what Kakao's own account screens show); android calls derive 010….
func (c *Config) formOf(acct string) string { return c.formOfAgent(c.Agent, acct) }

// formOfAgent is formOf for an explicit namespace. An existing login carries its own agent, which may
// differ from the deployment default (a win32 login on an android-default bridge), and the account-id
// form is decided by the namespace: Kakao answers the wrong one with status 30, "check your account or
// password" - so this follows the login, not the config.
func (c *Config) formOfAgent(agent, acct string) string {
	if acct == "" {
		return ""
	}
	if c.AccountForm == "international" || (c.AccountForm == "" && agent != "android") {
		return internationalForm(acct)
	}
	return localForm(acct)
}

// internationalForm is the inverse: 010… -> 8210…, and "+82 10-…" -> 8210…
func internationalForm(acct string) string {
	digits := cleanAccount(acct)
	if strings.HasPrefix(digits, "0") && len(digits) == 11 {
		return "82" + digits[1:]
	}
	return digits
}

// localForm is the android namespace's form: Kakao's international id is 82 + the local number
// without its leading 0, so 821012345678 -> 01012345678. (Taking the last 10 digits instead of
// re-adding the 0 gives 1012345678, which the gateway answers with status 30.)
func localForm(acct string) string {
	digits := cleanAccount(acct)
	if strings.HasPrefix(digits, "82") && len(digits) == 12 {
		return "0" + digits[2:]
	}
	return digits
}

// LoginMeta is stored in the bridge's user_login table (Metadata column) for one Kakao login. It is
// the on-disk form of the credential; the pair it holds is renewable while the device is registered
// (docs/protocol-notes.md §4) and dies with the session for an unregistered PC device.
type LoginMeta struct {
	Agent      string `json:"agent"`
	Account    string `json:"account"`
	DeviceUUID string `json:"device_uuid"`
	// Password is the account password this login was created with. Kakao renews a registered device
	// with login.json plus the account password (docs/protocol-notes.md §4), so it belongs to the
	// credential, not just to the login ceremony: treat the user_login table as secret material.
	Password string `json:"password,omitempty"`

	AccessToken    string `json:"access_token,omitempty"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	Recipe         string `json:"recipe,omitempty"`
	UserID         int64  `json:"user_id,omitempty"`
	AccountID      int64  `json:"account_id,omitempty"`
	DisplayAccount string `json:"display_account,omitempty"`
	MCCMNC         string `json:"mccmnc,omitempty"`
	Language       string `json:"language,omitempty"`
	AppVer         string `json:"app_version,omitempty"`
	DeviceName     string `json:"device_name,omitempty"`
	DeviceModel    string `json:"device_model,omitempty"`
	DeviceOS       string `json:"device_os_version,omitempty"`
	// MintedAt records when login.json last succeeded; used for the pre-expiry renewal decision.
	MintedAt int64 `json:"minted_at,omitempty"`
}

// auth rebuilds the credential source for this login so the same code path (login.json) can renew it.
// LocoAuth builds the LOCO handshake credential from a stored login. The device id and the oauth
// token are what CHECKIN/LOGINLIST authenticate with; the rest are the client identity fields that
// decide which field set the server accepts (docs/protocol-notes.md §3).
func (m *LoginMeta) LocoAuth(cfg *Config) *loco.Auth {
	agent := pick(m.Agent, cfg.Agent)
	a := &loco.Auth{
		UserID: m.UserID,
		Token:  m.AccessToken,
		DUUID:  m.DeviceUUID,
		OS:     agent,
		AppVer: pick(m.AppVer, cfg.AppVer),
		Lang:   pick(m.Language, cfg.Language),
		MCCMNC: pick(m.MCCMNC, cfg.MCCMNC),
		// Revision is the client's local room-list revision. The bridge does not keep one, and 0 is
		// what a fresh install sends.
		Revision: 0,
		Country:  "KR",
	}
	// Defaults fills the things every shipped client sends that a stored credential does not carry:
	// the packed feature flags (rp) and the radio code for a mobile session. Omitting them is not a
	// smaller request the server tolerates - LOGINLIST answers inner status -300, which is what this
	// bridge did in production until it was fixed.
	a.Defaults(cfg.AppVer, cfg.Language)
	return a
}

func (m *LoginMeta) DeviceAuth(cfg *Config) *kakao.DeviceAuth {
	acct := m.Account
	if acct == "" {
		acct = cfg.accountID()
	}
	return &kakao.DeviceAuth{
		Agent: m.Agent,
		// The credential stores one form; the login's own namespace decides the wire form.
		Account: cfg.formOfAgent(pick(m.Agent, cfg.Agent), acct),
		// The login's own password wins over the config: a chat-driven login has to be able to renew
		// itself without the operator ever putting the account password in the config.
		Password:  pick(m.Password, cfg.Password),
		AppVer:    m.AppVer,
		Lang:      m.Language,
		OSVersion: m.DeviceOS,
		XVC:       cfg.Device.XVC,
		Device: kakao.Device{
			UUID:      m.DeviceUUID,
			Name:      m.DeviceName,
			Model:     m.DeviceModel,
			OSVersion: m.DeviceOS,
		},
	}
}

// Connector implements bridgev2.NetworkConnector. One value of it is one KakaoTalk "network" for the
// bridge; each logged-in Matrix user gets its own Client (NetworkAPI) below.
type Connector struct {
	cfg    Config
	bridge *bridgev2.Bridge

	// mu guards the connection-wide state below, in particular the mxc URI of the KakaoTalk logo: one
	// session uploads it, and the network descriptor is read from wherever bridgev2 builds a space.
	mu   sync.Mutex
	logo id.ContentURIString

	// directMedia is set by the framework before Start when the config turns direct media on; from that
	// point on GenerateContentURI returns URIs this bridge can serve itself (see directmedia.go).
	directMedia bool
}

var _ bridgev2.NetworkConnector = (*Connector)(nil)

func New() *Connector { return &Connector{} }

func (c *Connector) Init(b *bridgev2.Bridge) { c.bridge = b }

// Start may import a pre-registered credential (see Config.TokensFile); per-user work itself belongs
// to LoadUserLogin, which bridgev2 calls for each stored login.
func (c *Connector) Start(ctx context.Context) error {
	// Worth one line: which Kakao namespace this instance claims, and whether it was told to carry an
	// existing device. Both are easy to get wrong and both decide whether a login can exist.
	c.bridge.Log.Info().Str("agent", c.cfg.Agent).Str("app_version", c.cfg.AppVer).
		Bool("tokens_file_configured", c.cfg.TokensFile != "").
		Bool("credentials_in_config", c.cfg.Account != "" && c.cfg.Password != "").
		Bool("ask_for_credentials", c.cfg.AskForCredentials).
		Msg("KakaoTalk network configuration loaded")
	return c.bootstrapLogin(ctx)
}

func (c *Connector) GetName() bridgev2.BridgeName {
	return bridgev2.BridgeName{
		DisplayName:      "KakaoTalk",
		NetworkURL:       "https://www.kakao.com",
		NetworkID:        BridgeID,
		BeeperBridgeType: "cc.libhwang.kakaobridge",
		// The mark bridgev2 puts on the spaces and bridge-info rooms it creates. Empty until the logo
		// has been uploaded (while generating a registration, for instance, when the homeserver is not
		// being talked to at all), which is what an unset icon means to the config template.
		NetworkIcon: c.logoMXC(),
		// Not in the maunium list (that one only lists their own programs); 29315 sits just below the
		// lowest port there, maubot's 29316, so a host running both is unlikely to collide. See
		// https://mau.fi/ports.
		DefaultPort: 29315,
	}
}

// GetDBMetaTypes registers the metadata struct stored per login (the Kakao credential).
func (c *Connector) GetDBMetaTypes() database.MetaTypes {
	return database.MetaTypes{
		UserLogin: func() any { return &LoginMeta{} },
	}
}

func (c *Connector) GetCapabilities() *bridgev2.NetworkGeneralCapabilities {
	return &bridgev2.NetworkGeneralCapabilities{}
}

func (c *Connector) GetConfig() (string, any, configupgrade.Upgrader) {
	example := fmt.Sprintf(`# KakaoTalk network config.
account: "821012345674"   # Kakao account id, international form; needed when a user logs in without typing it in chat
password: "hunter2"       # the account password; Kakao's passcode flow needs it from the bridge
ask_for_credentials: false # true = the login flow asks for account+password in the management room instead
agent: android            # android = registered mobile sub-device (durable); win32 = PC client (session-only)
app_version: 26.9.1       # must match the client build that minted the tokens
language: ko
mccmnc: "45006"            # a radio code; required for an os=android LOCO session
account_form: ""           # "" = follow the namespace (android sends 010…, win32 sends 8210…)
# Import a device that is already registered instead of running the passcode flow again: Kakao
# allows a single mobile sub-device per account, so a fresh database must carry the old identity.
# tokens_file is the token JSON our tools write; tokens_mxid is the Matrix user that owns it.
# Both must be set to import anything; empty means "no import".
tokens_file: ""
tokens_mxid: ""
device:
  name: kakaobridge        # what the account's device list shows on the phone
  model: linux
  os_version: "16"
  x_vc: %s
`, kakao.XVCWindows)
	return example, &c.cfg, configupgrade.SimpleUpgrader(copyNetworkConfig)
}

// copyNetworkConfig is why GetConfig must not return NoopUpgrader: configupgrade rebuilds the config
// file from the *example* tree and copies the user's values into it only where an upgrader step asks
// for them. With no steps, every start-up silently reset the whole network section - account,
// password, and the tokens_file that carries our registered device - back to the example defaults.
// Observed on the deployed bridge as "No user logins found" with a config that plainly had them.
//
// This network has no schema history yet, so one step that copies every field is the whole upgrader;
// add real steps here when a field is renamed or removed.
func copyNetworkConfig(up configupgrade.Helper) {
	// Every field has to be copied explicitly AND exist in the example text: configupgrade rebuilds
	// the file from the example, so an unmentioned key silently disappears from it (see the test).
	for _, key := range []string{
		"account", "password", "agent", "account_form", "app_version",
		"language", "mccmnc", "tokens_file", "tokens_mxid",
	} {
		up.Copy(configupgrade.Str, key)
	}
	up.Copy(configupgrade.Bool, "ask_for_credentials")
	for _, key := range []string{"name", "model", "os_version", "x_vc"} {
		up.Copy(configupgrade.Str, "device", key)
	}
}

// LoadUserLogin fills Client for a login loaded from the database. It runs under the bridge's cache
// lock, so it must not do any IO (bridgev2.NetworkConnector docs) - connecting happens in Connect.
func (c *Connector) LoadUserLogin(_ context.Context, login *bridgev2.UserLogin) error {
	meta, ok := login.Metadata.(*LoginMeta)
	if !ok || meta == nil || meta.DeviceUUID == "" {
		return fmt.Errorf("kknet: this login has no Kakao credential")
	}
	if meta.Agent == "" {
		meta.Agent = c.cfg.Agent
	}
	if meta.AppVer == "" {
		meta.AppVer = c.cfg.AppVer
	}
	if meta.Language == "" {
		meta.Language = c.cfg.Language
	}
	login.Client = NewClient(c, login, meta)
	return nil
}

func (c *Connector) GetLoginFlows() []bridgev2.LoginFlow {
	return []bridgev2.LoginFlow{{
		ID:          FlowPasscode,
		Name:        "Verification code",
		Description: "Show a 4-digit code to type in the KakaoTalk app on the account's main device",
	}}
}

// CreateLogin returns the login process, which does its work only once bridgev2 calls Start.
func (c *Connector) CreateLogin(_ context.Context, user *bridgev2.User, flowID string) (bridgev2.LoginProcess, error) {
	if flowID != FlowPasscode {
		return nil, fmt.Errorf("kknet: unknown login flow %q", flowID)
	}
	return &PasscodeLogin{conn: c, user: user}, nil
}

// GetBridgeInfoVersion: bump when the room capabilities we advertise change, so rooms are updated.
func (c *Connector) GetBridgeInfoVersion() (info, capabilities int) { return 1, 1 }

// --- helpers used by the login flow ---

// newDeviceAuth builds the device-registration credential for a login that is being created now. The
// device uuid is generated here and is the login's permanent id (see login.go's comment on why).
func (c *Connector) newDeviceAuth() *kakao.DeviceAuth {
	return &kakao.DeviceAuth{
		Agent:     c.cfg.Agent,
		Account:   c.cfg.accountID(),
		Password:  c.cfg.Password,
		AppVer:    c.cfg.AppVer,
		Lang:      c.cfg.Language,
		OSVersion: c.cfg.Device.OSVersion,
		XVC:       c.cfg.Device.XVC,
		Device: kakao.Device{
			UUID:      newDeviceUUID(c.cfg.Agent),
			Name:      c.cfg.Device.Name,
			Model:     c.cfg.Device.Model,
			OSVersion: c.cfg.Device.OSVersion,
		},
	}
}

func newDeviceUUID(agent string) string {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		panic(fmt.Sprintf("kknet: no randomness for a device id: %v", err))
	}
	if agent == "android" {
		return hex.EncodeToString(buf)
	}
	raw := make([]byte, 64)
	if _, err := rand.Read(raw); err != nil {
		panic(fmt.Sprintf("kknet: no randomness for a device id: %v", err))
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// windowOr clamps the server's own code window so we stop polling just before it expires.
func windowOr(d time.Duration, fallback time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return fallback
}
