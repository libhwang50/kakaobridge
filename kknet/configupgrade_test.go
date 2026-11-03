// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"strings"
	"testing"

	"go.mau.fi/util/configupgrade"
	"gopkg.in/yaml.v3"

	"maunium.net/go/mautrix/bridgev2/bridgeconfig"
)

// mxmain rebuilds the config file from the example text (configupgrade.DoBytes marshals the example
// tree after copying user values into it), so a network section with no upgrader steps gets reset to
// the example defaults. This is the regression the deployed bridge hit: tokens_file was in the file,
// and the process still started with an empty network config.
func TestNetworkValuesSurviveConfigUpgrade(t *testing.T) {
	c := New()
	example, _, upgrader := c.GetConfig()

	// The same wrapping mxmain.makeFullExampleConfig applies to the network example.
	var base strings.Builder
	base.WriteString("# Network-specific config options\nnetwork:\n")
	for line := range strings.SplitSeq(example, "\n") {
		base.WriteString("    " + line + "\n")
	}
	base.WriteString("\n")

	full := configupgrade.MergeUpgraders(base.String(),
		&configupgrade.ProxyUpgrader{Target: upgrader, Prefix: []string{"network"}})
	userCfg := []byte(`network:
    account: "821012345678"
    ask_for_credentials: true
    password: "hunter2"
    agent: android
    mccmnc: "45006"
    tokens_file: /data/tokens.json
    tokens_mxid: "@me:libhwang.cc"
    device:
        name: MATRIX-BRIDGE
        model: bridge-linux
`)
	out, err := configupgrade.DoBytes(userCfg, full)
	if err != nil {
		t.Fatalf("config upgrade: %v", err)
	}

	var parsed bridgeconfig.Config
	if err := yaml.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("parse upgraded config: %v", err)
	}
	var got Config
	if err := parsed.Network.Decode(&got); err != nil {
		t.Fatalf("decode network section: %v", err)
	}
	if !got.AskForCredentials || got.TokensFile != "/data/tokens.json" || got.TokensMXID != "@me:libhwang.cc" ||
		got.Account != "821012345678" || got.Password != "hunter2" || got.Device.Name != "MATRIX-BRIDGE" {
		t.Fatalf("network values were not carried through the upgrade:\n%s\n=> %+v", out, got)
	}
}
