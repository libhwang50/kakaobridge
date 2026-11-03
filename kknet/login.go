// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"context"
	"fmt"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"

	"github.com/libhwang50/kakaobridge/kakao"
)

// PasscodeLogin drives KakaoTalk's device-verification login as bridgev2 login steps.
//
// The flow is exactly what the shipped clients do (docs/protocol-notes.md §2): the *new* device
// asks the gateway for a 4-digit code, the user types that code on the account's main device, and the
// new device polls registerDevice until the gateway says the code was accepted (then login.json mints
// the token pair). bridgev2 models that shape natively: a display_and_wait step whose display type is
// "code", then Wait().
//
// The device id generated here becomes the login's permanent id. A device registered in the android
// namespace keeps working across session ends and can renew itself (docs/protocol-notes.md §4),
// which is why storing the credential here - rather than re-registering on every start - is the point.
// That durability ends when the account's main device approves *another* mobile registration: the
// replaced device then answers login.json with -100 (docs/protocol-notes.md §4), which is what
// Client.Connect reports as BAD_CREDENTIALS.
type PasscodeLogin struct {
	conn *Connector
	user *bridgev2.User

	// account/password are what the user typed for THIS attempt. They are deliberately not written
	// back into the config, which belongs to the operator, only into LoginMeta.
	account  string
	password string

	auth   *kakao.DeviceAuth
	code   string
	window time.Duration
}

var (
	_ bridgev2.LoginProcess               = (*PasscodeLogin)(nil)
	_ bridgev2.LoginProcessDisplayAndWait = (*PasscodeLogin)(nil)
	_ bridgev2.LoginProcessUserInput      = (*PasscodeLogin)(nil)
)

// Start asks for the account credentials unless the bridge was configured with them, then goes to the
// code. Kakao's flow needs the account password on the *device* side, so unlike an OAuth bridge there
// is no "just approve it on your phone" variant.
func (p *PasscodeLogin) Start(_ context.Context) (*bridgev2.LoginStep, error) {
	if p.conn.cfg.needsCredentialInput() {
		return &bridgev2.LoginStep{
			Type:   bridgev2.LoginStepTypeUserInput,
			StepID: StepIDCredentials,
			Instructions: "Send the Kakao account to sign in with; its id is the phone number Kakao uses " +
				"for the account. This room is not secret: the password will be readable in it, and the " +
				"bridge keeps a copy either way, because Kakao renews a registered device with the " +
				"account password. Put it in network.password instead if you prefer.",
			UserInputParams: &bridgev2.LoginUserInputParams{
				Fields: []bridgev2.LoginInputDataField{
					{
						Type:         bridgev2.LoginInputFieldTypePhoneNumber,
						ID:           "account",
						Name:         "Kakao account id",
						Description:  "e.g. +82 10-1234-5678",
						DefaultValue: p.conn.cfg.Account,
						Validate:     validateAccountID,
					},
					{
						Type:        bridgev2.LoginInputFieldTypePassword,
						ID:          "password",
						Name:        "Password",
						Description: "Kakao needs it to start the device verification, and to renew afterwards",
						// Optional only when the config already has one.
						Optional: p.conn.cfg.Password != "",
					},
				},
			},
		}, nil
	}
	return p.showCode()
}

// validateAccountID accepts whatever people type and hands the gateway only digits. Forms differ per
// namespace (android 010…, win32 8210…) and Config.formOf derives the right one, so the only thing
// to reject here is something that is not an account id at all.
func validateAccountID(v string) (string, error) {
	digits := cleanAccount(v)
	if len(digits) < 10 || len(digits) > 15 {
		return "", fmt.Errorf("%q does not look like a Kakao account id", v)
	}
	return digits, nil
}

// SubmitUserInput takes the account+password the user supplied and continues with the code step.
func (p *PasscodeLogin) SubmitUserInput(_ context.Context, input map[string]string) (*bridgev2.LoginStep, error) {
	acct, pass := cleanAccount(input["account"]), input["password"]
	if pass == "" {
		pass = p.conn.cfg.Password // the field is optional only when the config has one
	}
	if acct == "" || pass == "" {
		return nil, fmt.Errorf("kknet: a Kakao account id and password are required")
	}
	p.account, p.password = acct, pass
	return p.showCode()
}

// showCode asks the gateway for the verification code and hands it to the client to display.
func (p *PasscodeLogin) showCode() (*bridgev2.LoginStep, error) {
	p.auth = p.conn.newDeviceAuth()
	if p.account != "" {
		p.auth.Account = p.conn.cfg.formOf(p.account) // the namespace decides the wire form
	}
	if p.password != "" {
		p.auth.Password = p.password
	}
	code, window, err := p.auth.GeneratePasscode(p.conn.bridge.BackgroundCtx)
	if err != nil {
		return nil, fmt.Errorf("kknet: asking Kakao for a verification code: %w", err)
	}
	p.code, p.window = code, windowOr(window, 58*time.Second)
	// Operator-visible: the code step is where a login stalls if the phone is not there. The code
	// itself is only at debug - it is already in the room, and it is short-lived.
	p.conn.bridge.Log.Info().Str("kakao_device", p.auth.Device.UUID[:12]).
		Str("agent", p.auth.Agent).Dur("valid_for", p.window).
		Msg("Kakao verification code issued; waiting for it to be typed on the account's main device")
	p.conn.bridge.Log.Debug().Str("passcode", p.code).Msg("verification code handed to the client")
	return &bridgev2.LoginStep{
		Type:   bridgev2.LoginStepTypeDisplayAndWait,
		StepID: StepIDPasscode,
		Instructions: "Type this code in KakaoTalk on the device that is already signed in " +
			"(the app shows it under its device/login screens). It expires in about a minute.",
		DisplayAndWaitParams: &bridgev2.LoginDisplayAndWaitParams{
			Type: bridgev2.LoginDisplayTypeCode,
			Data: p.code,
		},
	}, nil
}

