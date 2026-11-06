// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

// Media from Kakao to Matrix.
//
// What the server gives us for a photo or a file is a signed HTTPS URL (measured in
// loco/testdata/mchatlogs_memo.hex, decoded by loco.AttachmentMetaOf), so receiving media is: fetch
// the bytes, upload them to the homeserver, send an m.image/m.video/m.file part. The signature on
// Kakao's URL expires within days, which is why the fetch happens while the event is being bridged
// and not later.
//
// With direct media enabled in the config, signed media instead keeps an address at Kakao and never
// enters the homeserver's media store; the address is re-read when a client asks for it, so the expiry
// stops being a problem (see directmedia.go). Item assets still take the path above.
//
// Media *from* Matrix is a different job: Kakao wants the bytes uploaded to its DNA storage first, then
// a WRITEMEDIA commit (docs/protocol-notes.md §9-§10, implemented in media_out.go).

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/libhwang50/kakaobridge/loco"
)

// maxMediaBytes bounds what the bridge will pull from Kakao's CDN in one go. The largest thing
// observed in this account's history was a 34 MB video file; anything far above that is more likely a
// mistake or a stream than a message.
const maxMediaBytes = 128 << 20

// convertChatLog turns a Kakao message into Matrix content. It is called by the framework at the
// point where a Matrix room and an intent exist, which is what uploading media needs.
func (c *Client) convertChatLog(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, cl loco.ChatLog) (*bridgev2.ConvertedMessage, error) {
	if loco.IsChannelMessageType(cl.Type) {
		// Buttons and other interactive parts have no Matrix representation yet, and KakaoTalk's own
		// desktop client puts a "Please check on mobile." footer on these; do the same thing.
		return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{{
			Type: event.EventMessage, Content: channelContentOf(cl),
		}}}, nil
	}
	meta := cl.AttachmentMetaOf()
	if meta == nil || !carryAsMedia(cl.Type, cl.Text) {
		if cl.Type == loco.MsgReply {
			// A Kakao reply is an ordinary message plus a pointer at the one it quotes, and the quoted text
			// is not in the message at all (see loco.ReplyMetaOf). Matrix has the same shape, so the bridge
			// points at the quoted message by its log id - the same id it already uses for its own.
			cm := &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{textPartOf(cl)}}
			if reply, ok := loco.ReplyMetaOf(cl); ok {
				cm.ReplyTo = &networkid.MessageOptionalPartID{
					MessageID: networkid.MessageID(strconv.FormatInt(reply.LogID, 10)),
				}
			}
			return cm, nil
		}
		return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{textPartOf(cl)}}, nil
	}
	if url := meta.DownloadURL(); url != "" {
		if part, ok := c.directMediaPart(ctx, cl, meta); ok {
			return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{part}}, nil
		}
		name := meta.FileName()
		data, mime, err := c.downloadMedia(ctx, meta)
		previewOf := ""
		if err == nil && !isDecodableImage(data, mime) {
			if dec, ok := loco.DecryptItemAsset(data); ok {
				// A Kakao "digital item": a real animated WebP whose first 128 bytes are encrypted with a
				// key compiled into the app. Decrypting them is what makes the sticker animate (see
				// loco/digitalitem.go); uploading the bytes as they came was what showed "Media unavailable".
				c.login.Log.Debug().Int64("log_id", cl.LogID).Str("item", name).
					Msg("Kakao's sticker asset had an encrypted header; decrypted it")
				data = dec
			}
		}
		if err == nil && !isDecodableImage(data, mime) {
			// Still nothing a client can open: take the pack's static preview rather than bytes no
			// decoder here recognises (see AttachmentMeta.StillPreviewItemURL).
			if alt := meta.StillPreviewItemURL(); alt != "" {
				if altData, altMime, altErr := c.fetchBytes(ctx, alt); altErr == nil && isDecodableImage(altData, altMime) {
					c.login.Log.Debug().Int64("log_id", cl.LogID).Str("item", name).
						Msg("Kakao's sticker asset is not an image a client can open; bridged the pack's still preview instead")
					data, mime = altData, altMime
					previewOf, name = name, path.Base(strings.SplitN(alt, "?", 2)[0])
				}
			}
			if previewOf == "" {
				err = fmt.Errorf("%s is not an image format a Matrix client can open", name)
			}
		}
		if err == nil {
			// The msgtype is decided from the resolved mime: voice notes and videos carry no `mt`
			// field at all, their kind comes from the generated file name.
			msgType := msgTypeFor(cl.Type, mime)
			mxc, file, err := intent.UploadMedia(ctx, portal.MXID, data, name, mime)
			if err == nil {
				part := &bridgev2.ConvertedMessagePart{
					Type: event.EventMessage,
					Content: &event.MessageEventContent{
						MsgType:  msgType,
						Body:     descriptionFor(cl, meta, name, previewOf),
						FileName: name,
						URL:      mxc,
						File:     file,
						Info: &event.FileInfo{
							MimeType: mime,
							Width:    meta.Width,
							Height:   meta.Height,
							Size:     int(meta.Size),
							Duration: meta.DurationMS(cl.Type),
							// Only say it moves if what we sent actually does.
							IsAnimated: loco.IsAnimatedMessageType(cl.Type) && previewOf == "",
							// Which item of which pack this is (4412206.emot_037.webp). Kakao gives no
							// other identifier for a sticker; keeping it makes the bridge able to talk
							// about sticker packs later without guessing from the file name.
							Extra: stillPreviewExtra(itemExtra(meta), previewOf),
						},
					},
				}
				fillDimensionsFromBytes(part, data)
				if loco.IsStickerMessageType(cl.Type) {
					makeSticker(part)
				}
				return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{part}}, nil
			}
			err = fmt.Errorf("uploading to the homeserver: %w", err)
		}
		c.login.Log.Warn().Err(err).Int64("log_id", cl.LogID).Str("media", name).
			Msg("Could not bridge a Kakao attachment as media")
		return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{
			mediaFallbackPart(cl, meta, name),
		}}, nil
	}
	return &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{
		mediaFallbackPart(cl, meta, meta.FileName()),
	}}, nil
}

