// Direct media access: instead of copying a Kakao attachment into the homeserver's media store, the
// bridge writes an mxc:// URI of its own into the event and serves that address later by pointing at
// wherever Kakao puts the bytes at the moment of the request. The framework does the HTTP part
// (maunium.net/go/mautrix/mediaproxy, wired up by bridgev2's Matrix connector when the config enables
// direct_media); what this file owns is the id and the resolver.
//
// https://docs.mau.fi/bridges/go/discord/direct-media.html describes the shape from the operator's side.
package kknet

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/mediaproxy"

	"github.com/libhwang50/kakaobridge/loco"
)

var _ bridgev2.DirectMediableNetwork = (*Connector)(nil)

// A direct media id names one attachment at Kakao, in as few characters as the meaning allows: it is
// written into the mxc:// URI of every event that shows the media, and the framework refuses a URI
// longer than 255 characters. One spelling:
//
//	m<chatId>.<logId>   the attachment of one Kakao message.
//
// The id names the message rather than the address, because the address is not durable: Kakao signs DNA
// media with an `expire` measured in days (docs/protocol-notes.md §8), and every history read returns a
// fresh signature. Re-reading the message is therefore both the way to keep old media working and the
// way not to invent a second identity for it.
//
// Item assets are not given direct URIs. A sticker's `4412206.emot_037.webp` has no signature and no
// expiry, but its first bytes are encrypted with a key compiled into the app and some items are not a
// format a client can open at all, so the bridge has to hold the bytes to decide what to send
// (docs/protocol-notes.md §11). Those keep going through the homeserver's media store.
func messageMediaID(chatID, logID int64) networkid.MediaID {
	return networkid.MediaID(fmt.Sprintf("m%d.%d", chatID, logID))
}

func parseMessageMediaID(id string) (chatID, logID int64, err error) {
	if !strings.HasPrefix(id, "m") {
		return 0, 0, fmt.Errorf("kknet: %q is not a Kakao media id", id)
	}
	chat, log, found := strings.Cut(strings.TrimPrefix(id, "m"), ".")
	if !found {
		return 0, 0, fmt.Errorf("kknet: Kakao media id %q has no message part", id)
	}
	if chatID, err = strconv.ParseInt(chat, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("kknet: Kakao media id %q names no chat: %w", id, err)
	}
	if logID, err = strconv.ParseInt(log, 10, 64); err != nil {
		return 0, 0, fmt.Errorf("kknet: Kakao media id %q names no message: %w", id, err)
	}
	return chatID, logID, nil
}

// directMediaEligible says whether an attachment can be addressed at Kakao rather than copied: it needs
// an address of its own. A signed thumbnail counts, because for older media the thumbnail is sometimes
// the only thing still signed while the full resource is not (docs/protocol-notes.md §8). An emote or
// sticker names an item instead, and its bytes have to be fetched and looked at before anything can be
// shown, so those stay with the homeserver's media store.
func directMediaEligible(meta *loco.AttachmentMeta) bool {
	return meta.URL != "" || meta.ThumbnailURL != ""
}

// SetUseDirectMedia is called by the framework before Start when direct_media is enabled; from that
// point on the Matrix connector's GenerateContentURI returns URIs this bridge can serve.
func (c *Connector) SetUseDirectMedia() { c.directMedia = true }

// useDirectMedia reports whether media should be addressed at Kakao instead of copied to Matrix.
func (c *Connector) useDirectMedia() bool { return c.directMedia }

// Download answers one of this bridge's media ids. Returning a URL rather than the bytes is the point:
// the media proxy turns it into a redirect with a cache lifetime that ends shortly before Kakao's own
// signature does, so a client that fetches twice in a day hits Kakao once.
func (c *Connector) Download(ctx context.Context, mediaID networkid.MediaID, params map[string]string) (mediaproxy.GetMediaResponse, error) {
	chatID, logID, err := parseMessageMediaID(string(mediaID))
	if err != nil {
		return nil, err
	}
	client, err := c.clientForChat(ctx, chatID)
	if err != nil {
		return nil, err
	}
	cl, err := client.chatLogByID(ctx, chatID, logID)
	if err != nil {
		return nil, err
	}
	meta := cl.AttachmentMetaOf()
	if meta == nil {
		return nil, fmt.Errorf("kknet: chat %d message %d no longer carries an attachment", chatID, logID)
	}
	// An item asset would answer with a path, and the bytes behind it are not what a client should get
	// (see above); such media is never given a direct URI in the first place.
	if !directMediaEligible(meta) {
		return nil, fmt.Errorf("kknet: chat %d message %d is an item asset, which this bridge serves through the homeserver", chatID, logID)
	}
	target := meta.DownloadURL()
	if target == "" {
		return nil, fmt.Errorf("kknet: chat %d message %d has no address left to fetch", chatID, logID)
	}
	return &mediaproxy.GetMediaResponseURL{URL: target, ExpiresAt: mediaExpiry(meta, target)}, nil
}

