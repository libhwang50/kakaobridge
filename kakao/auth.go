// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

// Device-registration and token-minting calls for a device that is not yet registered, plus the
// login.json call that mints a token pair: the Go counterpart of the registration flow the shipped
// clients run. The request/response contracts come from docs/protocol-notes.md §2 and §4, verified
// against the shipped clients (PC: win32 namespace, phone: android).
//
// The two flows differ by namespace, and the difference is real (docs/protocol-notes.md §2):
//   - account id: local form (010…) for android, international (8210…) for win32
//   - device uuid: 64-hex for android, 88-char base64 for win32
//   - login.json body: android sends forced/permanent/device_name and no os_version; win32 sends os_version/device_name
//   - generate: android sends permanent:true; passcodeLogin/generate requires X-VC in both namespaces

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BaseURL is the legacy REST front-end used by every /{agent}/ account call.
const BaseURL = "https://katalk.kakao.com/"

// XVCWindows / XVCPhone are the two X-VC values shipped clients send. Both were verified to be
// accepted in *both* namespaces (docs/protocol-notes.md §3), so X-VC is not a device-bound
// credential; it is still required for passcodeLogin/generate (missing it returns -500).
const (
	XVCWindows = "81dff230d8731c63"
	XVCPhone   = "05f67fa2bbc42ee8"
)

// Device is the device record the client sends. The phone shows name/model to the user as the account's
// registered/active device, so those strings are user-facing; keep them meaningful.
type Device struct {
	UUID      string `json:"uuid"`
	Name      string `json:"name"`
	Model     string `json:"model"`
	OSVersion string `json:"osVersion"`
}

// StatusError carries a gateway status code (login_flow.md §5). -100 means "not registered" or
// "pending approval" depending on the call, so callers must handle it per-call, not generically.
type StatusError struct {
	Status  int
	Message string
	Extra   map[string]any
}

func (e *StatusError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("kakao: status %d: %s", e.Status, e.Message)
	}
	return fmt.Sprintf("kakao: status %d", e.Status)
}

// DeviceAuth is what a device needs in order to register itself and get a session.
type DeviceAuth struct {
	// BaseURL overrides the gateway (tests, or a proxy for a capture). Empty means BaseURL.
	BaseURL string

	Agent     string // "android" | "win32"
	Account   string // account id in the form the namespace expects
	Password  string
	AppVer    string // must match the build that minted the tokens; the clients send their own version
	Lang      string
	XVC       string // required by passcodeLogin/generate; see XVCWindows/XVCPhone
	OSVersion string // win32 only (login.json os_version)
	Device    Device
}

func (a *DeviceAuth) mobile() bool { return a.Agent == "android" }

// url resolves a path against the gateway, or against BaseURL when a caller overrides it (tests).
func (a *DeviceAuth) url(path string) string {
	if a.BaseURL != "" {
		return a.BaseURL + "/" + path
	}
	return BaseURL + path
}

func (a *DeviceAuth) userAgent() string {
	if a.mobile() {
		return fmt.Sprintf("KT/%s An/%s %s", a.AppVer, a.Device.OSVersion, a.Lang)
	}
	return fmt.Sprintf("KT/%s Wd/%s %s", a.AppVer, a.OSVersion, a.Lang)
}

func (a *DeviceAuth) setHeaders(req *http.Request) {
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", a.Lang)
	req.Header.Set("User-Agent", a.userAgent())
	// X-VC is not only for the passcode family: without it login.json answers {"status":-500}, the
	// same "network environment" gate a wrong client build constant opens. It is a client build
	// value, not device-bound (both shipped values were accepted in both namespaces).
	// The agent header selects the namespace; lower-case or capitalised A: were both accepted.
	req.Header.Set("A", fmt.Sprintf("%s/%s/%s", a.Agent, a.AppVer, a.Lang))
	if a.XVC != "" {
		req.Header.Set("X-VC", a.XVC)
	}
}

// postJSON sends a JSON body and decodes the gateway reply, returning a *StatusError when status != 0.
func (a *DeviceAuth) postJSON(ctx context.Context, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(path), bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	a.setHeaders(req)
	return a.do(req, out)
}

func (a *DeviceAuth) postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url(path),
		strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	a.setHeaders(req)
	return a.do(req, out)
}

// do sends the request and decodes the reply into out, which MUST be a pointer: the gateway's
// answers are maps/objects, and handing a map by value is how three call sites here silently read an
// empty reply ({"status":0, "passcode":"1234"} came back and the caller saw map[] - the same
// "the gateway was silent, so we assumed" trap as the LOCO field-name typo).
func (a *DeviceAuth) do(req *http.Request, out any) error {
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kakao: %s %s: HTTP %d: %s", req.Method, req.URL.Path, resp.StatusCode,
			strings.TrimSpace(string(body)))
	}
	// Parse into a generic map first: every gateway reply carries `status`, and a non-zero one is an
	// error no matter what shape the payload has. (Decoding into a typed target directly hid errors.)
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("kakao: %s: bad JSON (%d bytes): %w", req.URL.Path, len(body), err)
	}
	// Decode before raising the status error: an error reply still carries useful fields, and
	// registerDevice's "not approved yet" (-100) answer is exactly where the poll interval and the
	// remaining window live (docs/protocol-notes.md §2).
	if out != nil {
		buf, err := json.Marshal(raw)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(buf, out); err != nil {
			return err
		}
	}
	if st, ok := raw["status"].(float64); ok && st != 0 {
		msg, _ := raw["message"].(string)
		return &StatusError{Status: int(st), Message: msg, Extra: raw}
	}
	return nil
}

