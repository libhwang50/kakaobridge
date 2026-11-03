// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

// Media from Matrix to KakaoTalk.
//
// The three steps are the ones the shipped clients use, measured in docs/protocol-notes.md §10:
// reserve a slot with the messaging gateway, put the bytes there, then commit the object with LOCO
// WRITEMEDIA. Nothing here invents an identifier: the resource path and the `hfac`/uploadKey come from
// the gateway, which is what the phone does and what the receiving clients then resolve to a signed CDN
// URL.
//
// What differs per kind is the commit body (docs/protocol-notes.md §9): a photo names itself with
// width and height, a video names nothing at all (Kakao reads size and duration off the file and serves
// a transcoded copy), and a file carries a SHA-1 checksum and the sender's file name.

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"path"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	"github.com/libhwang50/kakaobridge/kakao"
	"github.com/libhwang50/kakaobridge/loco"
)

// sendMatrixPhoto bridges one m.image (or a Matrix sticker, which is an m.image without a caption).
//
// A Matrix caption has no place to live in a Kakao photo: the composer has no caption field, and a
// non-empty extra.cmt in WRITEMEDIA was ignored by the server (docs/protocol-notes.md §9). So the
// text is sent as its own message right after the photo, which is how the two arrive in KakaoTalk from
// a Kakao user too. Losing the pairing is the cost; losing the text would be worse.
func (c *Client) sendMatrixPhoto(ctx context.Context, msg *bridgev2.MatrixMessage, chatID int64) (*bridgev2.MatrixMessageResponse, error) {
	data, mimeType, _, err := mediaOf(ctx, c, msg)
	if err != nil {
		return nil, err
	}
	ext := kakao.MediaExt(mimeType)
	if ext == "" {
		// A matrix client may leave the mime type out; the extension still says what Kakao needs.
		fileName := msg.Content.GetFileName()
		ext = strings.ToLower(strings.TrimPrefix(path.Ext(fileName), "."))
		if !bridgeableExt(ext) {
			return nil, fmt.Errorf("kknet: that image (%s) can't be sent to KakaoTalk; it accepts jpg, png, gif and webp",
				orUnknown(ext))
		}
		mimeType = "image/" + ext
	}

	ticket, err := c.uploadToKakao(ctx, chatID, "image", data, mimeType, "")
	if err != nil {
		return nil, err
	}

	width, height := 0, 0
	if info := msg.Content.Info; info != nil {
		width, height = info.Width, info.Height
	}
	if width <= 0 || height <= 0 {
		width, height = imageDimensions(data)
	}

	sess := c.session()
	if sess == nil {
		return nil, fmt.Errorf("kknet: not connected to KakaoTalk right now; try again in a moment")
	}
	f, err := sess.WriteMedia(chatID, loco.ClientMsgID(), ticket.Resource, ticket.ResourceType, ticket.UploadKey,
		int32(width), int32(height))
	if err != nil {
		return nil, fmt.Errorf("kknet: committing the photo in KakaoTalk: %w", err)
	}
	wr, err := loco.ParseWriteResponse(f.Body)
	if err != nil {
		return nil, err
	}
	c.login.Log.Info().Int64("chat_id", chatID).Int64("log_id", wr.LogID).
		Int("bytes", len(data)).Str("resource", ticket.Resource).
		Msg("Photo sent to Kakao")
	resp := c.mediaResponse(chatID, wr)
	c.mapCaptionEcho(resp, c.sendCaptionAfter(ctx, sess, chatID, msg))
	return resp, nil
}

// sendMatrixVideo bridges one m.video. The commit body carries nothing but the object reference: the
// shipped client sends no size, duration or poster, and the chat log that comes back has all three
// computed by Kakao (with the served file renamed to a transcoded `talkv_high.mp4`).
func (c *Client) sendMatrixVideo(ctx context.Context, msg *bridgev2.MatrixMessage, chatID int64) (*bridgev2.MatrixMessageResponse, error) {
	data, mimeType, fileName, err := mediaOf(ctx, c, msg)
	if err != nil {
		return nil, err
	}
	ticket, err := c.uploadToKakao(ctx, chatID, "video", data, mimeType, fileName)
	if err != nil {
		return nil, err
	}
	sess := c.session()
	if sess == nil {
		return nil, fmt.Errorf("kknet: not connected to KakaoTalk right now; try again in a moment")
	}
	wr, err := c.commitOnceReady(ctx, sess, ticket, func() (loco.Frame, error) {
		return sess.WriteVideo(chatID, loco.ClientMsgID(), ticket.Resource, ticket.ResourceType, ticket.UploadKey,
			loco.VideoCommitOptions{})
	})
	if err != nil {
		return nil, err
	}
	c.login.Log.Info().Int64("chat_id", chatID).Int64("log_id", wr.LogID).
		Int("bytes", len(data)).Str("resource", ticket.Resource).
		Msg("Video sent to Kakao")
	resp := c.mediaResponse(chatID, wr)
	c.mapCaptionEcho(resp, c.sendCaptionAfter(ctx, sess, chatID, msg))
	return resp, nil
}

