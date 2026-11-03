// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

	"github.com/libhwang50/kakaobridge/loco"
)

// One Kakao device credential carries one LOCO session, so a client that is already connecting or
// already connected has to refuse another Connect: the second session would get the first kicked out.
// The login flow starts a Connect itself (PasscodeLogin.complete), which is what this guard protects.
func TestSecondConnectIsRefusedWhileOneIsLive(t *testing.T) {
	c := &Client{}
	if !c.claimConnect() {
		t.Fatal("the first Connect must be accepted")
	}
	if c.claimConnect() {
		t.Fatal("a Connect while another one is in progress must be refused")
	}
	// startSession puts the session here once the socket is up, and Connect then returns: from that
	// point on it is the session, not the in-progress flag, that holds the claim.
	c.sess = &loco.Session{}
	c.endConnect()
	if c.claimConnect() {
		t.Fatal("a Connect must be refused while a session is live")
	}
}

// A Disconnect that lands while a Connect is still running (logging out, or shutting down, before the
// socket came up) must not leave the claim held: the session was never opened, so stopSession has
// nothing to close, and connecting the login again has to work.
func TestClaimIsReleasedAfterStoppingBeforeTheSessionOpened(t *testing.T) {
	c := &Client{}
	if !c.claimConnect() {
		t.Fatal("the first Connect must be accepted")
	}
	c.stopSession()
	if c.claimConnect() {
		t.Fatal("a Connect still in progress must hold the claim")
	}
	c.endConnect() // what Connect's deferred cleanup does on every return path
	if !c.claimConnect() {
		t.Fatal("after the Connect finished and the session was never opened, reconnecting must be allowed")
	}
}
