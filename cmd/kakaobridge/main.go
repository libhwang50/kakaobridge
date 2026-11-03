// SPDX-License-Identifier: AGPL-3.0-or-later

// kakaobridge is the KakaoTalk bridge: mautrix's bridgev2 owns the Matrix side (appservice, portals,
// ghosts, encryption, management-room commands, provisioning), and the kknet package owns KakaoTalk -
// the LOCO session, the REST endpoints the shipped clients use, and the conversion between the two.
package main

import (
	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
	"maunium.net/go/mautrix/bridgev2/matrix/mxmain"
	"maunium.net/go/mautrix/id"

	"github.com/libhwang50/kakaobridge/kknet"
)

// Version is the bridge version; InitVersion compares it against the build tags.
var Version = "0.0.0-dev"
var Tag string
var Commit string
var RawBuildTime string

// defaultBotAvatar is the KakaoTalk mark, uploaded to a federated homeserver so that anyone running
// this bridge can load it.
const defaultBotAvatar = "mxc://libhwang.cc/9oewcYTN0g2BArGdX9LzU3KowkQzUlC5"

// applyDefaultBotProfile gives the bot the network's mark unless the operator asked for something
// else. It has to happen here: applying appservice.bot.avatar is mautrix's job
// (matrix.Connector.UpdateBotProfile, run when the Matrix connector starts), and a network connector
// deliberately cannot see that part of the config. An explicit `avatar:` still wins, and `remove`
// still clears it - both leave Avatar non-empty, which is what this checks.
func applyDefaultBotProfile(cfg *bridgeconfig.Config) {
	bot := &cfg.AppService.Bot
	if bot.Avatar != "" {
		return
	}
	bot.Avatar = defaultBotAvatar
	// ParsedAvatar is what UpdateBotProfile actually uses; it is normally filled while parsing the
	// config, which a default set after parsing has to do by hand.
	bot.ParsedAvatar = id.MustParseContentURI(defaultBotAvatar)
}

func main() {
	m := mxmain.BridgeMain{
		Name:        "kakaobridge",
		Description: "A Matrix-KakaoTalk puppeting bridge",
		URL:         "https://github.com/libhwang50/kakaobridge",
		Version:     Version,
		Connector:   kknet.New(),
	}
	// PostInit runs after the config is loaded and before anything talks to the homeserver.
	m.PostInit = func() { applyDefaultBotProfile(m.Config) }
	m.InitVersion(Tag, Commit, RawBuildTime)
	m.Run()
}
