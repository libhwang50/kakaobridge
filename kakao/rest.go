// SPDX-License-Identifier: AGPL-3.0-or-later

// Package kakao reads the identity parts of the KakaoTalk REST gateway that a bridge needs: its own
// profile (for Matrix display name/avatar) and the friend list (for puppets and for opening 1:1
// rooms). The endpoints, HTTP methods and body shapes are the ones verified in docs/protocol-notes.md §6
// against our own credentials - nothing here is guessed from a document.
package kakao

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const base = "https://katalk.kakao.com/"

// Auth is the credential the bridge owns (the token material inside loco.Auth).
type Auth struct {
	Token    string // access_token
	DeviceID string // device_uuid
	Agent    string // "win32" | "android"
	Version  string // client version to announce, e.g. "26.9.1"
	Platform string // "Wd" (windows) | "An" (android): part of the client User-Agent
	Lang     string // "ko"
}

// Client talks to one agent namespace of the gateway.
type Client struct {
	A    Auth
	HTTP *http.Client
}

// New fills in the defaults the PC client uses (agent win32, language ko).
func New(a Auth) *Client {
	if a.Agent == "" {
		a.Agent = "win32"
	}
	if a.Lang == "" {
		a.Lang = "ko"
	}
	return &Client{A: a, HTTP: &http.Client{Timeout: 15 * time.Second}}
}

// setAuth puts the credential on the request the way the client's modern stack does: a single
// `authorization: <access_token>-<device_uuid>` value (verified for profile/list and
// friends/diff.json) plus the client User-Agent the gateway expects.
// SetAuth applies the `authorization: <access_token>-<device_uuid>` form plus the client headers the
// gateway expects. Exported for callers that build their own request (e.g. a sub-device logout).
func (c *Client) SetAuth(req *http.Request) { c.setAuth(req) }

func (c *Client) setAuth(req *http.Request) {
	req.Header.Set("authorization", c.A.Token+"-"+c.A.DeviceID)
	plat := c.A.Platform
	if plat == "" {
		plat = "Wd"
	}
	if c.A.Version != "" {
		req.Header.Set("User-Agent", fmt.Sprintf("KT/%s %s/10.0.19045 %s", c.A.Version, plat, c.A.Lang))
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", c.A.Lang)
}

func (c *Client) get(path string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, base+c.A.Agent+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	c.setAuth(req)
	return c.round(req)
}

func (c *Client) postForm(path string, form url.Values) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodPost, base+c.A.Agent+"/"+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	c.setAuth(req)
	return c.round(req)
}

// round reports transport failures, non-200s, undecodable bodies, and Kakao's own status field as
// separate errors: a 200 with `{"status":-500}` is a call the gateway refused, not a success.
func (c *Client) round(req *http.Request) (map[string]any, error) {
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kakao: %s %s: http %d: %s", req.Method, req.URL.Path,
			resp.StatusCode, trim(string(b), 200))
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("kakao: decode %s: %w (%s)", req.URL.Path, err, trim(string(b), 160))
	}
	if st, ok := out["status"].(float64); ok && st != 0 {
		return out, fmt.Errorf("kakao: %s status %v (%v)", req.URL.Path, st, out["message"])
	}
	return out, nil
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Profile is one of the account's own profiles (GET profile/list returns the multi-profile list).
type Profile struct {
	ProfileID     string `json:"profileId"`
	Nickname      string `json:"nickname"`
	StatusMessage string `json:"statusMessage"`
	Avatar        string `json:"profileImageUrl"`
	Main          bool   `json:"main"`
}

// SelfProfile returns the account's main profile - the bridge's own Matrix display name/avatar.
func (c *Client) SelfProfile() (*Profile, error) {
	o, err := c.get("profile/list")
	if err != nil {
		return nil, err
	}
	var main *Profile
	for _, p := range anySlice(o["profiles"]) {
		m := asMap(p)
		pr := &Profile{ProfileID: asString(m["profileId"]), Nickname: asString(m["nickname"]),
			StatusMessage: asString(m["statusMessage"]), Avatar: asString(m["profileImageUrl"]),
			Main: asBool(m["main"])}
		if pr.Main {
			return pr, nil
		}
		if main == nil {
			main = pr
		}
	}
	if main == nil {
		return nil, fmt.Errorf("kakao: no profile in response")
	}
	return main, nil
}

// Friend is one contact. Note: AccountID is the Kakao *account* id, which is not the same as UserID;
// LOCO addresses people by UserID, so that is the value a bridge should key on.
type Friend struct {
	UserID        int64  `json:"userId"`
	AccountID     int64  `json:"accountId"`
	Nickname      string `json:"nickName"`
	StatusMessage string `json:"statusMessage"`
	UserType      int    `json:"userType"`
	Type          int    `json:"type"`
	Suspended     bool   `json:"suspended"`
	Memorial      bool   `json:"memorial"`
	Avatar        string `json:"profileImageUrl"`
}

// Friends asks for a delta with nothing held back (ids= empty), which returns every contact plus
// total_count. Sending the ids already known would only return changes (the delta semantics the PC
// client uses); both forms are the same endpoint.
func (c *Client) Friends() ([]Friend, error) {
	o, err := c.postForm("friends/diff.json", url.Values{"ids": {}})
	if err != nil {
		return nil, err
	}
	var out []Friend
	for _, v := range anySlice(o["added_friends"]) {
		m := asMap(v)
		f := Friend{UserID: int64(asFloat(m["userId"])), AccountID: int64(asFloat(m["accountId"])),
			Nickname: asString(m["nickName"]), StatusMessage: asString(m["statusMessage"]),
			UserType: int(asFloat(m["userType"])), Type: int(asFloat(m["type"])),
			Suspended: asBool(m["suspended"]), Memorial: asBool(m["memorial"]),
			Avatar: asString(m["profileImageUrl"])}
		if f.UserID != 0 {
			out = append(out, f)
		}
	}
	return out, nil
}

func anySlice(v any) []any {
	if s, ok := v.([]any); ok {
		return s
	}
	return nil
}

func asMap(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBool(v any) bool {
	if b, ok := v.(bool); ok {
		return b
	}
	return false
}

func asFloat(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}
