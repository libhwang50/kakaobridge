// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"strings"
	"testing"

	"maunium.net/go/mautrix/event"
)

// What can be sent to Kakao is a protocol fact, not a preference: LOCO WRITE carries a plain string,
// and a photo needs the prepare/upload/WRITEMEDIA path instead. Formatting is dropped (Kakao bubbles are
// plain text); anything the bridge cannot send yet is refused in words a user can act on.
func TestKakaoTextOf(t *testing.T) {
	text, err := kakaoTextOf(&event.MessageEventContent{MsgType: event.MsgText, Body: "브리지에서 보냄"})
	if err != nil || text != "브리지에서 보냄" {
		t.Errorf("plain text must pass through: %q %v", text, err)
	}
	if got, err := kakaoTextOf(&event.MessageEventContent{MsgType: event.MsgNotice, Body: "nota bene"}); err != nil || got != "nota bene" {
		t.Errorf("m.notice should be sent as text too: %q %v", got, err)
	}
	if got, err := kakaoTextOf(&event.MessageEventContent{MsgType: event.MsgEmote, Body: "waves"}); err != nil || got != "* waves" {
		t.Errorf("emote: %q %v", got, err)
	}
	for _, mt := range []event.MessageType{event.MsgLocation, event.MessageType("m.poll.start")} {
		_, err := kakaoTextOf(&event.MessageEventContent{MsgType: mt, Body: "x"})
		if err == nil || !strings.Contains(err.Error(), "text and photos") {
			t.Errorf("%s must be refused with an explanation, got %v", mt, err)
		}
	}
	// m.image/m.video/m.file are implemented; audio is the one that stays refused, and the message says
	// what to do instead of naming the problem.
	for _, mt := range []event.MessageType{event.MsgAudio, event.MessageType("org.matrix.msc3245.voice")} {
		err := mediaRefusal(mt)
		if err == nil || !strings.Contains(err.Error(), string(mt)) || !strings.Contains(err.Error(), "as a file") {
			t.Errorf("%s must be refused by name with an alternative, got %v", mt, err)
		}
	}
	if _, err := kakaoTextOf(&event.MessageEventContent{MsgType: event.MsgText}); err == nil {
		t.Error("an empty body must not be sent")
	}
}
