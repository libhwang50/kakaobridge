// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"errors"
	"fmt"
	"testing"

	"github.com/libhwang50/kakaobridge/loco"
)

// A chat this account is no longer in answers CHATINFO with inner status -401. That was read from a Team
// Chat the account deleted and from a plus chat it unfollowed; both returned the same 17 bytes, and
// neither appeared in the chat list or in delChatIds (docs/protocol-notes.md §6). Treating only that
// refusal as a "no" is what keeps a quiet group - absent from the list for the boring reason - a room.
func TestAChatThisAccountIsNoLongerInIsRecognisedAsOne(t *testing.T) {
	body := loco.EncodeDoc([]loco.Elem{{Key: "status", Type: 0x10, Val: int32(-401)}})
	_, err := loco.ParseChatInfoResponse(body)
	if err == nil {
		t.Fatal("a refused CHATINFO should be an error")
	}
	if !chatIsGoneForUs(fmt.Errorf("kknet: %w", err)) {
		t.Errorf("-401 from CHATINFO should read as a chat this account is not in: %v", err)
	}
	for _, other := range []error{
		nil,
		&loco.InnerError{Verb: "CHATINFO", Status: 0},     // an answer, not a refusal
		&loco.InnerError{Verb: "GETMEM", Status: -401},    // the same status from another verb
		&loco.InnerError{Verb: "LOGINLIST", Status: -950}, // a refused session token
		errors.New("kknet: CHATINFO(484107646109432): read tcp: i/o timeout"),
	} {
		if chatIsGoneForUs(other) {
			t.Errorf("%v was read as a chat this account is not in", other)
		}
	}
	// A status this account has not measured is not a "no" either: -402 would be a guess.
	if _, err := loco.ParseChatInfoResponse(loco.EncodeDoc([]loco.Elem{{Key: "status", Type: 0x10, Val: int32(-402)}})); chatIsGoneForUs(err) {
		t.Error("an unmeasured CHATINFO status should not be read as leaving")
	}
}

// The chat list is a delta: it names the chats with recent traffic. So a room missing from it is a reason
// to ask, never an answer, and the asking must not include the rooms the list did name.
func TestOnlyRoomsTheListDidNotNameAreAskedAbout(t *testing.T) {
	listed := map[int64]bool{482943007629834: true, 484107600347896: true}
	rooms := []int64{482943007629834, 18181616243038287, 484107600347896, 484107646109432}
	got := chatsNoLongerListed(rooms, listed)
	want := []int64{18181616243038287, 484107646109432}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("asked about %v, want %v", got, want)
	}
}

// One snapshot that named nothing is more likely a list that did not arrive than an account that is in no
// chats, and acting on it would put every room at risk.
func TestAnEmptyListAsksAboutNothing(t *testing.T) {
	rooms := []int64{482943007629834, 484107646109432}
	for _, listed := range []map[int64]bool{nil, {}} {
		if got := chatsNoLongerListed(rooms, listed); got != nil {
			t.Errorf("an empty chat list asked about %v", got)
		}
	}
}
