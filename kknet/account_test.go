// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import "testing"

// The two namespaces want different forms of the same account id, and the gateway's rejection is a
// generic "check your account or password" (status 30), so the conversion has to be right on its own.
func TestAccountForm(t *testing.T) {
	const intl, local = "821012345678", "01012345678"
	cases := []struct {
		cfg  Config
		in   string
		want string
	}{
		{Config{Agent: "android"}, intl, local},
		{Config{Agent: "android"}, "+82 10-1234-5678", local},
		{Config{Agent: "android"}, local, local},
		{Config{Agent: "win32"}, intl, intl},
		{Config{Agent: "win32"}, local, intl},
		{Config{Agent: "android", AccountForm: "international"}, local, intl},
		{Config{Agent: "win32", AccountForm: "local"}, intl, local},
	}
	for _, c := range cases {
		if got := c.cfg.formOf(c.in); got != c.want {
			t.Errorf("agent=%s form=%q input=%q: got %q, want %q", c.cfg.Agent, c.cfg.AccountForm, c.in, got, c.want)
		}
	}
}
