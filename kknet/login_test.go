// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import "testing"

// Where the credentials come from is a real choice with consequences: the chat path is the only one
// that works for a second Matrix user, the config path is what lets an imported token file renew.
func TestCredentialInputDecision(t *testing.T) {
	full := Config{Account: "821012345678", Password: "pw"}
	if full.needsCredentialInput() {
		t.Error("configured credentials should not trigger the input step")
	}
	ask := full
	ask.AskForCredentials = true
	if !ask.needsCredentialInput() {
		t.Error("ask_for_credentials must override configured credentials")
	}
	for name, c := range map[string]Config{
		"no account":  {Password: "pw"},
		"no password": {Account: "821012345678"},
		"empty":       {},
	} {
		if !c.needsCredentialInput() {
			t.Errorf("%s: must ask for credentials", name)
		}
	}
}

// The account id a person types is not the form the gateway wants; one canonical form is stored and
// the namespace decides the wire form. Kakao answers the wrong form with "check your account or
// password" (status 30), which is otherwise a confusing failure.
func TestAccountInputIsCanonicalized(t *testing.T) {
	for _, in := range []string{"+82 10-1234-5678", "01012345678", "821012345678", "82 10 1234 5678"} {
		got, err := validateAccountID(in)
		if err != nil {
			t.Errorf("validateAccountID(%q): %v", in, err)
			continue
		}
		if internationalForm(got) != "821012345678" {
			t.Errorf("input %q cleaned to %q, canonical form %q", in, got, internationalForm(got))
		}
	}
	for _, bad := range []string{"", "hwang", "12345"} {
		if _, err := validateAccountID(bad); err == nil {
			t.Errorf("validateAccountID(%q) should fail", bad)
		}
	}
}

// Renewal uses the password the login was created with, not the operator's config: that is what makes
// a chat-driven login self-sustaining.
func TestLoginMetaPasswordWins(t *testing.T) {
	meta := &LoginMeta{Agent: "android", Account: "821012345678", DeviceUUID: "x", Password: "from-chat"}
	cfg := Config{Agent: "android", Password: "from-config", Device: DeviceConfig{XVC: "vc"}}
	if got := meta.DeviceAuth(&cfg).Password; got != "from-chat" {
		t.Errorf("login password must win over config, got %q", got)
	}
	borrowed := *meta
	borrowed.Password = ""
	if got := borrowed.DeviceAuth(&cfg).Password; got != "from-config" {
		t.Errorf("empty stored password should fall back to config, got %q", got)
	}
	// The wire form follows the namespace, from either stored form.
	local := *meta
	local.Account = "01012345678"
	if got := local.DeviceAuth(&cfg).Account; got != "01012345678" {
		t.Errorf("android must send the local form, got %q", got)
	}
	// The namespace comes from the login, not from the deployment default: a win32 login on an
	// android-default bridge still has to speak win32.
	pc := *meta
	pc.Agent = "win32"
	if got := pc.DeviceAuth(&cfg).Account; got != "821012345678" {
		t.Errorf("a win32 login must send the international form, got %q", got)
	}
	pcLocal := pc
	pcLocal.Account = "01012345678"
	if got := pcLocal.DeviceAuth(&cfg).Account; got != "821012345678" {
		t.Errorf("a win32 login with a locally stored account: got %q", got)
	}
}

// A credential in the database is not a LOCO handshake: the client-side defaults (packed feature
// flags, radio code) are part of what the server accepts. This is the regression the deployed bridge
// hit - CHECKIN was fine, LOGINLIST answered -300 - so it is tested directly.
func TestLocoAuthIsACompleteHandshake(t *testing.T) {
	cfg := Config{Agent: "android", AppVer: "26.9.1", Language: "ko"}
	meta := &LoginMeta{UserID: 447137810, AccessToken: "tok", DeviceUUID: "dev", Agent: "android"}
	a := meta.LocoAuth(&cfg)
	if len(a.RP) == 0 {
		t.Error("rp (feature flags) must be set; LOGINLIST rejects a body without them")
	}
	if a.MCCMNC == "" {
		t.Error("a mobile session needs a radio code")
	}
	if a.AppVer != "26.9.1" || a.Lang != "ko" || a.OS != "android" {
		t.Errorf("client identity wrong: %+v", a)
	}
	if body := a.LoginlistBody(); len(body) == 0 {
		t.Error("empty LOGINLIST body")
	}
	// A login's own fields win over the deployment defaults.
	mine := *meta
	mine.Agent, mine.AppVer, mine.Language, mine.MCCMNC = "win32", "25.0.0", "en", "999"
	b := mine.LocoAuth(&cfg)
	if b.OS != "win32" || b.AppVer != "25.0.0" || b.Lang != "en" {
		t.Errorf("login fields must win: %+v", b)
	}
	if len(b.RP) == 0 {
		t.Error("rp must be set for a PC login too")
	}
}