// directMediaPart addresses an attachment at Kakao instead of copying it to the homeserver. Only signed
// DNA media qualifies: an item asset has to be looked at before anything can be shown (see
// directmedia.go), so it keeps the upload path.
//
// What is not known here is what the bytes are. The mime type comes from Kakao's own `mt` field when it
// has one and from the file name otherwise, which is the same rule the upload path falls back to; width,
// height, size and duration are what Kakao stated about the attachment rather than what a decoder here
// measured. A wrong guess is visible but not fatal: the media proxy re-reads the message on every
// request, so a media that is there will be served.
func (c *Client) directMediaPart(ctx context.Context, cl loco.ChatLog, meta *loco.AttachmentMeta) (*bridgev2.ConvertedMessagePart, bool) {
	if !c.conn.useDirectMedia() || !directMediaEligible(meta) {
		return nil, false
	}
	mxc, err := c.login.Bridge.Matrix.GenerateContentURI(ctx, messageMediaID(cl.ChatID, cl.LogID))
	if err != nil {
		if !errors.Is(err, bridgev2.ErrDirectMediaNotEnabled) && c.firstDirectMediaFailure() {
			c.login.Log.Warn().Err(err).Msg("Kakao media cannot be addressed directly; copying media to the homeserver instead")
		}
		return nil, false
	}
	name := meta.FileName()
	mime := meta.MIME
	if mime == "" {
		mime = guessMime(name)
	}
	part := &bridgev2.ConvertedMessagePart{
		Type: event.EventMessage,
		Content: &event.MessageEventContent{
			MsgType:  msgTypeFor(cl.Type, mime),
			Body:     descriptionFor(cl, meta, name, ""),
			FileName: name,
			URL:      mxc,
			Info: &event.FileInfo{
				MimeType:   mime,
				Width:      meta.Width,
				Height:     meta.Height,
				Size:       int(meta.Size),
				Duration:   meta.DurationMS(cl.Type),
				IsAnimated: loco.IsAnimatedMessageType(cl.Type),
				Extra:      stillPreviewExtra(itemExtra(meta), ""),
			},
		},
	}
	if loco.IsStickerMessageType(cl.Type) {
		makeSticker(part)
	}
	return part, true
}