// sendMatrixFile bridges one m.file (and audio, which has no voice-note equivalent here - see the
// capability comment). A file is the one kind whose name matters: Kakao shows it and the chat log repeats
// it in the message text, so the Matrix file name is carried across verbatim.
func (c *Client) sendMatrixFile(ctx context.Context, msg *bridgev2.MatrixMessage, chatID int64) (*bridgev2.MatrixMessageResponse, error) {
	data, mimeType, fileName, err := mediaOf(ctx, c, msg)
	if err != nil {
		return nil, err
	}
	if fileName == "" {
		return nil, fmt.Errorf("kknet: that file has no name; Kakao shows the name in the chat")
	}
	ticket, err := c.uploadToKakao(ctx, chatID, "file", data, mimeType, fileName)
	if err != nil {
		return nil, err
	}
	sum := sha1.Sum(data)
	sess := c.session()
	if sess == nil {
		return nil, fmt.Errorf("kknet: not connected to KakaoTalk right now; try again in a moment")
	}
	wr, err := c.commitOnceReady(ctx, sess, ticket, func() (loco.Frame, error) {
		return sess.WriteFile(chatID, loco.ClientMsgID(), ticket.Resource, ticket.ResourceType, ticket.UploadKey,
			hex.EncodeToString(sum[:]), fileName)
	})
	if err != nil {
		return nil, err
	}
	c.login.Log.Info().Int64("chat_id", chatID).Int64("log_id", wr.LogID).
		Int("bytes", len(data)).Str("name", fileName).
		Msg("File sent to Kakao")
	resp := c.mediaResponse(chatID, wr)
	c.mapCaptionEcho(resp, c.sendCaptionAfter(ctx, sess, chatID, msg))
	return resp, nil
}

// mediaOf reads the bytes of the Matrix attachment once, for every kind.
func mediaOf(ctx context.Context, c *Client, msg *bridgev2.MatrixMessage) (data []byte, mimeType, fileName string, err error) {
	content := msg.Content
	// Element puts the mxc URI in content.url for unencrypted rooms and in content.file for encrypted
	// ones; DownloadMedia handles both, it only needs the right URI to ask for.
	uri, file := content.URL, content.File
	if file != nil {
		uri = file.URL
	}
	data, err = c.conn.bridge.Bot.DownloadMedia(ctx, uri, file)
	if err != nil {
		return nil, "", "", fmt.Errorf("kknet: fetching the attachment from the homeserver: %w", err)
	}
	if len(data) == 0 {
		return nil, "", "", fmt.Errorf("kknet: the attachment is empty")
	}
	if content.Info != nil {
		mimeType = content.Info.MimeType
	}
	return data, mimeType, content.GetFileName(), nil
}

// uploadToKakao reserves a slot and puts the bytes in it. `mediaType` is the gateway's own word for the
// kind (image, video, file, audio), and it rejects the ones it does not know.
func (c *Client) uploadToKakao(ctx context.Context, chatID int64, mediaType string, data []byte, mimeType, fileName string) (*kakao.MediaTicket, error) {
	ext := kakao.UploadExtFor(mediaType, mimeType, fileName)
	if ext == "" {
		return nil, fmt.Errorf("kknet: cannot send this %s to KakaoTalk: no file name or mime type to name it with", mediaType)
	}
	rc := c.rest()
	ticket, err := rc.PrepareMedia(ctx, chatID, mediaType, int64(len(data)), ext)
	if err != nil {
		return nil, err
	}
	if _, err := rc.UploadMedia(ctx, ticket, data, mimeType); err != nil {
		return nil, err
	}
	return ticket, nil
}

// commitOnceReady sends a media commit, and if the gateway is still busy with the uploaded bytes it asks
// again with a short backoff. This is what the `needReady` flag on a ticket is for: a video's first commit
// is answered with -306 and the identical body seconds later is accepted (docs/protocol-notes.md §10).
// The attempts are bounded, so a file the server never finishes with is reported to the user instead of
// hanging the send.
func (c *Client) commitOnceReady(ctx context.Context, sess *loco.Session, ticket *kakao.MediaTicket, commit func() (loco.Frame, error)) (*loco.WriteResult, error) {
	waits := []time.Duration{0}
	if ticket.NeedReady {
		waits = []time.Duration{0, 3 * time.Second, 5 * time.Second, 10 * time.Second, 20 * time.Second}
	}
	for _, wait := range waits {
		if wait > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
		}
		f, err := commit()
		if err != nil {
			return nil, fmt.Errorf("kknet: committing the attachment in KakaoTalk: %w", err)
		}
		if st := loco.InnerStatus(f.Body); st != loco.StatusMediaNotReady {
			if wait > 0 {
				c.login.Log.Debug().Str("resource", ticket.Resource).Dur("waited", wait).
					Msg("Kakao finished processing the attachment; the commit was accepted")
			}
			wr, err := loco.ParseWriteResponse(f.Body)
			if err != nil {
				return nil, fmt.Errorf("kknet: Kakao refused the attachment (%d)", st)
			}
			return wr, nil
		}
	}
	// Only reachable for a kind whose ticket said the server would need time: the file is uploaded, so
	// saying so is more useful than a generic failure.
	return nil, fmt.Errorf("kknet: Kakao is still processing that attachment; send it again in a minute")
}

