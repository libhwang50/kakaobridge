// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"strings"
	"testing"

	"github.com/libhwang50/kakaobridge/loco"
)

// A direct media id is written into the mxc:// URI of every Matrix event that shows the media, and the
// framework refuses a URI longer than 255 characters. The id is what this bridge controls, so it is kept
// short: two Kakao numbers, no padding. Everything the framework adds around it is fixed (a 4-byte
// marker, a 12-byte signature, base64) and is not this bridge's to shrink.
func TestAMediaIDNamesTheMessageRatherThanTheAddress(t *testing.T) {
	// The ids are the largest a Kakao chat and message get in this account's history.
	id := messageMediaID(18484167895367455, 3965989416160335874)
	if got := len(string(id)); got > 40 {
		t.Errorf("media id %q is %d characters; Kakao addresses do not need that much", id, got)
	}
	// The address is deliberately not in there: Kakao signs DNA media for days, not years
	// (docs/protocol-notes.md §8), so an id carrying the address would expire with it.
	if strings.Contains(string(id), "kakaocdn") || strings.Contains(string(id), "credential") {
		t.Errorf("media id %q carries an address, which stops being true", id)
	}
	chatID, logID, err := parseMessageMediaID(string(id))
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if chatID != 18484167895367455 || logID != 3965989416160335874 {
		t.Errorf("round trip read chat %d message %d", chatID, logID)
	}
}

// An id that did not come from this bridge should be refused rather than guessed at: it arrives over
// HTTP from a client, and reading it wrongly would answer with the wrong media.
func TestAMediaIDThatIsNotOursIsRefused(t *testing.T) {
	for _, bad := range []string{
		"",
		"482943007629834.3965989416160335874", // ours, without the marker
		"m482943007629834",                    // no message named
		"mchat.log",                           // not numbers
		"m482943007629834.",                   // message left empty
		"m482943007629834.1.2",                // one attachment per message, not two
	} {
		if _, _, err := parseMessageMediaID(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

// What may be addressed at Kakao is decided by what the attachment names: a signed address can be
// re-read at any later time, while an item's bytes have to be looked at before anything can be shown
// (docs/protocol-notes.md §11). A thumbnail-only photo counts, because for older media the thumbnail is
// sometimes the only thing still signed.
func TestSignedMediaIsAddressedAtKakaoAndItemsAreNot(t *testing.T) {
	signed := "https://talk.kakaocdn.net/dna/abc.jpg?credential=xx&expires=1790000000&signature=yy"
	thumb := "https://talk.kakaocdn.net/dna/abc.jpg?credential=xx&expires=1790000000&signature=zz&convert=resize&w=120"
	cases := []struct {
		name string
		meta loco.AttachmentMeta
		want bool
	}{
		{"a photo with its full address", loco.AttachmentMeta{URL: signed}, true},
		{"a photo whose full address is gone", loco.AttachmentMeta{ThumbnailURL: thumb}, true},
		{"a voice note", loco.AttachmentMeta{URL: signed, Name: "voice-1.mp3"}, true},
		{"an emote, which names an item", loco.AttachmentMeta{Path: "2212560.emot_047.png"}, false},
		{"an animated sticker", loco.AttachmentMeta{EmoticonItemPath: "4412207.emot_048.webp"}, false},
		{"an attachment with nothing in it", loco.AttachmentMeta{}, false},
	}
	for _, tc := range cases {
		if got := directMediaEligible(&tc.meta); got != tc.want {
			t.Errorf("%s: eligible=%v, want %v", tc.name, got, tc.want)
		}
	}
}
