// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Auth carries what a *bridge-owned* identity needs to authenticate LOCO itself, instead of
// replaying a captured CHECKIN/LOGINLIST body. Field names, order and BSON types were taken from
// the shipped client's own bodies (see cmd/bsondump) and verified against
// docs/protocol-notes.md §5; the token/duuid pair comes from the login flow
// (LoadAuth reads the JSON format that network.tokens_file refers to).
type Auth struct {
	UserID    int64  `json:"userId"`
	Token     string `json:"access_token"` // LOGINLIST: oauthToken
	DUUID     string `json:"device_uuid"`  // LOGINLIST: duuid
	OS        string `json:"agent"`        // "win32" | "android"
	AppVer    string `json:"-"`
	Lang      string `json:"-"`
	Country   string `json:"countryIso"`
	MCCMNC    string `json:"-"`
	LastToken int64  `json:"lastTokenId"`
	PCState   int32  `json:"-"`
	DType     int32  `json:"-"`
	RP        []byte `json:"-"`
	// Revision is the client's own local DB revision, sent by a mobile LOGINLIST
	// (defpackage/xx70.java: aVar.f("revision", …getRevision())). A bridge with no local
	// mirror of the room list sends 0.
	Revision int32 `json:"revision"`
	// LastLBK pins the "lbk" field (the client sends epoch seconds of this login). Zero = now.
	LastLBK int64 `json:"-"`
}

// DefaultRP is the "rp" blob: 3 little-endian int16s packed by the client as
// {blockRevision, -1, plusBlockRevision} (apk: defpackage/xx70.java). It is NOT a constant: it is a
// per-feature flag triple (1 = include blocked rooms / plus-blocked rooms), which is why every fresh
// session showed 01 00 ff ff 01 00.
var DefaultRP = []byte{0x01, 0x00, 0xff, 0xff, 0x01, 0x00}

func (a *Auth) Defaults(appVer, lang string) {
	if a.OS == "" {
		a.OS = "win32"
	}
	if a.AppVer == "" {
		a.AppVer = appVer
	}
	if a.Lang == "" {
		a.Lang = lang
	}
	if a.Country == "" {
		a.Country = "KR"
	}
	if a.MCCMNC == "" {
		if a.subDevice() {
			// A mobile session has to present a radio; 45006 is the carrier code recorded for this
			// account's phone (docs/protocol-notes.md §3). Set "mccmnc" in the credential file to
			// pick a different one.
			a.MCCMNC = "45006"
		} else {
			a.MCCMNC = "999" // no cellular radio on a desktop session
		}
	}
	if a.DType == 0 && !a.subDevice() {
		a.DType = 2 // observed for os=win32
	}
	if a.PCState == 0 && !a.subDevice() {
		a.PCState = 1
	}
	if len(a.RP) == 0 {
		a.RP = DefaultRP
	}
}

// subDevice reports whether this identity is a mobile-style session (agent "android"). The shipped
// phone client sends a different field set for those; see docs/protocol-notes.md §3.
func (a *Auth) subDevice() bool { return a.OS == "android" }

func (a *Auth) validate() error {
	if a.UserID == 0 || a.Token == "" || a.DUUID == "" {
		return fmt.Errorf("loco: auth incomplete (userId=%d token=%d chars duuid=%d chars)",
			a.UserID, len(a.Token), len(a.DUUID))
	}
	return nil
}

// CheckinBody builds the CHECKIN request sent to the ticket host, which answers with the LOCO host to
// connect to. Note: no token here — CHECKIN only identifies the user/device class.
//
// The field set is OS-dependent, and that is not cosmetic: the shipped phone client sends userId, os,
// ntype, appVer, lang, MCCMNC with no countryISO and no useSub, while the PC client sends countryISO
// and useSub instead. Both bodies are decoded from live frames in docs/protocol-notes.md §5 and
// docs/protocol-notes.md §3.
func (a *Auth) CheckinBody() []byte {
	if a.subDevice() {
		return EncodeDoc([]Elem{
			{Key: "userId", Type: tInt64, Val: a.UserID},
			{Key: "os", Type: tString, Val: a.OS},
			{Key: "ntype", Type: tInt32, Val: int32(0)},
			{Key: "appVer", Type: tString, Val: a.AppVer},
			{Key: "lang", Type: tString, Val: a.Lang},
			{Key: "MCCMNC", Type: tString, Val: a.MCCMNC},
		})
	}
	return EncodeDoc([]Elem{
		{Key: "userId", Type: tInt64, Val: a.UserID},
		{Key: "os", Type: tString, Val: a.OS},
		{Key: "ntype", Type: tInt32, Val: int32(0)},
		{Key: "appVer", Type: tString, Val: a.AppVer},
		{Key: "MCCMNC", Type: tString, Val: a.MCCMNC},
		{Key: "countryISO", Type: tString, Val: a.Country},
		{Key: "useSub", Type: tBool, Val: true},
		{Key: "lang", Type: tString, Val: a.Lang},
	})
}