// GeneratePasscode asks the gateway for a verification code for this device. The user types that code
// on the account's main device; the window is the server's own remainingSeconds value (58 s in every
// capture). Call PollRegisterDevice afterwards until it reports approval or the window passes.
func (a *DeviceAuth) GeneratePasscode(ctx context.Context) (code string, window time.Duration, err error) {
	body := map[string]any{
		"email":    a.Account,
		"password": a.Password,
		"device":   a.Device,
	}
	if a.mobile() {
		body["permanent"] = true
	}
	var out map[string]any
	if err := a.postJSON(ctx, a.Agent+"/account/passcodeLogin/generate", body, &out); err != nil {
		return "", 0, err
	}
	code, _ = out["passcode"].(string)
	if code == "" {
		return "", 0, fmt.Errorf("kakao: passcodeLogin/generate returned no passcode: %v", out)
	}
	if rs, ok := out["remainingSeconds"].(float64); ok {
		window = time.Duration(rs) * time.Second
	}
	return code, window, nil
}

// RegisterResult is one poll of passcodeLogin/registerDevice.
type RegisterResult struct {
	Approved  bool
	Remaining time.Duration
	Interval  time.Duration
}

// PollRegisterDevice polls once. While the code has not been typed the gateway answers status -100,
// which is expected here (unlike in login.json, where -100 means the device is not usable); the
// caller keeps polling every Interval. On approval the gateway returns status 0 with a nonce, which we
// currently don't keep - see docs/protocol-notes.md §4 for why the nonce is a lead, not a need.
func (a *DeviceAuth) PollRegisterDevice(ctx context.Context) (*RegisterResult, error) {
	var out map[string]any
	err := a.postJSON(ctx, a.Agent+"/account/passcodeLogin/registerDevice", map[string]any{
		"email":    a.Account,
		"password": a.Password,
		"device":   map[string]any{"uuid": a.Device.UUID},
	}, &out)
	res := &RegisterResult{}
	if num, ok := out["remainingSeconds"].(float64); ok {
		res.Remaining = time.Duration(num) * time.Second
	}
	if num, ok := out["nextRequestIntervalInSeconds"].(float64); ok {
		res.Interval = time.Duration(num) * time.Second
	}
	if res.Interval == 0 {
		res.Interval = 3 * time.Second
	}
	if err != nil {
		var se *StatusError
		if ok := asStatusError(err, &se); ok && se.Status == -100 {
			return res, nil // still waiting for the user to type the code
		}
		return nil, err
	}
	res.Approved = true
	return res, nil
}

func asStatusError(err error, target **StatusError) bool {
	if se, ok := err.(*StatusError); ok {
		*target = se
		return true
	}
	return false
}

// TokenPair is the session credential minted by login.json. Re-login with the same device while it is
// registered returns the SAME pair (verified: subdevice_flow.md §5b), which is why re-minting is the
// bridge's refresh path; there is no working renew_token.json call (login_flow.md §5).
type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	Recipe       string `json:"recipe"`
	UserID       int64  `json:"userId"`
	AccountID    int64  `json:"accountId"`
	ServerTime   int64  `json:"serverTime"`
	CountryIso   string `json:"countryIso"`

	AutoLoginAccountID string `json:"autoLoginAccountId"`
	DisplayAccountID   string `json:"displayAccountId"`
	MainDeviceAgent    string `json:"mainDeviceAgentName"`
	MainDeviceVersion  string `json:"mainDeviceAppVersion"`

	DeviceUUID string `json:"-"` // not returned; the device sent it, so the client knows it
}

// AuthHeader is the value every authenticated REST call sends (rest_api.md §1).
func (t *TokenPair) AuthHeader() string { return t.AccessToken + "-" + t.DeviceUUID }

// Login mints (or re-mints) the token pair for this device. While the device is registered this needs
// no human: a registered device keeps the same pair across session ends and can renew itself
// (subdevice_flow.md §5b). A status -100 means the device is not registered, or its session ended and
// it is not a registered device (login_flow.md §5) - in both cases only a fresh passcode flow fixes it.
func (a *DeviceAuth) Login(ctx context.Context) (*TokenPair, error) {
	form := url.Values{
		"email":       {a.Account},
		"password":    {a.Password},
		"device_uuid": {a.Device.UUID},
		"permanent":   {"true"},
		"device_name": {a.Device.Name},
	}
	if a.mobile() {
		form.Set("forced", "false") // the phone client sends this; it has no os_version field
	} else {
		form.Set("os_version", a.OSVersion)
	}
	var out map[string]any
	if err := a.postForm(ctx, a.Agent+"/account/login.json", form, &out); err != nil {
		return nil, err
	}
	tok := &TokenPair{DeviceUUID: a.Device.UUID}
	buf, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(buf, tok); err != nil {
		return nil, fmt.Errorf("kakao: decoding login.json reply: %w", err)
	}
	if tok.AccessToken == "" {
		return nil, fmt.Errorf("kakao: login.json returned no access_token: %v", out)
	}
	if tok.ServerTime == 0 {
		tok.ServerTime = time.Now().Unix()
	}
	return tok, nil
}
