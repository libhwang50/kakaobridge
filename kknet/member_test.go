// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"context"
	"testing"
)

// A room the person never spoke in is not a room to ask about, so the bridge remembers where a message
// came from. Synthetic ids.
func TestASpokenRoomIsRememberedForAPerson(t *testing.T) {
	c := &Client{meta: &LoginMeta{UserID: 7}}
	c.noteSeen(100, 7) // this account: not a stranger anywhere
	c.noteSeen(100, 8)
	c.noteSeen(200, 8) // the room they spoke in most recently is the one to ask
	c.noteSeen(200, 0)
	if got := c.seenIn[7]; got != 0 {
		t.Errorf("the account's own id is not a stranger to look up, got chat %d", got)
	}
	if got := c.seenIn[0]; got != 0 {
		t.Errorf("a message with no author records nothing, got chat %d", got)
	}
	if got := c.seenIn[8]; got != 200 {
		t.Errorf("want the room the last message came from, got %d", got)
	}
	// With no session open there is nothing to ask, and that must be a quiet nil rather than a
	// request that cannot be made.
	if m := c.lookupStranger(context.Background(), 8); m != nil {
		t.Errorf("no session, no answer: %+v", m)
	}
	if m := c.lookupStranger(context.Background(), 99); m != nil {
		t.Errorf("a person never seen has no room to be asked about: %+v", m)
	}
}
