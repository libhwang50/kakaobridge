// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/libhwang50/kakaobridge/loco"
)

func testClient() *Client {
	conn := New()
	conn.cfg.Agent = "android"
	ul := &bridgev2.UserLogin{UserLogin: &database.UserLogin{ID: "dev"}}
	return NewClient(conn, ul, &LoginMeta{UserID: 447137810, DeviceUUID: "dev"})
}

// Kakao's 1:1 rooms belong to one account, so they carry this login as the portal receiver; a room
// with more members is shared and must not, or two bridge users would get separate rooms for it.
func TestPortalKeysByChatType(t *testing.T) {
	c := testClient()
	for _, ct := range []string{"DirectChat", "PlusChat", "MemoChat"} {
		key := c.portalKeyFor(123, ct)
		if string(key.Receiver) != "dev" {
			t.Errorf("%s: DM portal must name its receiver, got %+v", ct, key)
		}
		if string(key.ID) != "123" {
			t.Errorf("%s: portal id = %q", ct, key.ID)
		}
	}
	for _, ct := range []string{"OpenChat", "GroupChat", "MultiChat"} {
		if key := c.portalKeyFor(123, ct); key.Receiver != "" {
			t.Errorf("%s: shared room must not have a receiver, got %+v", ct, key)
		}
	}
}

func TestTextMessageConversion(t *testing.T) {
	content := convertedContentOf(loco.ChatLog{Type: loco.MsgText, Text: "그래, 브리지 잘 되네"})
	if content.MsgType != event.MsgText {
		t.Errorf("a Kakao text message must stay m.text, got %s", content.MsgType)
	}
	if content.Body != "그래, 브리지 잘 되네" {
		t.Errorf("body changed: %q", content.Body)
	}
}

// Media is not bridged as media yet. The honest failure is a notice that says what was there, while
// keeping the caption Kakao stores in the same field.
func TestUnbridgedMediaTypeSaysSo(t *testing.T) {
	photo := convertedContentOf(loco.ChatLog{Type: loco.MsgPhoto, Text: "사진"})
	if photo.MsgType != event.MsgNotice {
		t.Errorf("unbridged media should not be presented as an ordinary message: %s", photo.MsgType)
	}
	if photo.Body != "사진\n(photo message, not bridged)" {
		t.Errorf("caption lost or label wrong: %q", photo.Body)
	}
	bare := convertedContentOf(loco.ChatLog{Type: loco.MsgFile})
	if bare.Body != "(file message, not bridged)" {
		t.Errorf("empty-caption file: %q", bare.Body)
	}
	unknown := convertedContentOf(loco.ChatLog{Type: 999})
	if unknown.Body != "(type 999 message, not bridged)" {
		t.Errorf("unknown types must not panic or vanish: %q", unknown.Body)
	}
}

// chatIDOf is the only place a portal key turns back into a Kakao chat id; a hand-made or migrated
// portal must fail loudly instead of querying the server with garbage.
func TestChatIDOfRejectsNonNumericKeys(t *testing.T) {
	c := testClient()
	if id, err := chatIDOf(c.portalKeyFor(482943465622166, "DirectChat")); err != nil || id != 482943465622166 {
		t.Errorf("round trip failed: %d %v", id, err)
	}
	if _, err := chatIDOf(networkid.PortalKey{ID: "not-a-chat-id"}); err == nil {
		t.Error("expected an error for a non-numeric portal id")
	}
}

// A Kakao read marker names a log id, and it may name a message the bridge never bridged - a type it
// skips, or history Kakao did not serve. The receipt then belongs on the newest message before it,
// because "read up to here" is about the member's position, not about a specific event.
func TestReadMarkerLandsOnTheNearestBridgedMessage(t *testing.T) {
	bridged := []int64{100, 200, 300}
	for _, tc := range []struct {
		name      string
		watermark int64
		want      int64
		ok        bool
	}{
		{"exact match", 200, 200, true},
		{"newest message", 300, 300, true},
		{"a skipped message in the middle", 250, 200, true},
		{"past everything bridged", 999, 300, true},
		{"older than anything bridged", 50, 0, false},
	} {
		got, ok := readReceiptTarget(bridged, tc.watermark)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: watermark %d -> %d,%v want %d,%v", tc.name, tc.watermark, got, ok, tc.want, tc.ok)
		}
	}
	// With nothing bridged in the room there is nothing honest to mark read.
	if got, ok := readReceiptTarget(nil, 42); ok || got != 0 {
		t.Errorf("empty room: got %d,%v", got, ok)
	}
}

// Log ids arrive in order from backfill and pushes, but a message sent from Matrix is recorded when
// the write returns, so the index has to stay sorted and free of duplicates.
func TestBridgedIndexStaysSortedAndUnique(t *testing.T) {
	c := testClient()
	for _, id := range []int64{10, 30, 20, 30, 40, 5} {
		c.rememberBridged(7, id)
	}
	got := c.bridged[7]
	want := []int64{5, 10, 20, 30, 40}
	if len(got) != len(want) {
		t.Fatalf("index = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index = %v, want %v", got, want)
		}
	}
	if target, ok := readReceiptTarget(got, 30); !ok || target != 30 {
		t.Errorf("exact lookup after an out-of-order insert: %d,%v", target, ok)
	}
	c.rememberBridged(0, 1)
	c.rememberBridged(9, 0)
	if len(c.bridged) != 1 {
		t.Errorf("a chat or log id of zero must not create an entry: %v", c.bridged)
	}
}
