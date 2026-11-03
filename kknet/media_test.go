// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"

	"github.com/libhwang50/kakaobridge/loco"
)

func photoMeta() *loco.AttachmentMeta {
	return &loco.AttachmentMeta{
		URL: "https://talk.kakaocdn.net/dna/abc/def.jpg", Resource: "abc/def.jpg",
		MIME: "image/jpeg", Width: 1920, Height: 1080, Size: 696527,
	}
}

// Kakao has no caption for media, so Matrix's body must come from the alt text (attachment cmt) or the
// file name - never from the message field, which holds a client-localised placeholder.
func TestDescriptionUsesAltTextNotThePlaceholder(t *testing.T) {
	meta := photoMeta()
	name := meta.FileName()
	withAlt := photoMeta()
	withAlt.Cmt = "Picture of me skiing with alt text"
	if got := descriptionFor(loco.ChatLog{Type: loco.MsgPhoto, Text: "photo"}, withAlt, name, ""); got != withAlt.Cmt {
		t.Errorf("alt text must win: got %q", got)
	}
	if got := descriptionFor(loco.ChatLog{Type: loco.MsgPhoto, Text: "사진"}, meta, name, ""); got != name {
		t.Errorf("no alt text: want the file name %q, got %q", name, got)
	}
	if got := descriptionFor(loco.ChatLog{Type: loco.MsgVoice}, &loco.AttachmentMeta{}, "", ""); got != "voice note" {
		t.Errorf("with nothing to name it, say what kind it is, got %q", got)
	}
}

// The placeholder in the message field is localised to the sending client (사진 from the Windows client,
// photo from the Android one), so known media types ignore it. Unknown types only become media when the
// message has no text, which keeps link shares as text.
func TestCarryAsMediaIgnoresClientPlaceholders(t *testing.T) {
	for _, typ := range []int32{loco.MsgPhoto, loco.MsgVideo, loco.MsgVoice, loco.MsgFile, loco.MsgEmote} {
		if !carryAsMedia(typ, "photo") {
			t.Errorf("type %d with a placeholder body must still bridge as media", typ)
		}
	}
	if carryAsMedia(loco.MsgLink, "https://example.com") {
		t.Error("a link share should stay text")
	}
	if carryAsMedia(999, "some text") {
		t.Error("an unknown type with text should stay text")
	}
	if !carryAsMedia(999, "") {
		t.Error("an unknown type with no text and an attachment is probably media")
	}
}

func TestMsgTypeMapping(t *testing.T) {
	cases := []struct {
		kakao int32
		mime  string
		want  event.MessageType
	}{
		{loco.MsgPhoto, "image/jpeg", event.MsgImage},
		{loco.MsgPhoto, "", event.MsgImage},
		{loco.MsgVideo, "video/mp4", event.MsgVideo},
		{loco.MsgVoice, "", event.MsgAudio}, // voice notes carry no mime at all
		{loco.MsgFile, "audio/mpeg", event.MsgAudio},
		{loco.MsgFile, "application/pdf", event.MsgFile},
		{loco.MsgFile, "", event.MsgFile},
		{999, "image/webp", event.MsgImage},
	}
	for _, c := range cases {
		if got := msgTypeFor(c.kakao, c.mime); got != c.want {
			t.Errorf("kakao type %d mime %q: want %s, got %s", c.kakao, c.mime, c.want, got)
		}
	}
	if got := guessMime("talka_aac.m4a"); !strings.HasPrefix(got, "audio/") {
		t.Errorf("guessMime(talka_aac.m4a) = %q", got)
	}
	if got := guessMime("talkv_hevc.mp4"); got != "video/mp4" {
		t.Errorf("guessMime(talkv_hevc.mp4) = %q", got)
	}
}

// When media cannot be carried, the fallback must still say what was there - name and size included.
func TestMediaFallbackSaysWhatWasLost(t *testing.T) {
	meta := &loco.AttachmentMeta{Name: "conflict.mpg", Size: 34590724}
	part := mediaFallbackPart(loco.ChatLog{Type: loco.MsgFile, Text: "conflict.mpg"}, meta, meta.Name)
	if part.Content.MsgType != event.MsgNotice {
		t.Errorf("fallback must not be presented as a normal message: %s", part.Content.MsgType)
	}
	if !strings.Contains(part.Content.Body, "conflict.mpg") || !strings.Contains(part.Content.Body, "33.0 MiB") {
		t.Errorf("fallback should name the file and its size: %q", part.Content.Body)
	}
}

