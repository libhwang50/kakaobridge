// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"github.com/libhwang50/kakaobridge/kakao"
	"github.com/libhwang50/kakaobridge/loco"
)

// Client is one KakaoTalk account session (bridgev2.NetworkAPI). It holds the credential the login
// flow produced and can renew it on its own: a *registered* device keeps the same token pair across
// session ends and re-mints it with login.json (docs/protocol-notes.md §4), so no human is
// needed to keep this alive. A win32 (PC) session has neither property, which is why the config
// defaults to the android namespace.
type Client struct {
	conn  *Connector
	login *bridgev2.UserLogin
	meta  *LoginMeta
	auth  *kakao.DeviceAuth

	mu       sync.Mutex
	loggedIn bool
	lastErr  string

	// The live LOCO session and what it has told us about. The caches are deliberately separate from
	// the credential state: they describe the connection, and they are dropped when it closes.
	stopped bool // the operator stopped this login; the pump must not reconnect
	// connecting says a Connect is in progress on this client. One device credential carries one LOCO
	// session, so a second Connect on the same client would get the first kicked out.
	connecting bool
	sess       *loco.Session
	pumpStop   context.CancelFunc
	rooms      map[int64]*roomData
	users      map[int64]loco.Member
	seenChats  map[int64]string // chatId -> chat type, from LOGINLIST
	// links maps an Open Link id to the chat that uses it. Only rooms whose CHATINFO has been read are
	// in here, which is what lets a SYNCLINKUP push find the room it describes.
	links map[int64]int64
	// bridged lists the Kakao log ids this client has put in Matrix, per chat, in increasing order.
	// A read marker names a log id, and it may name a message the bridge never bridged (a type it
	// skips, or history Kakao did not serve), so the bridge needs to know what it did bridge in order
	// to put the receipt on the nearest older message that is actually there.
	bridged map[int64][]int64
}

var (
	_ bridgev2.NetworkAPI           = (*Client)(nil)
	_ bridgev2.NetworkAPIWithUserID = (*Client)(nil)
)

func NewClient(c *Connector, login *bridgev2.UserLogin, meta *LoginMeta) *Client {
	return &Client{
		conn:      c,
		login:     login,
		meta:      meta,
		auth:      meta.DeviceAuth(&c.cfg),
		rooms:     map[int64]*roomData{},
		users:     map[int64]loco.Member{},
		seenChats: map[int64]string{},
		links:     map[int64]int64{},
		bridged:   map[int64][]int64{},
	}
}

// Connect re-mints the token pair (the refresh path we verified; renew_token.json does not work -
// docs/protocol-notes.md §4). NetworkAPI.Connect must not return an error: connection problems are
// reported through BridgeState, which is what the management room and the user's status room show.
func (c *Client) Connect(ctx context.Context) {
	if !c.claimConnect() {
		c.login.Log.Debug().Msg("Connect called while this client is already connecting or connected; ignoring")
		return
	}
	defer c.endConnect()

	c.login.BridgeState.Send(status.BridgeState{StateEvent: status.StateConnecting})

	// Kakao renews a registered device with the account *password*. A login that has none (one
	// imported from a token file, on a bridge that does not put it in the config) cannot renew
	// itself, and the gateway's answer is an unhelpful status 30 - say what is actually missing.
	if c.meta.Password == "" && c.conn.cfg.Password == "" {
		c.mu.Lock()
		c.loggedIn = false
		c.lastErr = "no account password stored for this login"
		c.mu.Unlock()
		c.login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateBadCredentials,
			Message: "This login has no stored account password, so Kakao cannot renew it. " +
				"Run login again (the password can be given in chat), or set network.password.",
		})
		return
	}

	tok, err := c.auth.Login(ctx)
	if err != nil {
		var se *kakao.StatusError
		c.mu.Lock()
		c.loggedIn = false
		c.lastErr = err.Error()
		c.mu.Unlock()
		ev := status.StateUnknownError
		msg := fmt.Sprintf("Kakao login failed: %s", err)
		if ok := asStatus(err, &se); ok && se.Status == -100 {
			// -100 for a device that was registered before means the identity is no longer usable:
			// only a new passcode approval fixes it, so ask the user rather than retry forever.
			ev = status.StateBadCredentials
			msg = "Kakao no longer accepts this device (it was deregistered, or its session ended " +
				"and it was never registered). Run login again to verify it on your phone."
		}
		c.login.BridgeState.Send(status.BridgeState{StateEvent: ev, Message: msg})
		return
	}

	c.mu.Lock()
	c.meta.AccessToken = tok.AccessToken
	c.meta.RefreshToken = tok.RefreshToken
	c.meta.Recipe = tok.Recipe
	c.meta.UserID = tok.UserID
	c.meta.AccountID = tok.AccountID
	if tok.DisplayAccountID != "" {
		c.meta.DisplayAccount = tok.DisplayAccountID
	}
	c.meta.MintedAt = time.Now().Unix()
	c.loggedIn = true
	c.lastErr = ""
	c.mu.Unlock()

	// The credential is what the login flow stored; keep the store in step with it. Kakao hands back
	// the same pair for a registered device today, but this is the place that would notice a rotation.
	if err := c.login.Save(ctx); err != nil {
		c.login.Log.Warn().Err(err).Msg("Failed to store the renewed Kakao credential")
	}

	// Branding before the session: the icon only needs the homeserver, and bridgev2 takes it from the
	// network descriptor when it creates a space, which the first chat list below can trigger.
	c.ensureBranding(ctx)

	// The credential proves who this is; the LOCO session is what carries conversations. Without it
	// nothing flows, so a failure here is reported as a connection problem rather than a happy state
	// with fine print.
	chats, err := c.startSession(ctx)
	if err != nil {
		c.mu.Lock()
		c.loggedIn = false
		c.lastErr = err.Error()
		c.mu.Unlock()
		c.login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Message:    err.Error(),
		})
		return
	}
	c.login.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateConnected,
		// Say what actually works. It is the only capability summary the user gets without reading
		// code, so it has to stay in step with what the connector implements: name each direction
		// separately, because Kakao→Matrix and Matrix→Kakao support differ per feature.
		Message: fmt.Sprintf("Signed in to KakaoTalk with %d chats in view. "+
			"Messages, media and read receipts bridge from KakaoTalk; text, photos, videos and files bridge "+
			"back to KakaoTalk. Not bridged yet: voice notes, edits or read receipts sent from Matrix.", chats),
	})
}

