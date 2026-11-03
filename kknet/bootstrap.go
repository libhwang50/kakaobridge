// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
)

// tokenFile mirrors the `<account>_tokens.json` files the registration tooling outside this bridge
// produced. It exists so a device registered before this bridge had a
// database - or recovered from a capture - can bootstrap a login without asking a human to type a
// passcode again. It is a bootstrap path, not the credential store: the bridge's own store is the
// bridgev2 `user_login` table (LoginMeta). The device *description* (name/model/app version) is not
// in that file on purpose: it comes from the network config, which is also what the phone displays.
type tokenFile struct {
	AccessToken      string `json:"access_token"`
	RefreshToken     string `json:"refresh_token"`
	Recipe           string `json:"recipe"`
	UserID           int64  `json:"userId"`
	AccountID        int64  `json:"accountId"`
	AutoLoginAccount string `json:"autoLoginAccountId"`
	DisplayAccount   string `json:"displayAccountId"`
	CountryISO       string `json:"countryIso"`
	DeviceUUID       string `json:"device_uuid"`
	Agent            string `json:"agent"`
	Lang             string `json:"lang"`
	MCCMNC           string `json:"mccmnc"`
	Revision         int32  `json:"revision"`
}

// LoginMetaFromFile builds a LoginMeta from a token file plus the configured device description.
func LoginMetaFromFile(path string, cfg *Config) (*LoginMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("kknet: reading token file: %w", err)
	}
	var tf tokenFile
	if err := json.Unmarshal(data, &tf); err != nil {
		return nil, fmt.Errorf("kknet: parsing token file: %w", err)
	}
	if tf.AccessToken == "" || tf.DeviceUUID == "" {
		return nil, fmt.Errorf("kknet: %s has no access_token/device_uuid", path)
	}
	if tf.Agent == "" {
		tf.Agent = cfg.Agent
	}
	if tf.Agent == "" {
		tf.Agent = "android"
	}
	acct := cfg.Account
	if acct == "" {
		acct = tf.AutoLoginAccount
	}
	return &LoginMeta{
		Agent:          tf.Agent,
		Account:        acct,
		DeviceUUID:     tf.DeviceUUID,
		AccessToken:    tf.AccessToken,
		RefreshToken:   tf.RefreshToken,
		Recipe:         tf.Recipe,
		UserID:         tf.UserID,
		AccountID:      tf.AccountID,
		DisplayAccount: tf.DisplayAccount,
		MCCMNC:         pick(tf.MCCMNC, cfg.MCCMNC),
		Language:       pick(tf.Lang, cfg.Language),
		AppVer:         cfg.AppVer,
		DeviceName:     cfg.Device.Name,
		DeviceModel:    cfg.Device.Model,
		DeviceOS:       cfg.Device.OSVersion,
	}, nil
}

// pick prefers the value recorded with the credential and falls back to the configured default.
func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// bootstrapLogin turns Config.TokensFile into a real user_login row. mxmain calls StartConnectors
// before StartLogins, so a row written here is connected by the framework during the same start-up -
// we must not connect it ourselves or the device would open two sessions.
func (c *Connector) bootstrapLogin(ctx context.Context) error {
	if c.cfg.TokensFile == "" {
		return nil
	}
	if c.cfg.TokensMXID == "" {
		return fmt.Errorf("kknet: network.tokens_file needs network.tokens_mxid to own it")
	}
	user, err := c.bridge.GetUserByMXID(ctx, c.cfg.TokensMXID)
	if err != nil {
		return fmt.Errorf("kknet: getting the Matrix user for tokens_file: %w", err)
	}
	if len(user.GetUserLogins()) > 0 {
		c.bridge.Log.Info().Stringer("mxid", c.cfg.TokensMXID).
			Msg("Kakao logins already exist in the database; ignoring network.tokens_file")
		return nil
	}
	meta, err := LoginMetaFromFile(c.cfg.TokensFile, &c.cfg)
	if err != nil {
		return err
	}
	_, err = user.NewLogin(ctx, &database.UserLogin{
		UserMXID:   user.MXID,
		ID:         networkid.UserLoginID(meta.DeviceUUID),
		RemoteName: meta.DisplayAccount,
		Metadata:   meta,
	}, nil)
	if err != nil {
		return fmt.Errorf("kknet: importing %s as a Kakao login: %w", c.cfg.TokensFile, err)
	}
	c.bridge.Log.Info().Stringer("mxid", user.MXID).Str("kakao_device", meta.DeviceUUID).
		Int64("kakao_user_id", meta.UserID).Msg("Imported a registered Kakao device from network.tokens_file")
	return nil
}