// Kakao's emoticon packs are stickers. Static and animated items are both .png/.webp objects with no flag
// in the message, so the message type is the only thing that says whether it moves.
func TestEmotesBecomeStickers(t *testing.T) {
	// Not m.sticker: measured on our own event, an m.sticker msgtype left the uploaded image in the room
	// with correct info, and the client showed only the body text. m.image is what actually displays.
	for _, typ := range []int32{loco.MsgEmote, loco.MsgEmoteAnimated, loco.MsgEmoteAnimated2} {
		if got := msgTypeFor(typ, "image/webp"); got != event.MsgImage {
			t.Errorf("type %d: want an image that clients render, got %s", typ, got)
		}
		if !carryAsMedia(typ, "") {
			t.Errorf("type %d should bridge as media", typ)
		}
	}
	if msgTypeFor(loco.MsgMultiPhoto, "image/jpeg") != event.MsgImage {
		t.Error("type 27 is a multi-photo post, not a sticker")
	}
}

// There is no accepted Matrix proposal for interactive messages, so a channel message keeps its text and
// says that the rest is in KakaoTalk - what KakaoTalk's own desktop client does.
func TestChannelMessagesCarryTextPlusAFooter(t *testing.T) {
	withText := channelContentOf(loco.ChatLog{Type: loco.MsgPlus, Text: "쿠팡입니다."})
	if withText.MsgType != event.MsgText {
		t.Errorf("a channel message with text stays text: %s", withText.MsgType)
	}
	if !strings.HasPrefix(withText.Body, "쿠팡입니다.") {
		t.Errorf("the text must come first: %q", withText.Body)
	}
	if !strings.Contains(withText.Body, channelFooter) {
		t.Errorf("the footer must be present: %q", withText.Body)
	}
	empty := channelContentOf(loco.ChatLog{Type: loco.MsgPlusEvent, Text: "  "})
	if empty.MsgType != event.MsgNotice || empty.Body != channelFooter {
		t.Errorf("a card with no text should be a notice saying only what was left out: %+v", empty)
	}
	if got := convertedContentOf(loco.ChatLog{Type: loco.MsgAlimtalk, Text: "x"}); !strings.Contains(got.Body, channelFooter) {
		t.Error("the text-only path must add the footer too, so both paths agree")
	}
	if carryAsMedia(loco.MsgPlus, "") {
		t.Error("channel cards are not carried as media")
	}
}

// The pack item key has to survive somewhere, or the bridge can never do anything sticker-shaped later.
func TestStickerItemKeyIsKept(t *testing.T) {
	item := &loco.AttachmentMeta{Path: "4412206.emot_037.webp"}
	got := itemExtra(item)
	if got == nil || got["xyz.kakao.item"] != "4412206.emot_037.webp" {
		t.Errorf("item key lost: %+v", got)
	}
	if itemExtra(&loco.AttachmentMeta{URL: "https://talk.kakaocdn.net/dna/x/y.jpg"}) != nil {
		t.Error("a photo has no item key; info must not gain a bogus field")
	}
}

// Kakao's animated sticker assets are served as image/webp and are not WebP as delivered: these are the
// first 24 bytes of https://item.kakaocdn.net/dw/4412207.emot_034.webp (258358 bytes, no RIFF/WEBP).
// Uploading them is what made Sable say "Media unavailable" and Element log "Unable to load image", so the
// bridge checks the container before it uploads. What those bytes really are, and how the bridge undoes it,
// is in loco/digitalitem.go.
func TestKakaoAnimatedStickerBytesAreNotAnImage(t *testing.T) {
	head, err := hex.DecodeString("b5e4041d3800821931077e1431dd7ce044581ce9d1da1c50")
	if err != nil {
		t.Fatal(err)
	}
	if isDecodableImage(head, "image/webp") {
		t.Error("the animated sticker asset should not be accepted as an image")
	}
	real := map[string][]byte{
		"png":  {0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0},
		"jpeg": {0xFF, 0xD8, 0xFF, 0xE0},
		"gif":  []byte("GIF89a.."),
		"webp": append(append([]byte("RIFF"), 0, 0, 0, 0), []byte("WEBPVP8 ")...),
	}
	for kind, b := range real {
		if !isDecodableImage(b, "image/"+kind) {
			t.Errorf("a real %s should be accepted", kind)
		}
	}
	// Non-images are not checked: their containers were verified to match the protocol.
	if !isDecodableImage([]byte("whatever"), "audio/mp4") {
		t.Error("audio and video should pass through unexamined")
	}
}