// mediaResponse is the one DB row bridgev2 keeps for the Matrix event: the remote id Kakao assigned.
func (c *Client) mediaResponse(chatID int64, wr *loco.WriteResult) *bridgev2.MatrixMessageResponse {
	c.rememberBridged(chatID, wr.LogID)
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID(strconv.FormatInt(wr.LogID, 10)),
			Timestamp: time.Unix(wr.SendTime, 0),
		},
		StreamOrder: wr.LogID,
	}
}

// mapCaptionEcho teaches bridgev2 that the caption message it is about to be told about is not new: the
// row is written after the attachment's own row was saved (PostSave), and points at a placeholder Matrix
// id so the unique index on mxid is respected. If it cannot be written the only consequence is that the
// caption shows up in Matrix twice, which is what the warning says.
func (c *Client) mapCaptionEcho(resp *bridgev2.MatrixMessageResponse, captionLogID int64) {
	if resp == nil || captionLogID == 0 {
		return
	}
	resp.PostSave = func(ctx context.Context, saved *database.Message) {
		host := ""
		if i := strings.LastIndex(string(saved.MXID), ":"); i >= 0 {
			host = string(saved.MXID)[i:]
		}
		dupe := *saved
		dupe.RowID = 0
		dupe.ID = networkid.MessageID(strconv.FormatInt(captionLogID, 10))
		dupe.MXID = id.EventID(fmt.Sprintf("$kakaobridge-caption-%d%s", captionLogID, host))
		if err := c.login.Bridge.DB.Message.Insert(ctx, &dupe); err != nil {
			c.login.Log.Warn().Err(err).Int64("log_id", captionLogID).
				Msg("Could not record the caption as bridged; Kakao's echo of it may show up again in Matrix")
		}
	}
}

// bridgeableExt is the set of image extensions the shipped clients accept for a photo message.
func bridgeableExt(ext string) bool {
	switch ext {
	case "jpg", "jpeg", "png", "gif", "webp":
		return true
	}
	return false
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown type"
	}
	return s
}

// mediaRefusal is what a user sees for a kind of media the bridge does not send yet. Explicit beats
// silently dropping an attachment, and naming what to do instead beats naming the problem.
func mediaRefusal(msgType event.MessageType) error {
	return fmt.Errorf("kknet: %s can't be sent to KakaoTalk yet: Kakao voice notes are a separate format "+
		"that has not been measured; send it as a file instead", msgType)
}

// sendCaptionAfter writes a Matrix caption as its own Kakao message, and records a second mapping so the
// echo of that message is not bridged back as a duplicate.
//
// Kakao's chat then holds two messages where Matrix has one, so the echo of the second one has to be
// recognised: Matrix already has the text, as the caption of the attachment event. bridgev2 dedups
// inbound messages by the remote id in its own database, and HandleMatrixMessage can only return one row,
// so the second mapping is written in PostSave - after bridgev2 saved the first, because the message table
// has a unique index on (bridge_id, mxid) and two rows cannot share one Matrix event (measured: the second
// insert wins and the attachment row fails). The extra row's MXID is a placeholder derived from the log id
// ($kakaobridge-caption-…), never sent to the homeserver: its only job is to make the echo a duplicate.
// The cost is that a Kakao-side delete of the caption would be aimed at an id that has no Matrix event, so
// nothing happens - and single-message deletes from Kakao are not bridged today.
//
// The caption id is deliberately not passed to rememberBridged: that list chooses which message a Kakao
// read marker belongs on, and this Kakao message is not a message of its own in Matrix. Leaving it out
// makes a read marker about the caption resolve to the attachment, which is the event holding that text.
func (c *Client) sendCaptionAfter(ctx context.Context, sess *loco.Session, chatID int64, msg *bridgev2.MatrixMessage) int64 {
	caption := strings.TrimSpace(msg.Content.GetCaption())
	if caption == "" {
		return 0
	}
	cf, err := sess.WriteMsg(chatID, caption)
	if err != nil {
		c.login.Log.Warn().Err(err).Int64("chat_id", chatID).Msg("Attachment reached Kakao but its caption did not")
		return 0
	}
	cw, err := loco.ParseWriteResponse(cf.Body)
	if err != nil {
		c.login.Log.Warn().Err(err).Int64("chat_id", chatID).Msg("Caption may have reached Kakao but was not acknowledged")
		return 0
	}
	c.login.Log.Info().Int64("chat_id", chatID).Int64("log_id", cw.LogID).Msg("Caption sent as its own Kakao message")
	return cw.LogID
}