// claimConnect reports whether this client may start a connection: it refuses one while another
// Connect is running or a session is live. It is the guard for the places that start a connection
// outside the framework's own paths (a login flow, see PasscodeLogin.complete).
func (c *Client) claimConnect() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.connecting || c.sess != nil {
		return false
	}
	c.connecting = true
	return true
}

// endConnect releases the claim. A session that came up keeps the client blocked for further
// Connects on its own, so this only clears the in-progress flag; the session flag is cleared by
// stopSession.
func (c *Client) endConnect() {
	c.mu.Lock()
	c.connecting = false
	c.mu.Unlock()
}

func asStatus(err error, target **kakao.StatusError) bool {
	if se, ok := err.(*kakao.StatusError); ok {
		*target = se
		return true
	}
	return false
}

// Disconnect closes the session. Nothing to close yet: Connect does not hold a LOCO connection.
// Disconnect closes the LOCO session and drops everything that described it. The credential stays:
// a registered Kakao device survives this, and reconnecting re-mints the same token pair.
func (c *Client) Disconnect() {
	c.mu.Lock()
	c.stopped = true
	c.mu.Unlock()
	c.stopSession()
	c.mu.Lock()
	c.loggedIn = false
	c.rooms = map[int64]*roomData{}
	c.users = map[int64]loco.Member{}
	c.seenChats = map[int64]string{}
	c.links = map[int64]int64{}
	c.mu.Unlock()
}

// session returns the live LOCO session, if any.
func (c *Client) session() *loco.Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sess
}

func (c *Client) IsLoggedIn() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.loggedIn
}