// A still preview has to say so: the file on Matrix is not the asset the sender sent.
func TestStillPreviewIsLabelled(t *testing.T) {
	cl := loco.ChatLog{Type: loco.MsgEmoteAnimated, Text: "voice"}
	meta := &loco.AttachmentMeta{Path: "4412207.emot_034.webp"}
	if got := descriptionFor(cl, meta, "4412207.thum_034.png", "4412207.emot_034.webp"); got != "4412207.emot_034.webp (still preview)" {
		t.Errorf("a still preview must name the sticker it stands for: %q", got)
	}
	extra := stillPreviewExtra(itemExtra(meta), "4412207.emot_034.webp")
	if extra["xyz.kakao.item"] != "4412207.emot_034.webp" || extra["xyz.kakao.still_preview_of"] != "4412207.emot_034.webp" {
		t.Errorf("info should keep the item key and mark the preview: %v", extra)
	}
}

// An emoticon pack item is a Matrix sticker, not an m.image: m.sticker is the event type clients use for
// the larger bubble-less layout and to loop animated ones, and mautrix passes ConvertedMessagePart.Type
// straight to the homeserver. A photo the sender uploaded stays an ordinary image message.
func TestEmoteItemsBridgeAsStickerEvents(t *testing.T) {
	for _, typ := range []int32{loco.MsgEmote, loco.MsgEmoteAnimated, loco.MsgEmoteAnimated2, loco.MsgAnimatedEmot} {
		if !loco.IsStickerMessageType(typ) {
			t.Fatalf("type %d is an emoticon pack item and should bridge as a sticker", typ)
		}
		part := &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: &event.MessageEventContent{
			MsgType:  event.MsgImage,
			Body:     "Cry",
			FileName: "4412207.emot_034.webp",
			URL:      "mxc://libhwang.cc/x",
			Info:     &event.FileInfo{MimeType: "image/webp", Width: 360, Height: 360, IsAnimated: loco.IsAnimatedMessageType(typ)},
		}}
		makeSticker(part)
		if part.Type != event.EventSticker {
			t.Errorf("type %d: want an m.sticker event, got %s", typ, part.Type)
		}
		if part.Content.MsgType != "" {
			t.Errorf("type %d: m.sticker has no msgtype, got %q", typ, part.Content.MsgType)
		}
		if part.Content.FileName != "" {
			t.Errorf("type %d: filename is an m.image field, got %q", typ, part.Content.FileName)
		}
		if part.Content.Info.Width != 360 || part.Content.Info.Height != 360 {
			t.Error("a sticker must keep its dimensions; clients lay them out from info.w/h")
		}
		if want := loco.IsAnimatedMessageType(typ); part.Content.Info.IsAnimated != want {
			t.Errorf("type %d: is_animated should be %v", typ, want)
		}
	}
	for _, typ := range []int32{loco.MsgPhoto, loco.MsgFile, loco.MsgVoice} {
		if loco.IsStickerMessageType(typ) {
			t.Errorf("type %d is something the sender uploaded, not a sticker", typ)
		}
	}
}

// Sticker items carry no width/height in the protocol, so the bridge reads them out of the asset: the
// fixture is a real Kakao item head (encrypted), whose VP8X canvas says 360x360.
func TestStickerDimensionsComeFromTheAsset(t *testing.T) {
	raw, err := os.ReadFile("../loco/testdata/kakao_animated_emot_head.hex")
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("the captured sticker asset is traffic from a personal account and is not published")
	}
	if err != nil {
		t.Fatal(err)
	}
	enc, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	asset, ok := loco.DecryptItemAsset(enc)
	if !ok {
		t.Fatal("the fixture should decrypt to an image")
	}
	part := &bridgev2.ConvertedMessagePart{Content: &event.MessageEventContent{Info: &event.FileInfo{}}}
	fillDimensionsFromBytes(part, asset)
	if part.Content.Info.Width != 360 || part.Content.Info.Height != 360 {
		t.Errorf("want 360x360 from the VP8X header, got %dx%d", part.Content.Info.Width, part.Content.Info.Height)
	}
	part = &bridgev2.ConvertedMessagePart{Content: &event.MessageEventContent{Info: &event.FileInfo{Width: 100, Height: 200}}}
	fillDimensionsFromBytes(part, asset)
	if part.Content.Info.Width != 100 {
		t.Error("a size the protocol did give must win")
	}
	png := []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\x0dIHDR\x00\x00\x00\xd2\x00\x00\x00\xd2\x08\x06")
	if w, h := imageDimensions(png); w != 210 || h != 210 {
		t.Errorf("PNG IHDR: got %dx%d, want 210x210", w, h)
	}
	gif := []byte("GIF89a\xd2\x00\xd2\x00")
	if w, h := imageDimensions(gif); w != 210 || h != 210 {
		t.Errorf("GIF logical screen: got %dx%d, want 210x210", w, h)
	}
}