// carryAsMedia decides whether a message with an attachment becomes Matrix media or stays text.
//
// Kakao has no caption for media (the app offers an alt-text editor and nothing else), and what it puts
// in the message body for a media message is a placeholder localised to the sending client: the same
// photo sent from the Windows client arrives with message="사진" and from the Android client with
// message="photo", a voice note with message="voice note", a video with message="동영상". So for the
// types that are known to be media the body is never useful, and it is dropped. For an unrecognised
// type we only take the media path when there is nothing to say as text, which is how link shares and
// other text-plus-attachment messages keep their text.
func carryAsMedia(kakaoType int32, text string) bool {
	if loco.IsChannelMessageType(kakaoType) {
		// A channel card may carry an image, but its interactive parts are the point; it bridges as text
		// plus a note instead (see channelContentOf).
		return false
	}
	if loco.IsMediaMessageType(kakaoType) {
		return true
	}
	return strings.TrimSpace(text) == ""
}

// itemExtra carries the Kakao item key in the media info of a sticker, when there is one.
func itemExtra(meta *loco.AttachmentMeta) map[string]any {
	key := meta.Path
	if key == "" {
		key = meta.EmoticonItemPath
	}
	if key == "" {
		return nil
	}
	return map[string]any{"xyz.kakao.item": key}
}

// channelFooter is appended to a channel (plus-chat) message. Kakao's channels send interactive cards -
// buttons, image carousels, links - and there is no accepted Matrix proposal for interactive messages, so
// the bridge carries the text and says what it left behind. KakaoTalk's desktop client does the same with
// a "Please check on mobile." footer.
const channelFooter = "(this is a channel message, check on mobile client for interactions.)"

// channelContentOf renders a channel message: its text plus the note, or just the note when the card had
// no text to begin with.
func channelContentOf(cl loco.ChatLog) *event.MessageEventContent {
	body := strings.TrimSpace(cl.Text)
	if body == "" {
		return &event.MessageEventContent{MsgType: event.MsgNotice, Body: channelFooter}
	}
	return &event.MessageEventContent{MsgType: event.MsgText, Body: body + "\n\n" + channelFooter}
}

// descriptionFor fills Matrix's `body`, which for m.image/m.video is the alt text and for other media
// is a description of the file. Kakao's alt text is the `cmt` field (measured: a photo carrying
// "Picture of me skiing with alt text"); with no alt text the file name is the honest fallback. For a
// still preview of an animated sticker, name the sticker and say it is a preview: the file on Matrix has
// a different name than the thing the sender sent, and an unlabelled still would read as a wrong bridge.
func descriptionFor(cl loco.ChatLog, meta *loco.AttachmentMeta, name, previewOf string) string {
	if previewOf != "" {
		return fmt.Sprintf("%s (still preview)", previewOf)
	}
	if meta.Cmt != "" {
		return meta.Cmt
	}
	if name != "" {
		return name
	}
	return mediaNameOf(cl.Type)
}

// makeSticker turns an image part into a Matrix sticker: the m.sticker event type, whose content is an
// m.image content without the msgtype field (mautrix documents MessageEventContent as covering both, and
// portal.go passes ConvertedMessagePart.Type straight to the homeserver). Clients reserve m.sticker for the
// larger, bubble-less layout that matches how Kakao shows an emoticon pack item, and they loop animated
// ones from info.is_animated.
func makeSticker(part *bridgev2.ConvertedMessagePart) {
	part.Type = event.EventSticker
	part.Content.MsgType = ""
	// "filename" is an m.image field; a sticker is named by its body, which is what clients show as alt text.
	part.Content.FileName = ""
}

// fillDimensionsFromBytes completes width/height from the bytes when the protocol did not say them. Sticker
// items carry no w/h at all, and clients lay stickers out from info.w/h, so without this a sticker arrives
// unmeasurable.
func fillDimensionsFromBytes(part *bridgev2.ConvertedMessagePart, data []byte) {
	info := part.Content.Info
	if info == nil || (info.Width > 0 && info.Height > 0) {
		return
	}
	if w, h := imageDimensions(data); w > 0 && h > 0 {
		info.Width, info.Height = w, h
	}
}

// imageDimensions reads the canvas size out of an image container.
func imageDimensions(data []byte) (int, int) {
	switch {
	case len(data) >= 24 && string(data[0:8]) == "\x89PNG\r\n\x1a\n" && string(data[12:16]) == "IHDR":
		return int(binary.BigEndian.Uint32(data[16:20])), int(binary.BigEndian.Uint32(data[20:24]))
	case len(data) >= 10 && (string(data[0:6]) == "GIF87a" || string(data[0:6]) == "GIF89a"):
		return int(binary.LittleEndian.Uint16(data[6:8])), int(binary.LittleEndian.Uint16(data[8:10]))
	case len(data) >= 12 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return webpDimensions(data)
	case len(data) >= 4 && data[0] == 0xFF && data[1] == 0xD8:
		return jpegDimensions(data)
	}
	return 0, 0
}

