// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"os"
	"testing"
)

// TestParseLoginlistResponse checks the decoder against a captured LOGINLIST response of our own
// bridge session (testdata/loginlist_response.bin, 2026-10-28). The room/message ids in it are the
// ones already confirmed independently from the PC pcap in docs/protocol-notes.md §8, so they are
// not self-referential: MemoChat 482943007629834 is the account's own self-chat.
func TestParseLoginlistResponse(t *testing.T) {
	b, err := os.ReadFile("../testdata/loginlist_response.bin")
	if err != nil {
		t.Skip("captured response not present:", err)
	}
	st, err := ParseLoginlistResponse(b)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastTokenID == 0 {
		t.Error("lastTokenId (push cursor) not parsed")
	}
	var self ChatData
	for _, r := range st.Rooms {
		if r.Type == "MemoChat" {
			self = r
		}
	}
	if self.ChatID != 482943007629834 {
		t.Errorf("self-chat room = %d, want the MemoChat id from protocol_spec §5; rooms=%+v",
			self.ChatID, st.Rooms)
	}
	if self.Preview == "" {
		t.Error("no message preview parsed")
	}
	var foundDel bool
	for _, id := range st.Deleted {
		if id != 0 {
			foundDel = true
		}
	}
	if !foundDel {
		t.Errorf("delChatIds not parsed: %+v", st.Deleted)
	}
	t.Logf("rooms=%d cursor=%d minLogId=%d eof=%v del=%d", len(st.Rooms), st.LastTokenID,
		st.MinLogID, st.EOF, len(st.Deleted))
	for _, r := range st.Rooms {
		t.Logf("  %-11s chatId=%-16d lastLogId=%d preview=%q", r.Type, r.ChatID, r.LastLog, r.Preview)
	}
}

// Membership and other room pushes name the chat in whatever key that verb uses; the lookup tolerates
// the dictionary's short key and one level of nesting, and says no when there is nothing to name.
func TestPushBodiesNameTheirChat(t *testing.T) {
	cases := []struct {
		name string
		doc  []Elem
		want int64
	}{
		{"spelled out", []Elem{{Key: "chatId", Type: tInt64, Val: int64(484107646109432)}}, 484107646109432},
		{"dictionary key", []Elem{{Key: "c", Type: tInt64, Val: int64(42)}}, 42},
		{"nested one level", []Elem{
			{Key: "room", Type: tDoc, Val: []Elem{{Key: "chatId", Type: tInt64, Val: int64(7)}}},
		}, 7},
		{"names nothing", []Elem{{Key: "status", Type: tInt32, Val: int32(0)}}, 0},
	}
	for _, tc := range cases {
		id, ok := ChatIDOfPush(EncodeDoc(tc.doc))
		if tc.want == 0 {
			if ok {
				t.Errorf("%s: found chat %d, want nothing", tc.name, id)
			}
			continue
		}
		if !ok || id != tc.want {
			t.Errorf("%s: got %d, %v; want %d", tc.name, id, ok, tc.want)
		}
	}
}

// A LEFT push is the live twin of delChatIds: it names one chat that has just gone out of this account's
// list, along with the token id that chat reached. Read from a session that watched a Team Chat being
// deleted and a group chat being left (docs/protocol-notes.md §6).
func TestLeftPushNamesTheChatThatIsGone(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: int64(484107646109432)},
		{Key: "lastTokenId", Type: tInt64, Val: int64(3971414708550387772)},
		{Key: "serviceId", Type: tNull},
		{Key: "status", Type: tInt32, Val: int32(0)},
	})
	left, err := ParseLeftPush(body)
	if err != nil {
		t.Fatalf("a LEFT push that names a chat should parse: %v", err)
	}
	if left.ChatID != 484107646109432 || left.LastTokenID != 3971414708550387772 {
		t.Errorf("read chat %d at token %d", left.ChatID, left.LastTokenID)
	}
	if _, err := ParseLeftPush(EncodeDoc([]Elem{{Key: "status", Type: tInt32, Val: int32(0)}})); err == nil {
		t.Error("a LEFT push that names no chat is nothing the bridge can act on")
	}
	refused := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: int64(484107646109432)},
		{Key: "status", Type: tInt32, Val: int32(-203)},
	})
	if _, err := ParseLeftPush(refused); err == nil {
		t.Error("a refused LEFT must surface as an error, not an empty result")
	}
}
