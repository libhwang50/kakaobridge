// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
)

// The bot profile is applied by mautrix from appservice.bot.*, which a network connector cannot see,
// so the default is filled in here. Filling it must never override what the operator wrote.
func TestDefaultBotProfileLeavesTheOperatorAlone(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		want       string
	}{
		{"nothing set takes the network mark", "", defaultBotAvatar},
		{"an explicit avatar wins", "mxc://example.org/custom", "mxc://example.org/custom"},
		{"remove still means remove", "remove", "remove"},
	}
	for _, tc := range cases {
		cfg := &bridgeconfig.Config{}
		cfg.AppService.Bot.Avatar = tc.configured
		applyDefaultBotProfile(cfg)
		if got := cfg.AppService.Bot.Avatar; got != tc.want {
			t.Errorf("%s: avatar is %q, want %q", tc.name, got, tc.want)
		}
	}
}

// UpdateBotProfile uses ParsedAvatar, not the string, so a default that only sets the string would
// silently do nothing.
func TestDefaultBotProfileSetsTheParsedFormToo(t *testing.T) {
	cfg := &bridgeconfig.Config{}
	applyDefaultBotProfile(cfg)
	parsed := cfg.AppService.Bot.ParsedAvatar
	if parsed.IsEmpty() {
		t.Fatal("ParsedAvatar is empty; the homeserver would never be told about the default avatar")
	}
	if parsed.Homeserver != "libhwang.cc" || parsed.FileID != "9oewcYTN0g2BArGdX9LzU3KowkQzUlC5" {
		t.Fatalf("parsed the wrong media id: %+v", parsed)
	}
}