// webpDimensions walks the RIFF chunks. An animated item is VP8X (canvas size is 24-bit, one less than the
// real value), and static ones are VP8 (a 14-bit pair after the 9d 01 2a sync) or VP8L (a 14-bit pair
// packed into a 32-bit word).
func webpDimensions(data []byte) (int, int) {
	for pos := 12; pos+8 <= len(data); {
		tag, size := string(data[pos:pos+4]), int(binary.LittleEndian.Uint32(data[pos+4:pos+8]))
		body := pos + 8
		if body+size > len(data) {
			break
		}
		switch tag {
		case "VP8X":
			if size >= 10 {
				return le24(data[body+4:body+7]) + 1, le24(data[body+7:body+10]) + 1
			}
		case "VP8 ":
			if i := bytes.Index(data[body:body+size], []byte{0x9d, 0x01, 0x2a}); i >= 0 && i+7 <= size {
				p := body + i + 3
				return int(binary.LittleEndian.Uint16(data[p:p+2])) & 0x3FFF,
					int(binary.LittleEndian.Uint16(data[p+2:p+4])) & 0x3FFF
			}
		case "VP8L":
			if size >= 5 && data[body] == 0x2f {
				v := binary.LittleEndian.Uint32(data[body+1 : body+5])
				return int(v&0x3FFF) + 1, int((v>>14)&0x3FFF) + 1
			}
		}
		pos = body + size + (size & 1)
	}
	return 0, 0
}

func le24(b []byte) int { return int(b[0]) | int(b[1])<<8 | int(b[2])<<16 }

// jpegDimensions scans the markers for a start-of-frame segment.
func jpegDimensions(data []byte) (int, int) {
	for pos := 2; pos+9 < len(data); {
		if data[pos] != 0xFF {
			pos++
			continue
		}
		marker, seg := data[pos+1], int(binary.BigEndian.Uint16(data[pos+2:pos+4]))
		if marker >= 0xC0 && marker <= 0xCF && marker != 0xC4 && marker != 0xC8 && marker != 0xCC {
			return int(binary.BigEndian.Uint16(data[pos+7 : pos+9])), int(binary.BigEndian.Uint16(data[pos+5 : pos+7]))
		}
		pos += 2 + seg
	}
	return 0, 0
}

// isDecodableImage reports whether the bytes really are the image they claim to be. Kakao labels its
// animated sticker assets image/webp and serves bytes that are not WebP (no RIFF/WEBP or VP8X; see
// loco.AttachmentMeta.StillPreviewItemURL), so the bridge checks before uploading instead of trusting
// the label: an unopenable upload shows up in clients as "Media unavailable".
func isDecodableImage(data []byte, mime string) bool {
	if !strings.HasPrefix(mime, "image/") {
		// Video, audio and files are not checked: their bytes were verified to match what the protocol
		// describes (a real MP4/M4A/JPEG), and a container check would add no signal.
		return true
	}
	return loco.KnownImageHeader(data)
}

// stillPreviewExtra keeps the item key of the sticker, and adds which animated item a still preview stands
// for.
func stillPreviewExtra(extra map[string]any, previewOf string) map[string]any {
	if previewOf == "" {
		return extra
	}
	if extra == nil {
		extra = map[string]any{}
	}
	extra["xyz.kakao.still_preview_of"] = previewOf
	return extra
}

func textPartOf(cl loco.ChatLog) *bridgev2.ConvertedMessagePart {
	return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: convertedContentOf(cl)}
}

// mediaFallbackPart is used when there is media but we could not carry it across: say what it was and
// keep whatever caption/alt text Kakao sent, rather than showing nothing.
func mediaFallbackPart(cl loco.ChatLog, meta *loco.AttachmentMeta, name string) *bridgev2.ConvertedMessagePart {
	label := fmt.Sprintf("(%s message, not bridged", mediaNameOf(cl.Type))
	if name != "" {
		label = fmt.Sprintf("(%s: %s, not bridged", mediaNameOf(cl.Type), name)
	}
	if meta != nil && meta.Size > 0 {
		label = fmt.Sprintf("%s, %.1f MiB", label, float64(meta.Size)/(1<<20))
	}
	label += ")"
	content := &event.MessageEventContent{MsgType: event.MsgNotice, Body: cl.Text}
	if content.Body == "" {
		content.Body = label
	} else {
		content.Body = content.Body + "\n" + label
	}
	return &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: content}
}

