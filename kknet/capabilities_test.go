// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"context"
	"testing"

	"maunium.net/go/mautrix/event"
)

// bridgev2 refuses an m.image before the connector ever sees it if GetCapabilities does not name it, so
// an empty feature set looks like a working bridge that silently drops photos. This test is the guard
// against that: what is claimed here must be what the send path implements, and nothing more.
func TestCapabilitiesMatchWhatCanBeSent(t *testing.T) {
	c := &Client{}
	feats := c.GetCapabilities(context.Background(), nil)

	for _, mt := range []event.MessageType{event.MsgImage, event.CapMsgSticker} {
		feat := feats.File[mt]
		if feat == nil {
			t.Errorf("%s must be claimed, or bridgev2 rejects it before the network is asked", mt)
			continue
		}
		for _, mime := range []string{"image/jpeg", "image/png", "image/gif", "image/webp"} {
			if feat.GetMimeSupport(mime).Reject() {
				t.Errorf("%s %s must be accepted; the upload path sends it", mt, mime)
			}
		}
		// Anything not listed is rejected by bridgev2 (GetMimeSupport falls back to CapLevelRejected),
		// which is what gives the user a named reason instead of a send that dies in the connector.
		if lvl := feat.GetMimeSupport("image/heif"); !lvl.Reject() {
			t.Errorf("%s image/heif is claimed at level %d; the shipped clients do not accept it", mt, lvl)
		}
		if feat.MaxSize != 0 {
			t.Errorf("%s claims a size limit of %d; Kakao's real ceiling has not been measured", mt, feat.MaxSize)
		}
	}
	if f := feats.File[event.MsgImage]; f == nil || f.Caption != event.CapLevelPartialSupport {
		t.Error("a photo caption is bridged as a separate Kakao message, so it must be claimed as partially " +
			"supported: Kakao's composer has no caption and the server ignores extra.cmt")
	}
	if f := feats.File[event.CapMsgSticker]; f != nil && f.Caption != event.CapLevelDropped {
		t.Errorf("a sticker carries no caption, got level %d", f.Caption)
	}
	for _, mt := range []event.MessageType{event.MsgVideo, event.MsgFile} {
		feat := feats.File[mt]
		if feat == nil {
			t.Errorf("%s must be claimed, or bridgev2 rejects it before the network is asked", mt)
			continue
		}
		if feat.Caption != event.CapLevelPartialSupport {
			t.Errorf("%s captions are sent as a separate Kakao message, so they must be claimed as partial support", mt)
		}
	}
	// mp4 is the container verified end to end; the others get a ticket but Kakao re-encodes the file,
	// so they are only ever claimed as partial.
	if lvl := feats.File[event.MsgVideo].GetMimeSupport("video/mp4"); !lvl.Full() {
		t.Errorf("video/mp4 must be claimed as fully supported, got level %d", lvl)
	}
	for _, mime := range []string{"video/webm", "video/x-matroska", "video/quicktime"} {
		if lvl := feats.File[event.MsgVideo].GetMimeSupport(mime); !lvl.Partial() || lvl.Full() {
			t.Errorf("%s must be claimed as partial (Kakao transcodes it), got level %d", mime, lvl)
		}
	}
	// A plain file is claimed for anything, at partial support: the gateway keeps its own allow-list of
	// extensions (EXTENSION_NOT_ALLOWED for e.g. .apk) and the bridge passes that refusal through.
	if lvl := feats.File[event.MsgFile].GetMimeSupport("application/pdf"); !lvl.Partial() {
		t.Errorf("application/pdf as a file must be claimed as partial support, got level %d", lvl)
	}
	for _, mt := range []event.MessageType{event.MsgAudio} {
		if feats.File[mt] != nil {
			t.Errorf("%s must not be claimed until Kakao's voice-note format is measured", mt)
		}
	}
}