// Wait polls registerDevice until the code is accepted or the server's own window closes. The gateway
// answers status -100 while waiting, which is normal here (in login.json the same code means the
// device is not usable - docs/protocol-notes.md §4), and it tells us the poll interval.
func (p *PasscodeLogin) Wait(ctx context.Context) (*bridgev2.LoginStep, error) {
	if p.auth == nil {
		return nil, fmt.Errorf("kknet: Wait called before the code step")
	}
	deadline := time.Now().Add(p.window)
	for {
		res, err := p.auth.PollRegisterDevice(ctx)
		if ctx.Err() != nil {
			return nil, bridgev2.ErrLoginStepCancelled
		} else if err != nil {
			return nil, fmt.Errorf("kknet: waiting for the code to be accepted: %w", err)
		} else if res.Approved {
			p.conn.bridge.Log.Info().Str("kakao_device", p.auth.Device.UUID[:12]).
				Msg("Kakao accepted the verification code; minting the token pair")
			return p.complete(ctx)
		}
		left := windowOr(res.Remaining, p.window)
		if time.Now().Add(res.Interval).After(deadline) || left <= 0 {
			return nil, fmt.Errorf("kknet: the verification code was not typed within %s; start the login again", p.window)
		}
		select {
		case <-ctx.Done():
			return nil, bridgev2.ErrLoginStepCancelled
		case <-time.After(res.Interval):
		}
	}
}

func (p *PasscodeLogin) Cancel() {}

// complete mints the token pair and stores it as this user's login. NewLogin is the framework's own
// path: it sets the bridge id, calls our LoadUserLogin (which fills Client), and inserts the row.
//
// Starting the session is on us. bridgev2 calls NetworkAPI.Connect for logins it loads at start-up
// (Bridge.StartLoginsForUsers) and after a bridge-state-triggered reconnect, but not for one a login
// flow has just created - NewLogin fills Client and stops. Without the Connect below, a successful
// login leaves the account signed in with no LOCO session, so no chat list, no space and no portals
// appear until the bridge is restarted.
func (p *PasscodeLogin) complete(ctx context.Context) (*bridgev2.LoginStep, error) {
	tok, err := p.auth.Login(ctx)
	if err != nil {
		return nil, fmt.Errorf("kknet: Kakao accepted the code but login failed: %w", err)
	}
	meta := &LoginMeta{
		Agent: p.auth.Agent,
		// Store one canonical form. The user may have typed +82 10-…, 010…, or 8210…; Config.formOf
		// turns that into whatever the configured namespace expects on the wire.
		Account:        internationalForm(p.auth.Account),
		Password:       p.auth.Password,
		DeviceUUID:     p.auth.Device.UUID,
		AccessToken:    tok.AccessToken,
		RefreshToken:   tok.RefreshToken,
		Recipe:         tok.Recipe,
		UserID:         tok.UserID,
		AccountID:      tok.AccountID,
		DisplayAccount: tok.DisplayAccountID,
		MCCMNC:         p.conn.cfg.MCCMNC,
		Language:       p.auth.Lang,
		AppVer:         p.auth.AppVer,
		DeviceName:     p.auth.Device.Name,
		DeviceModel:    p.auth.Device.Model,
		DeviceOS:       p.auth.Device.OSVersion,
		MintedAt:       time.Now().Unix(),
	}
	p.conn.bridge.Log.Info().Str("kakao_device", meta.DeviceUUID[:12]).
		Int64("kakao_user_id", meta.UserID).Str("agent", meta.Agent).
		Bool("password_stored", meta.Password != "").
		Msg("Kakao login obtained; storing it as a bridgev2 user login")
	ul, err := p.user.NewLogin(ctx, &database.UserLogin{
		UserMXID:   p.user.MXID,
		ID:         networkid.UserLoginID(meta.DeviceUUID),
		RemoteName: meta.DisplayAccount,
		Metadata:   meta,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("kknet: saving the Kakao login: %w", err)
	}
	// In the background, because bringing the chat list in means creating every portal room, and the
	// login command should not sit on that. What the user gets instead is the bridge state message
	// that Connect sends when it is done.
	go ul.Client.Connect(ul.Log.WithContext(p.conn.bridge.BackgroundCtx))
	return &bridgev2.LoginStep{
		Type:         bridgev2.LoginStepTypeComplete,
		Instructions: fmt.Sprintf("Signed in to KakaoTalk as %s.", meta.DisplayAccount),
		CompleteParams: &bridgev2.LoginCompleteParams{
			UserLoginID: ul.ID,
			UserLogin:   ul,
		},
	}, nil
}

// cleanAccount strips the separators people type (+82 10-1234-5678 -> 821012345678).
func cleanAccount(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r >= '0' && r <= '9' {
			out = append(out, r)
		}
	}
	return string(out)
}
