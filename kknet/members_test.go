// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
)

// The numbers are measured: a room whose own count said 1,976 was answered by GETMEM with exactly 1,000
// member entries, and the account asking was not one of them. A truncated list handed over as complete
// makes the homeserver remove members who are still in the chat - which is how a bridged room vanished
// from the client with "User is not in remote chat".
func TestAMemberListKakaoTruncatedIsNotClaimedComplete(t *testing.T) {
	if memberListIsFull(1000, 1976) {
		t.Error("1,000 of a reported 1,976 is not the whole list")
	}
	if memberListIsFull(2, 3) {
		t.Error("a list missing one member is not the whole list")
	}
	if !memberListIsFull(3, 3) {
		t.Error("a list that covers the count the chat reports is the whole list, and join/leave depends on it")
	}
	if memberListIsFull(4, 0) {
		t.Error("a chat that named no count does not license a claim")
	}
}

// A room whose membership Kakao does not give is bridged as the account alone: it is known to be in the
// chat, and nothing is claimed about the others.
func TestAPublicRoomNamesOnlyTheAccountAsItsMembers(t *testing.T) {
	senderOf := func(id int64) bridgev2.EventSender {
		return bridgev2.EventSender{IsFromMe: true, Sender: networkid.UserID("1001")}
	}
	list := selfMemberList(1001, senderOf)
	if list.IsFull {
		t.Error("the list must not be claimed to be the whole membership")
	}
	if len(list.MemberMap) != 1 {
		t.Fatalf("%d members, want the account alone", len(list.MemberMap))
	}
	for _, m := range list.MemberMap {
		if m.Membership != event.MembershipJoin {
			t.Errorf("membership = %q, want join: the account is in this chat", m.Membership)
		}
		if m.UserInfo != nil {
			t.Error("no profile is invented here; the room's own profile is an open profile, not this one")
		}
	}
}
