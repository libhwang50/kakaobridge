// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

// Branding is the part of a bridge a user judges without reading a line of code: what the rooms are
// called and what they look like. These tests keep the two rules that decide it.

import (
	"bytes"
	"image/png"
	"testing"

	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/libhwang50/kakaobridge/loco"
)

func TestMemoChatIsNamedAfterTheAccount(t *testing.T) {
	members := []loco.Member{
		{UserID: 447137810, Nickname: "황/인채", ProfileImageURL: "https://cdn/small.jpg",
			FullProfileImageURL: "https://cdn/full.jpg"},
	}
	name, avatar := memoChatMeta(members, 447137810)
	if name != "황/인채" {
		t.Errorf("a memo chat is titled with the account nickname in KakaoTalk; got %q", name)
	}
	if avatar != "https://cdn/full.jpg" {
		t.Errorf("a room avatar is displayed large, so it must use the full-size picture; got %q", avatar)
	}
}

func TestMemoChatPrefersTheAccountMember(t *testing.T) {
	// If Kakao ever lists more than one member here, the answer is still the account's own name, not
	// the first entry in the list.
	members := []loco.Member{
		{UserID: 999, Nickname: "someone else", FullProfileImageURL: "https://cdn/other.jpg"},
		{UserID: 447137810, Nickname: "me", FullProfileImageURL: "https://cdn/me.jpg"},
	}
	name, avatar := memoChatMeta(members, 447137810)
	if name != "me" || avatar != "https://cdn/me.jpg" {
		t.Errorf("got %q/%q, want the account's own name and picture", name, avatar)
	}
}

func TestMemoChatWithoutProfileDataNamesNothing(t *testing.T) {
	name, avatar := memoChatMeta([]loco.Member{{UserID: 447137810}}, 447137810)
	if name != "" || avatar != "" {
		t.Errorf("no data must mean no name and no avatar, got %q/%q", name, avatar)
	}
}

func TestCurrentRoomAvatarReadsBothForms(t *testing.T) {
	if got := currentRoomAvatar(nil); got != "" {
		t.Errorf("no state event must read as no avatar, got %q", got)
	}
	parsed := &event.Event{Content: event.Content{
		Parsed: &event.RoomAvatarEventContent{URL: "mxc://example/parsed"},
	}}
	if got := currentRoomAvatar(parsed); got != "mxc://example/parsed" {
		t.Errorf("parsed content: got %q", got)
	}
	raw := &event.Event{Content: event.Content{Raw: map[string]any{"url": "mxc://example/raw"}}}
	if got := currentRoomAvatar(raw); got != "mxc://example/raw" {
		t.Errorf("raw content: got %q", got)
	}
	empty := &event.Event{Content: event.Content{Raw: map[string]any{}}}
	if got := currentRoomAvatar(empty); got != "" {
		t.Errorf("a room with no avatar must read as empty, got %q", got)
	}
}

// The logo is embedded in the binary, so a bad asset ships to every user of this bridge. Decoding it
// here is the cheap way to notice.
func TestKakaoTalkLogoIsARealPNG(t *testing.T) {
	if len(kakaoLogoPNG) == 0 {
		t.Fatal("the KakaoTalk logo asset is empty")
	}
	img, err := png.Decode(bytes.NewReader(kakaoLogoPNG))
	if err != nil {
		t.Fatalf("assets/kakaotalk-logo.png is not a PNG: %v", err)
	}
	b := img.Bounds()
	if b.Dx() < 128 || b.Dy() < 128 {
		t.Errorf("the logo is %dx%d; a room avatar that small is shown blurry", b.Dx(), b.Dy())
	}
}

func TestLogoCacheIsReadThroughTheLock(t *testing.T) {
	c := &Connector{}
	if got := c.logoMXC(); got != id.ContentURIString("") {
		t.Errorf("a fresh connector has no uploaded logo, got %q", got)
	}
	c.setLogoMXC("mxc://example/logo")
	if got := c.logoMXC(); got != "mxc://example/logo" {
		t.Errorf("the uploaded URI must be remembered, got %q", got)
	}
	// The network descriptor reports it, which is how a space gets its avatar.
	if got := c.GetName().NetworkIcon; got != "mxc://example/logo" {
		t.Errorf("GetName must report the icon, got %q", got)
	}
}
