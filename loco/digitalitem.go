// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

// Kakao's animated sticker assets ("digital items") are ordinary animated WebP (or GIF) files whose first
// 128 bytes are encrypted. The asset served for 4412207.emot_034.webp is 258358 bytes with no RIFF header
// at all, which is why every Matrix client called it a broken image; the rest of the file is untouched
// WebP (chunk names VP8 / ALPH / ANMF appear at their normal places).
//
// The decoder is in the app: com.kakao.digitalitem.image.lib.ImageDecode calls
// nativeWebpImageDecode(path, frame, encrypted), and libdigitalitem_image_decoder.so decrypts the first
// 128 bytes with a fixed-key stream cipher before handing the bytes to libwebp (measured by calling its
// exported cryptData under qemu-aarch64). The cipher is three Galois LFSRs clocked stop-and-go by the last
// bit of the third one, output being the XOR of the last two clocked state bits:
//
//	key      "a271730728cbe141e47fd9d677e9006d", compiled into the library (not per item, not per pack)
//	seeds    a = 0x888c1214<<32 | key[8:12], b = 0x2527ac91<<32 | key[4:8], c = 0x12000032<<32 | key[0:4]
//	feedback c (31 bit, always clocked) mask 0x80000000^0x40000031
//	         a (clocked when c's bit0 is 0)   mask 0xF0000000^0x08000001
//	         b (clocked when c's bit0 is 1)   mask 0xC0000000^0x20000010
//
// Verified against captured assets: decrypting the head of 4412207.emot_034.webp and 4412206.emot_037.webp
// gives RIFF....WEBP with a VP8X canvas of 360x360 (TestDigitalItemAssetIsAnEncryptedWebP).

const digitalItemKey = "a271730728cbe141e47fd9d677e9006d"

// digitalItemEncryptedBytes is how much of the asset the client encrypts. The library refuses anything
// shorter ("Encrypted Data too small < 128bytes.").
const digitalItemEncryptedBytes = 128

// digitalItemKeystream is the fixed mask for the head of an item asset, generated once.
var digitalItemKeystream = func() [digitalItemEncryptedBytes]byte {
	var out [digitalItemEncryptedBytes]byte
	c := seedLFSR(0x12000032, digitalItemKey[0:4])
	b := seedLFSR(0x2527ac91, digitalItemKey[4:8])
	a := seedLFSR(0x888c1214, digitalItemKey[8:12])
	for i := range out {
		var lastA, lastB byte = 0, 1
		var acc byte
		for bit := 0; bit < 8; bit++ {
			if c&1 == 1 {
				lastB = byte(b & 1)
				if lastB == 1 {
					b = ((b >> 1) | 0xC0000000) ^ 0x20000010
				} else {
					b = (b >> 1) & 0x3FFFFFFF
				}
				c = ((c >> 1) | 0x80000000) ^ 0x40000031
			} else {
				lastA = byte(a & 1)
				if lastA == 1 {
					a = ((a >> 1) | 0xF0000000) ^ 0x08000001
				} else {
					a = (a >> 1) & 0x0FFFFFFF
				}
				c = (c >> 1) & 0x7FFFFFFF
			}
			acc = (acc << 1) | (lastA ^ lastB)
		}
		out[i] = acc
	}
	return out
}()

func seedLFSR(def uint64, key string) uint64 {
	s := def
	for i := 0; i < len(key); i++ {
		s = (s << 8) | uint64(key[i])
	}
	return s
}

// DecryptItemAsset returns the asset with its head decrypted, and whether the result is an image a client
// can read. It changes only the first 128 bytes; an asset that is already plain, or that decrypts to
// something else (a different key, a newer format), is reported as not usable.
func DecryptItemAsset(data []byte) ([]byte, bool) {
	if len(data) < digitalItemEncryptedBytes {
		return nil, false
	}
	out := make([]byte, len(data))
	copy(out, data)
	for i := 0; i < digitalItemEncryptedBytes; i++ {
		out[i] ^= digitalItemKeystream[i]
	}
	if !KnownImageHeader(out) {
		return nil, false
	}
	return out, true
}

// KnownImageHeader checks the container rather than the label Kakao puts on the HTTP response.
func KnownImageHeader(b []byte) bool {
	switch {
	case len(b) > 12 && string(b[0:4]) == "RIFF" && string(b[8:12]) == "WEBP":
		return true
	case len(b) > 6 && (string(b[0:6]) == "GIF87a" || string(b[0:6]) == "GIF89a"):
		return true
	case len(b) > 8 && string(b[0:8]) == "\x89PNG\r\n\x1a\n":
		return true
	case len(b) > 2 && b[0] == 0xFF && b[1] == 0xD8 && b[2] == 0xFF:
		return true
	}
	return false
}
