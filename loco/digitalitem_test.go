// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"bytes"
	"testing"
)

// The first 256 bytes of two animated sticker assets as item.kakaocdn.net serves them
// (4412207.emot_034.webp, 258358 bytes; 4412206.emot_037.webp, 156754 bytes). Neither has a container
// header, and both decrypt to the same kind of WebP: the key is compiled into the app, not per item.
func TestDigitalItemAssetIsAnEncryptedWebP(t *testing.T) {
	for _, name := range []string{"kakao_animated_emot_head.hex", "kakao_animated_emot_head2.hex"} {
		raw := body(t, name)
		if KnownImageHeader(raw) {
			t.Fatalf("%s: the captured asset should not look like an image as served", name)
		}
		dec, ok := DecryptItemAsset(raw)
		if !ok {
			t.Fatalf("%s: the asset should decrypt to an image", name)
		}
		if string(dec[0:4]) != "RIFF" || string(dec[8:12]) != "WEBP" || string(dec[12:16]) != "VP8X" {
			t.Fatalf("%s: want an animated WebP header, got %q", name, dec[:16])
		}
		le24 := func(o int) int { return 1 + int(dec[o]) | int(dec[o+1])<<8 | int(dec[o+2])<<16 }
		w, h := le24(24), le24(27)
		if w != 360 || h != 360 {
			t.Errorf("%s: the client draws these at 360x360, header says %dx%d", name, w, h)
		}
	}
}

// A plain asset (a static sticker PNG, or a photo from the DNA CDN) must be left alone: only the head of a
// digital item is encrypted, and XOR-ing bytes that were never encrypted would corrupt a good image.
func TestPlainAssetsAreNotDecrypted(t *testing.T) {
	for _, head := range [][]byte{
		[]byte("GIF89a...."),
		[]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'},
		[]byte{0xFF, 0xD8, 0xFF, 0xE0},
	} {
		if _, ok := DecryptItemAsset(append(head, make([]byte, 200)...)); ok {
			t.Errorf("%q should not be treated as an encrypted asset", head[:8])
		}
	}
	if _, ok := DecryptItemAsset(bytes.Repeat([]byte{0x42}, 64)); ok {
		t.Error("an asset shorter than the encrypted header cannot be decrypted")
	}
}
