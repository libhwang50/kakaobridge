// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import "testing"

// The names below are invented for the test; the key spellings (ols, ol, li, ln, desc, liu, lt, otk)
// and the shapes are as measured on a room created to read them (docs/protocol-notes.md §7b).

func linkElem(key string, li int64, name, desc, image string, linkType int32, token int64) Elem {
	return Elem{Key: key, Type: tDoc, Val: []Elem{
		{Key: "li", Type: tInt64, Val: li},
		{Key: "otk", Type: tInt64, Val: token},
		{Key: "ln", Type: tString, Val: name},
		{Key: "lu", Type: tString, Val: "https://open.kakao.com/o/example"},
		{Key: "liu", Type: tString, Val: image},
		{Key: "olu", Type: tDoc, Val: []Elem{
			{Key: "userId", Type: tInt64, Val: int64(1001)},
			{Key: "nn", Type: tString, Val: "some.open.profile"},
		}},
		{Key: "lt", Type: tInt32, Val: linkType},
		{Key: "desc", Type: tString, Val: desc},
		{Key: "ml", Type: tInt32, Val: int32(4000)},
	}}
}

func TestARoomsNameComesFromItsLink(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "status", Type: tInt32, Val: int32(0)},
		{Key: "ols", Type: tArr, Val: []Elem{
			// An account's own open profile link is in the same answer; it must not be taken for the room.
			linkElem("0", 4859, "some.person", "a person's own status", "", 1, 111),
			linkElem("1", 4860, "Bridge Dev Room",
				"We are building a bridge.\n\nTry KakaoTalk from any Matrix client.",
				"https://open.kakaocdn.net/dn/room/image.jpg", 8, 222),
		}},
	})
	link, err := ParseSyncLinkResponse(body, 4860)
	if err != nil {
		t.Fatalf("reading the room's link: %v", err)
	}
	if link.Name != "Bridge Dev Room" {
		t.Errorf("name = %q, want the name the room carries", link.Name)
	}
	if link.Description != "We are building a bridge.\n\nTry KakaoTalk from any Matrix client." {
		t.Errorf("description = %q, want the room's intro", link.Description)
	}
	if link.ImageURL != "https://open.kakaocdn.net/dn/room/image.jpg" {
		t.Errorf("picture = %q, want the full-size room image", link.ImageURL)
	}
	if link.Type != 8 {
		t.Errorf("type = %d, want 8: a chatroom link", link.Type)
	}
}

// A link the answer does not contain is an error rather than an unnamed room: the chat named the link,
// so a list without it is not the answer we were given.
func TestALinkTheListDoesNotNameIsAnError(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "status", Type: tInt32, Val: int32(0)},
		{Key: "ols", Type: tArr, Val: []Elem{linkElem("0", 4859, "some.person", "", "", 1, 111)}},
	})
	if _, err := ParseSyncLinkResponse(body, 4860); err == nil {
		t.Fatal("expected an error when the link is not in the answer")
	}
}

// A rename or a new intro reaches clients as a SYNCLINKUP push carrying the whole link.
func TestALinkUpdateCarriesTheNewProfile(t *testing.T) {
	body := EncodeDoc([]Elem{
		linkElem("ol", 4860, "Bridge Dev Room (renamed)", "A new intro.", "", 8, 333),
		{Key: "status", Type: tInt32, Val: int32(0)},
	})
	link, err := ParseLinkUpdate(body)
	if err != nil {
		t.Fatalf("reading the pushed link: %v", err)
	}
	if link.LinkID != 4860 {
		t.Errorf("link id = %d, want the one the chat points at", link.LinkID)
	}
	if link.Name != "Bridge Dev Room (renamed)" {
		t.Errorf("name = %q, want the new name", link.Name)
	}
	if link.OpenToken != 333 {
		t.Errorf("token = %d, want the new token: it changes when the profile is written", link.OpenToken)
	}
}

func TestALinkUpdateWithoutALinkIsAnError(t *testing.T) {
	body := EncodeDoc([]Elem{{Key: "status", Type: tInt32, Val: int32(0)}})
	if _, err := ParseLinkUpdate(body); err == nil {
		t.Fatal("expected an error when no link is named")
	}
}