// downloadMedia fetches the signed CDN URL. No Kakao auth headers are needed: the signature in the
// query string is the authorisation (measured; a plain GET returns the bytes).
func (c *Client) downloadMedia(ctx context.Context, meta *loco.AttachmentMeta) (data []byte, mime string, err error) {
	data, served, err := c.fetchBytes(ctx, meta.DownloadURL())
	if err != nil {
		return nil, "", err
	}
	// Prefer what Kakao declared in the message, then what the CDN says, then the file name: a voice
	// note or a video has no declared mime, and its name is the only hint. A declared `image/jpg` is
	// normalised to image/jpeg, the registered spelling: the homeserver serves media with
	// `X-Content-Type-Options: nosniff` (measured), so a type it does not know is not sniffed back.
	mime = meta.MIME
	if mime == "" || mime == "application/octet-stream" {
		if served != "" {
			mime = served
		}
	}
	if mime == "" || mime == "application/octet-stream" {
		mime = guessMime(meta.FileName())
	}
	if strings.EqualFold(mime, "image/jpg") {
		mime = "image/jpeg"
	}
	if mime == "" {
		mime = "application/octet-stream"
	}
	return data, mime, nil
}

// fetchBytes does one bounded GET and reports the body plus whatever content type the CDN claims.
func (c *Client) fetchBytes(ctx context.Context, url string) (data []byte, served string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	// Ask for the bytes as stored. Go's transport adds `Accept-Encoding: gzip` itself and undoes gzip,
	// but not br or zstd, so a CDN that prefers those would hand us compressed bytes we would upload as
	// if they were the image.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := mediaHTTP.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound {
		// The usual cause is an expired signature on old history, not a lost file.
		return nil, "", fmt.Errorf("kakaocdn returned %d (the signed URL has probably expired)", res.StatusCode)
	} else if res.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("kakaocdn returned %d", res.StatusCode)
	}
	if enc := res.Header.Get("content-encoding"); enc != "" && enc != "identity" {
		return nil, "", fmt.Errorf("kakaocdn sent it with Content-Encoding: %s, which the bridge cannot undo", enc)
	}
	data, err = io.ReadAll(io.LimitReader(res.Body, maxMediaBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading from kakaocdn: %w", err)
	}
	if len(data) > maxMediaBytes {
		return nil, "", fmt.Errorf("attachment is larger than the %d MiB the bridge will fetch", maxMediaBytes>>20)
	}
	return data, res.Header.Get("content-type"), nil
}

var mediaHTTP = &http.Client{Timeout: 2 * time.Minute}

func guessMime(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".mp4", ".mpg", ".mpeg":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".m4a":
		return "audio/mp4"
	case ".pdf":
		return "application/pdf"
	default:
		return ""
	}
}

// msgTypeFor maps a Kakao media type (plus its mime, when the Kakao type is ambiguous) to a Matrix
// msgtype. Kakao's own numbers are documented in loco/roominfo.go.
func msgTypeFor(kakaoType int32, mime string) event.MessageType {
	switch kakaoType {
	case loco.MsgEmote, loco.MsgEmoteAnimated, loco.MsgEmoteAnimated2, loco.MsgAnimatedEmot, loco.MsgSpritecon:
		// Kakao's emoticon packs are stickers, but they bridge as ordinary m.image with is_animated set.
		// Measured: emitting msgtype "m.sticker" (which is not a Matrix msgtype at all - the spec's
		// sticker is an m.sticker *event type*, and MSC4230 adds is_animated to m.image and to that event
		// type) put the image in the room correctly but the client fell back to showing the body text.
		// m.image renders everywhere; the pack item key stays in info, so nothing is lost.
		return event.MsgImage
	case loco.MsgPhoto, loco.MsgMultiPhoto:
		return event.MsgImage
	case loco.MsgVideo:
		return event.MsgVideo
	case loco.MsgVoice:
		// measured: a voice note is a generated talka_<codec>.m4a with no mime field
		return event.MsgAudio
	default:
		if strings.HasPrefix(mime, "image/") {
			return event.MsgImage
		} else if strings.HasPrefix(mime, "video/") {
			return event.MsgVideo
		} else if strings.HasPrefix(mime, "audio/") {
			return event.MsgAudio
		}
		return event.MsgFile
	}
}