// LoginlistBody builds the LOGINLIST request that actually authenticates the session: the server
// checks oauthToken+duuid and supersedes any previous session for that device.
//
// chatIds/maxIds are the per-room watermarks a client restores from its local store; sending empty
// arrays (as the shipped client does) means "no per-room catch-up". lastTokenId is the LOCO push
// cursor: 0 asks for nothing historic. lbk is filled in at call time (observed = epoch seconds of
// the login). dtype/pcst keep the values the PC client sends; for a mobile identity they are NOT
// sent at all, because their values for os=android have never been observed (docs/protocol-notes.md §4
// §2) and are omitted rather than guessed at.
func (a *Auth) mobileFields() []Elem {
	return []Elem{
		{Key: "appVer", Type: tString, Val: a.AppVer},
		{Key: "prtVer", Type: tString, Val: "1.0"},
		{Key: "os", Type: tString, Val: a.OS},
		{Key: "lang", Type: tString, Val: a.Lang},
		{Key: "duuid", Type: tString, Val: a.DUUID},
		{Key: "ntype", Type: tInt32, Val: int32(0)},
		{Key: "MCCMNC", Type: tString, Val: a.MCCMNC},
		{Key: "revision", Type: tInt32, Val: a.Revision},
	}
}

func (a *Auth) LoginlistBody() []byte {
	lbk := a.LastLBK
	if lbk == 0 && !a.subDevice() {
		// the shipped PC client's captured value was an epoch stamp; the mobile builder sends a
		// blind-token cursor instead (0 = no blind/hidden-message state); see docs/protocol-notes.md §5.
		lbk = time.Now().Unix()
	}
	if a.subDevice() {
		return EncodeDoc(append(a.mobileFields(),
			Elem{Key: "chatIds", Type: tArr, Val: []Elem{}},
			Elem{Key: "maxIds", Type: tArr, Val: []Elem{}},
			Elem{Key: "lastTokenId", Type: tInt64, Val: a.LastToken},
			Elem{Key: "lbk", Type: tInt32, Val: int32(a.LastLBK)},
			Elem{Key: "rp", Type: tBin, Val: a.RP},
			Elem{Key: "bg", Type: tBool, Val: false},
			Elem{Key: "oauthToken", Type: tString, Val: a.Token},
			Elem{Key: "isSw", Type: tBool, Val: false}))
	}
	el := []Elem{
		{Key: "prtVer", Type: tString, Val: "1.0"},
		{Key: "appVer", Type: tString, Val: a.AppVer},
		{Key: "os", Type: tString, Val: a.OS},
		{Key: "lang", Type: tString, Val: a.Lang},
		{Key: "oauthToken", Type: tString, Val: a.Token},
		{Key: "duuid", Type: tString, Val: a.DUUID},
		{Key: "ntype", Type: tInt32, Val: int32(0)},
		{Key: "MCCMNC", Type: tString, Val: a.MCCMNC},
	}
	el = append(el, Elem{Key: "dtype", Type: tInt32, Val: a.DType},
		Elem{Key: "pcst", Type: tInt32, Val: a.PCState})
	el = append(el,
		Elem{Key: "chatIds", Type: tArr, Val: []Elem{}},
		Elem{Key: "maxIds", Type: tArr, Val: []Elem{}},
		Elem{Key: "lastTokenId", Type: tInt64, Val: a.LastToken},
		Elem{Key: "lbk", Type: tInt32, Val: int32(lbk)},
		Elem{Key: "rp", Type: tBin, Val: a.RP})
	return EncodeDoc(el)
}

// LoadAuth reads a credential file in the format network.tokens_file points at
// ({"access_token","refresh_token","device_uuid","userId","agent","countryIso","recipe"}) and applies
// the same defaults the bridge would use.
// Keeping one format for the bootstrap file and the runtime credential means onboarding and running
// need no manual copying. Note that the bridge can read this file but never writes one: a device
// registered through the login flow lives only in the bridge database.
func LoadAuth(path, appVer, lang string) (*Auth, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw struct {
		AccessToken string `json:"access_token"`
		DeviceUUID  string `json:"device_uuid"`
		UserID      int64  `json:"userId"`
		Agent       string `json:"agent"`
		CountryIso  string `json:"countryIso"`
		Recipe      string `json:"recipe"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	a := &Auth{UserID: raw.UserID, Token: raw.AccessToken, DUUID: raw.DeviceUUID,
		OS: raw.Agent, Country: raw.CountryIso}
	a.Defaults(appVer, lang)
	if err := a.validate(); err != nil {
		return nil, err
	}
	return a, nil
}