// mediaExpiry says how long a client may keep the address it was just given. The proxy turns this into
// a cache lifetime five minutes shorter than the answer, so it must not be longer than the redirect it
// goes with. Kakao states two windows on the same attachment: the signature inside the URL (three days
// in every sample so far, docs/protocol-notes.md §8) and the attachment's own `expire` (two weeks from
// the same read). The earlier of the two is the only honest answer.
func mediaExpiry(meta *loco.AttachmentMeta, target string) time.Time {
	earliest := time.Time{}
	if until, ok := loco.SignedUntil(target); ok {
		earliest = until
	}
	if meta.Expire != 0 {
		if served := time.UnixMilli(meta.Expire); earliest.IsZero() || served.Before(earliest) {
			earliest = served
		}
	}
	return earliest
}

// clientForChat picks a connected Kakao account to ask about this chat. A group room has no single
// owning account in Matrix, so the accounts recorded in that portal are tried first and any other live
// account after them. Kakao only answers a history read for an account that is in the chat, so a wrong
// pick surfaces as "Kakao did not return chat N message M" rather than as media from the wrong room.
func (c *Connector) clientForChat(ctx context.Context, chatID int64) (*Client, error) {
	var tried []*Client
	seen := map[*Client]bool{}
	add := func(login *bridgev2.UserLogin) {
		if login == nil {
			return
		}
		client, ok := login.Client.(*Client)
		if !ok || seen[client] || client.session() == nil {
			return
		}
		seen[client] = true
		tried = append(tried, client)
	}
	key := networkid.PortalKey{ID: networkid.PortalID(strconv.FormatInt(chatID, 10))}
	if logins, err := c.bridge.GetUserLoginsInPortal(ctx, key); err == nil {
		for _, login := range logins {
			add(login)
		}
	}
	for _, login := range c.bridge.GetAllCachedUserLogins() {
		add(login)
	}
	if len(tried) == 0 {
		return nil, fmt.Errorf("kknet: no connected Kakao account can be asked about chat %d", chatID)
	}
	return tried[0], nil
}

// firstDirectMediaFailure says whether this is the first time the bridge failed to address Kakao media
// directly, so the reason is said once per session instead of once per media message.
func (c *Client) firstDirectMediaFailure() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.directMediaWarned {
		return false
	}
	c.directMediaWarned = true
	return true
}

// chatLogByID reads one Kakao message, once more if the first attempt found a session that had already
// gone away. Kakao's history call is "everything after this log id" with no upper bound, so this asks
// just after the message and picks it out of the answer; a chat that has moved a long way on since the
// message costs one page of history to answer.
func (c *Client) chatLogByID(ctx context.Context, chatID, logID int64) (loco.ChatLog, error) {
	cl, err := c.readChatLog(ctx, chatID, logID)
	if err == nil || !sessionWentAway(err) {
		return cl, err
	}
	// A broken pipe means the session this request travelled on is dead. The pump replaces it when it
	// can, so one short wait covers the ordinary case of a session being re-established right now.
	select {
	case <-ctx.Done():
		return cl, err
	case <-time.After(2 * time.Second):
	}
	return c.readChatLog(ctx, chatID, logID)
}

// sessionWentAway says whether a failed LOCO call failed on the way to Kakao rather than at Kakao, which
// is worth trying once more because the next attempt may use a session the pump has just rebuilt.
func sessionWentAway(err error) bool {
	var inner *loco.InnerError
	return !errors.As(err, &inner)
}

func (c *Client) readChatLog(ctx context.Context, chatID, logID int64) (loco.ChatLog, error) {
	sess := c.session()
	if sess == nil {
		return loco.ChatLog{}, fmt.Errorf("kknet: no LOCO session to ask about chat %d", chatID)
	}
	f, err := sess.MChatLogs([]int64{chatID}, []int64{logID - 1})
	if err != nil {
		return loco.ChatLog{}, fmt.Errorf("kknet: asking Kakao for chat %d message %d: %w", chatID, logID, err)
	}
	logs, _, err := loco.ParseChatLogs(f.Body)
	if err != nil {
		return loco.ChatLog{}, err
	}
	for _, cl := range logs {
		if cl.LogID == logID {
			return cl, nil
		}
	}
	return loco.ChatLog{}, fmt.Errorf("kknet: Kakao did not return chat %d message %d", chatID, logID)
}
