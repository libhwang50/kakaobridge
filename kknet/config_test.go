// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
)

// The framework reads the network section as an opaque node and decodes it into whatever GetConfig
// returned (mxmain.LoadConfig). This is the same two steps, so a mistake in our yaml tags shows up
// here instead of as a bridge that starts with an empty config.
func TestNetworkSectionDecodes(t *testing.T) {
	doc := []byte(`
network:
    account: "821012345678"
    password: "hunter2"
    agent: android
    app_version: 26.9.1
    language: ko
    mccmnc: "45006"
    tokens_file: /data/tokens.json
    tokens_mxid: "@me:example.com"
    reaction_images: true
    device:
        name: MATRIX-BRIDGE
        model: bridge-linux
        os_version: "16"
        x_vc: 81dff230d8731c63
`)
	var full bridgeconfig.Config
	if err := yaml.Unmarshal(doc, &full); err != nil {
		t.Fatalf("full config parse: %v", err)
	}
	var c Connector
	if full.Network.Kind == 0 {
		t.Fatal("the network section was not captured by bridgeconfig.Config")
	}
	_, data, _ := c.GetConfig()
	if err := full.Network.Decode(data); err != nil {
		t.Fatalf("network decode: %v", err)
	}
	want := Config{
		Account: "821012345678", Password: "hunter2", Agent: "android", AppVer: "26.9.1",
		Language: "ko", MCCMNC: "45006", TokensFile: "/data/tokens.json", TokensMXID: "@me:example.com",
		ReactionImages: true,
		Device:         DeviceConfig{Name: "MATRIX-BRIDGE", Model: "bridge-linux", OSVersion: "16", XVC: "81dff230d8731c63"},
	}
	if c.cfg != want {
		t.Fatalf("network config did not round-trip:\n got %+v\nwant %+v", c.cfg, want)
	}
	c.cfg.Fill()
	if got := c.cfg.accountID(); got != "01012345678" {
		t.Errorf("android account form: got %q, want the local form 01012345678", got)
	}
}

// Reactions arriving as Unicode characters is the default, because a client that cannot draw a custom
// emoji shows the URI of a picture as a literal string. The option to show Kakao's own picture is a
// config switch, so its name is worth a test.
func TestReactionsShowCharactersUnlessThePictureIsAskedFor(t *testing.T) {
	var c Config
	if err := yaml.Unmarshal([]byte("account: a\n"), &c); err != nil {
		t.Fatal("a network section without the option should parse")
	}
	if c.ReactionImages {
		t.Error("reactions default to the character, not the picture")
	}
	if err := yaml.Unmarshal([]byte("reaction_images: true\n"), &c); err != nil {
		t.Fatalf("reaction_images: %v", err)
	}
	if !c.ReactionImages {
		t.Error("reaction_images: true did not reach the config")
	}
}
