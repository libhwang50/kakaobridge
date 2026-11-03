// SPDX-License-Identifier: AGPL-3.0-or-later

// kkrenew exercises the bridge's credential path without any Matrix or LOCO involvement: it loads a
// token file, then runs the same login.json call Client.Connect uses. For a *registered* device this
// re-mints (or returns) the same pair with no human in the loop; for a session-only device it fails
// with -100, which is exactly the case the bridge reports as BAD_CREDENTIALS.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/libhwang50/kakaobridge/kknet"
)

func main() {
	tokens := flag.String("tokens", "", "token json; see docs/configuration.md")
	password := flag.String("password", os.Getenv("KK_PASSWORD"), "account password (or set KK_PASSWORD)")
	account := flag.String("account", "", "account id; defaults to autoLoginAccountId in the token file")
	agent := flag.String("agent", "android", "device namespace")
	appVer := flag.String("app-version", "26.9.1", "client version to announce")
	flag.Parse()

	cfg := kknet.Config{Account: *account, Password: *password, Agent: *agent, AppVer: *appVer,
		Language: "ko", MCCMNC: "45006"}
	cfg.Fill()
	if cfg.Password == "" {
		fmt.Fprintln(os.Stderr, "need -password or KK_PASSWORD")
		os.Exit(2)
	}
	meta, err := kknet.LoginMetaFromFile(*tokens, &cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("device %s… agent=%s userId=%d account=%s\n", meta.DeviceUUID[:12], meta.Agent, meta.UserID, meta.DisplayAccount)

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	auth := meta.DeviceAuth(&cfg)
	tok, err := auth.Login(ctx)
	if err != nil {
		fmt.Printf("login.json FAILED: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("login.json status=0 userId=%d account=%q\n", tok.UserID, tok.DisplayAccountID)
	fmt.Printf("access_token %s\n", same(tok.AccessToken, meta.AccessToken))
	fmt.Printf("refresh_token %s\n", same(tok.RefreshToken, meta.RefreshToken))
}

func same(a, b string) string {
	if a == b {
		return "(unchanged)"
	}
	return "CHANGED"
}
