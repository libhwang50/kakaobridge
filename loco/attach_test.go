// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"testing"
	"time"
)

// The still preview of a sticker item is <itemCode>.thum_<index>.png; verified against the CDN (4412207
// .thum_034.png -> 200 image/png, 210x210) and matching how the client builds store thumbnails in
// defpackage/nkp.java. DNA media (photos, files) has no item key, so there is nothing to derive.
func TestStillPreviewItemURLIsDerivedFromTheItemKey(t *testing.T) {
	cases := []struct {
		meta AttachmentMeta
		want string
	}{
		{AttachmentMeta{Path: "4412207.emot_034.webp"}, "https://item.kakaocdn.net/dw/4412207.thum_034.png"},
		{AttachmentMeta{EmoticonItemPath: "2212560.emot_047.png"}, "https://item.kakaocdn.net/dw/2212560.thum_047.png"},
		{AttachmentMeta{Path: "4412207.thum_034.png"}, ""},
		{AttachmentMeta{URL: "https://talk.kakaocdn.net/dw/abc.jpg"}, ""},
	}
	for _, c := range cases {
		if got := c.meta.StillPreviewItemURL(); got != c.want {
			t.Errorf("%q: got %q, want %q", c.meta.Path+c.meta.EmoticonItemPath, got, c.want)
		}
	}
}

// A reaction's item code resolves like a message's inline mini emoji: "1200509_029" is served at
// 1200509.emoji_029.png (measured: all 114 built-in reaction items answer 200 image/png, 180x180,
// unsigned and unexpiring; a two-digit index answers 404, so the digits are kept as the code has them).
func TestAMiniEmojiItemIsAddressedFromItsCode(t *testing.T) {
	cases := []struct{ id, want string }{
		{"1200283_027", "https://item.kakaocdn.net/dw/1200283.emoji_027.png"},
		{"1200509_029", "https://item.kakaocdn.net/dw/1200509.emoji_029.png"},
		{"1200509", ""},
		{"_029", ""},
	}
	for _, c := range cases {
		if got := MiniEmojiURL(c.id); got != c.want {
			t.Errorf("%q: got %q, want %q", c.id, got, c.want)
		}
	}
}

// A signed DNA URL states the signature's own lifetime, and it is the shorter of the two windows on the
// attachment: measured on one photo, three days in the URL against two weeks in `expire`. Anything that
// hands Kakao's URL to someone else has to answer with the short one.
func TestASignedURLSaysHowLongTheSignatureLasts(t *testing.T) {
	url := "https://talk.kakaocdn.net/dna/caamNY/TkUUiXGTpNV7.png?credential=zf3biCP&expires=1794206934&signature=gbdwed"
	got, ok := SignedUntil(url)
	if !ok {
		t.Fatal("the signature expiry in the URL was not read")
	}
	if !got.Equal(time.Unix(1794206934, 0)) {
		t.Errorf("got %v, want the instant the URL names", got)
	}
	for _, noExpiry := range []string{
		"https://item.kakaocdn.net/dw/4412207.emot_048.webp", // an item: no signature, no expiry
		"https://talk.kakaocdn.net/dna/x.png",
		"https://talk.kakaocdn.net/dna/x.png?expires=",
		"https://talk.kakaocdn.net/dna/x.png?expires=soon",
		"https://talk.kakaocdn.net/dna/x.png?expires=0",
	} {
		if _, ok := SignedUntil(noExpiry); ok {
			t.Errorf("%q was read as naming an expiry", noExpiry)
		}
	}
}