// LogoutRemote asks Kakao to end this device's session. The sub-device endpoints come from the phone
// client's own route table; they are the right verbs but have not been
// exercised from the bridge yet, so failures are logged, not fatal.
func (c *Client) LogoutRemote(ctx context.Context) {
	c.Disconnect()
	c.mu.Lock()
	c.loggedIn = false
	c.mu.Unlock()
	if c.meta.Agent != "android" {
		return
	}
	rc := c.rest()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		kakao.BaseURL+"android/sub_device/settings/logout.json", nil)
	if err != nil {
		return
	}
	rc.SetAuth(req)
	resp, err := rc.HTTP.Do(req)
	if err != nil {
		c.login.Log.Warn().Err(err).Msg("Kakao sub-device logout failed")
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func (c *Client) rest() *kakao.Client {
	return kakao.New(kakao.Auth{
		Token:    c.meta.AccessToken,
		DeviceID: c.meta.DeviceUUID,
		Agent:    c.meta.Agent,
		Version:  c.meta.AppVer,
		Platform: platformOf(c.meta.Agent),
		Lang:     c.meta.Language,
	})
}

func platformOf(agent string) string {
	if agent == "android" {
		return "An"
	}
	return "Wd"
}

// GetUserID is the remote id of this login; Kakao identities are numeric user ids.
func (c *Client) GetUserID() networkid.UserID {
	c.mu.Lock()
	defer c.mu.Unlock()
	return networkid.UserID(fmt.Sprintf("%d", c.meta.UserID))
}

func (c *Client) IsThisUser(_ context.Context, userID networkid.UserID) bool {
	return userID == c.GetUserID()
}

// GetChatInfo/GetUserInfo return empty info for now: the room and contact data lives in LOCO
// LOGINLIST + the friends endpoints, and both arrive with the event pump (milestone 2). Empty (not
// nil) means "nothing to change", so rooms keep whatever they have.
// GetChatInfo answers bridgev2's request for a room's metadata. It goes to the live session: the
// CHATINFO/GETMEM pair is the only place the server gives a room's type, members and names.
func (c *Client) GetChatInfo(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
	chatID, err := chatIDOf(portal.PortalKey)
	if err != nil {
		return nil, err
	}
	return c.chatInfoFor(ctx, chatID)
}

// chatIDOf reads the Kakao chat id back out of a portal key.
func chatIDOf(key networkid.PortalKey) (int64, error) {
	id, err := strconv.ParseInt(string(key.ID), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("kknet: portal key %q is not a Kakao chat id", key.ID)
	}
	return id, nil
}

// GetUserInfo fills in a ghost's name and avatar. Names come from room member lists (Kakao sends
// profiles per room); the friend list is the fallback for a person who is not in any bridged room.
func (c *Client) GetUserInfo(ctx context.Context, ghost *bridgev2.Ghost) (*bridgev2.UserInfo, error) {
	uid, err := strconv.ParseInt(string(ghost.ID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("kknet: ghost id %q is not a Kakao user id", ghost.ID)
	}
	c.mu.Lock()
	m, ok := c.users[uid]
	c.mu.Unlock()
	if ok {
		return userInfoOf(m), nil
	}
	if uid == c.meta.UserID {
		prof, err := c.rest().SelfProfile()
		if err != nil {
			return nil, err
		}
		return userInfoOf(loco.Member{UserID: uid, Nickname: prof.Nickname,
			ProfileImageURL: prof.Avatar, StatusMessage: prof.StatusMessage}), nil
	}
	fs, err := c.rest().Friends()
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if f.UserID == uid {
			mm := loco.Member{UserID: f.UserID, AccountID: f.AccountID, Nickname: f.Nickname,
				ProfileImageURL: f.Avatar, StatusMessage: f.StatusMessage,
				Suspended: f.Suspended, Memorial: f.Memorial}
			c.mu.Lock()
			c.users[uid] = mm
			c.mu.Unlock()
			return userInfoOf(mm), nil
		}
	}
	return nil, fmt.Errorf("kknet: Kakao has no profile for user %d", uid)
}

// GetCapabilities says what may be sent into a Kakao chat. It is static because Kakao does not vary
// this per chat. The mime list is the set the shipped clients use for a photo message; naming it lets
// bridgev2 reject an unsupported type with a reason the user can read, instead of the event reaching the
// bridge and dying there. No size limit is claimed: a 7.7 MB photo uploaded through the gateway in a
// probe, and the real ceiling has not been measured, so the bridge does not invent one. A caption is
// only partially supported: Kakao's composer has no caption for a photo and the server ignores
// extra.cmt, so the bridge sends it as a separate message (see sendCaptionAfter).
//
// Videos are claimed per container, because that is what the gateway's answer is keyed on: it hands out a
// ticket for mp4/webm/mkv/mov and then transcodes the file itself (the chat log serves a renamed
// `talkv_high.mp4`), so only mp4 is claimed as fully supported. A plain file is claimed for any mime type
// but at partial support, because the gateway keeps its own allow-list of extensions - it answers
// EXTENSION_NOT_ALLOWED for e.g. .apk - and the bridge passes that refusal through unchanged.
// Audio is not claimed: Kakao voice notes are their own format and have not been measured.
//
// The four formatting marks are what a Kakao bubble really has room for, and both directions of them are
// implemented: kakaoTextOf reads them out of a formatted Matrix message and sends them the way the client
// does, styledHTML renders the ones Kakao sends back. What is not claimed - underline, code, quotes, lists,
// link markup - is sent as its words, which is what Kakao's own client does with text it cannot mark up.
func (c *Client) GetCapabilities(_ context.Context, _ *bridgev2.Portal) *event.RoomFeatures {
	images := map[string]event.CapabilitySupportLevel{
		"image/jpeg": event.CapLevelFullySupported,
		"image/png":  event.CapLevelFullySupported,
		"image/gif":  event.CapLevelFullySupported,
		"image/webp": event.CapLevelFullySupported,
	}
	return &event.RoomFeatures{
		Formatting: event.FormattingFeatureMap{
			event.FmtBold:          event.CapLevelFullySupported,
			event.FmtItalic:        event.CapLevelFullySupported,
			event.FmtStrikethrough: event.CapLevelFullySupported,
			event.FmtSpoiler:       event.CapLevelFullySupported,
		},
		File: event.FileFeatureMap{
			event.MsgImage: &event.FileFeatures{
				MimeTypes: images,
				Caption:   event.CapLevelPartialSupport,
			},
			event.CapMsgSticker: &event.FileFeatures{
				MimeTypes: images,
				Caption:   event.CapLevelDropped,
			},
			event.MsgVideo: &event.FileFeatures{
				MimeTypes: map[string]event.CapabilitySupportLevel{
					"video/mp4":        event.CapLevelFullySupported,
					"video/webm":       event.CapLevelPartialSupport,
					"video/x-matroska": event.CapLevelPartialSupport,
					"video/quicktime":  event.CapLevelPartialSupport,
				},
				Caption: event.CapLevelPartialSupport,
			},
			event.MsgFile: &event.FileFeatures{
				MimeTypes: map[string]event.CapabilitySupportLevel{"*/*": event.CapLevelPartialSupport},
				Caption:   event.CapLevelPartialSupport,
			},
		},
	}
}

// HandleMatrixMessage is milestone 2: it needs the LOCO write path (KSEND/SENDMESSAGE with the auth
// handshake) plus our DNA media upload for attachments. Explicit rather than silently dropped.
// HandleMatrixMessage sends a Matrix message into a Kakao chat with LOCO WRITE. The server assigns the
// logId in its answer, so the bridge hands Matrix the real remote id rather than a placeholder; the
// echo of this message (if the server pushes one back) is then recognised as the same message.
func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	chatID, err := chatIDOf(msg.Portal.PortalKey)
	if err != nil {
		return nil, err
	}
	// A photo takes the upload path instead of a WRITE, and file/video/audio are named rather than
	// guessed at, so the user is told what the bridge can do instead of seeing a dropped event. A
	// Matrix sticker is sent as a photo: Kakao's own stickers come in as image attachments, so this is
	// the shape its clients expect.
	switch msg.Content.MsgType {
	case event.MsgImage, event.CapMsgSticker:
		return c.sendMatrixPhoto(ctx, msg, chatID)
	case event.MsgVideo:
		return c.sendMatrixVideo(ctx, msg, chatID)
	case event.MsgFile:
		return c.sendMatrixFile(ctx, msg, chatID)
	case event.MsgAudio:
		return nil, mediaRefusal(msg.Content.MsgType)
	}
	text, styles, err := kakaoTextOf(msg.Content)
	if err != nil {
		return nil, err
	}
	sess := c.session()
	if sess == nil {
		return nil, fmt.Errorf("kknet: not connected to KakaoTalk right now; try again in a moment")
	}
	f, err := sess.WriteRaw(chatID, text, loco.WriteExtra(styles), loco.ClientMsgID())
	if err != nil {
		return nil, fmt.Errorf("kknet: sending to Kakao: %w", err)
	}
	wr, err := loco.ParseWriteResponse(f.Body)
	if err != nil {
		return nil, err
	}
	c.login.Log.Info().Int64("chat_id", chatID).Int64("log_id", wr.LogID).
		Msg("Message sent to Kakao")
	c.rememberBridged(chatID, wr.LogID)
	return &bridgev2.MatrixMessageResponse{
		DB: &database.Message{
			ID:        networkid.MessageID(strconv.FormatInt(wr.LogID, 10)),
			Timestamp: time.Unix(wr.SendTime, 0),
		},
		StreamOrder: wr.LogID,
	}, nil
}

// kakaoTextOf decides what can actually be sent. Kakao's WRITE carries the text on its own and, when the
// message is formatted, the styles beside it in a separate field: matrixStyle reads them out of the HTML a
// Matrix client sends, and WriteExtra spells them the way the client does. A message with no HTML keeps its
// body, because that is all the sender had. Anything that needs a media pipeline is refused in words the
// user can act on rather than silently degraded.
func kakaoTextOf(content *event.MessageEventContent) (string, *loco.RichText, error) {
	switch content.MsgType {
	case event.MsgText, event.MsgNotice:
		if text, styles, ok := matrixStyle(content); ok {
			return text, styles, nil
		}
		if content.Body == "" {
			return "", nil, fmt.Errorf("kknet: empty message")
		}
		return content.Body, nil, nil
	case event.MsgEmote:
		return "* " + content.Body, nil, nil
	default:
		return "", nil, fmt.Errorf("kknet: only text and photos can be sent to KakaoTalk so far "+
			"(this one is %s)", content.MsgType)
	}
}
