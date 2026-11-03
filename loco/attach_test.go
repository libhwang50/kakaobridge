// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import "testing"

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
