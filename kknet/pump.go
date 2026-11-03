// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

// The LOCO side of the bridge: a live session, the chat list it reports, and the messages.
//
// What the server tells us, and what we do with it (all measured on the bridge's own registered
// mobile device; see docs/protocol-notes.md §5 and §4):
//
//	LOGINLIST      on login: chats with recent traffic (chatId, type, lastLogId, preview), plus the
//	             push cursor lastTokenId. It is a delta, not a room list - a chat nobody touched is
//	             not named here.
//	CHATINFO       per chat: type, member counts, last message, display members with nicknames and
//	             profile image URLs. This is what a Matrix room is created from.
//	GETMEM         per chat: full member list with profiles. Ghost display names come from here.
//	MCHATLOGS      history for DirectChat/MemoChat (returns everything after the given logId, with
//	             eof). It returns nothing for PlusChat, even when that chat has messages - the
//	             messages exist (LOGINLIST previews and CHATINFO.lastChatLog both show them), this
//	             verb just does not serve them.
//	MSG/WRITE/...  pushes: the same chatLog documents, live.
//
// So a room appears in Matrix when the bridge sees it in LOGINLIST, or when a chat resync/message
// event arrives for it. Nothing here pretends to know chats the server has not named; that gap is
// documented in docs/bridgev2-notes.md rather than papered over.

import (
	"context"
	"fmt"
	"io"
	stdliblog "log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/rs/zerolog"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"
	"maunium.net/go/mautrix/bridgev2/status"
	"maunium.net/go/mautrix/event"

	"github.com/libhwang50/kakaobridge/loco"
)

// locoWriter adapts the LOCO package's stdlib logger to the bridge's zerolog logger, so connection
// details from the remote side land in the same log as everything else.
type locoWriter struct{ log zerolog.Logger }

func (w *locoWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg != "" {
		w.log.Debug().Str("component", "kakao-loco").Msg(msg)
	}
	return len(p), nil
}

// roomData is what the room verbs said about one chat, cached so that GetChatInfo and the ghost
// lookups do not re-ask the server for every event.
type roomData struct {
	summary *loco.ChatSummary
	members []loco.Member
	seenAt  time.Time
}

// startSession opens a LOCO session with the credential this login holds, then brings the chat list
// into Matrix. It returns the number of chats the server named.
func (c *Client) startSession(ctx context.Context) (int, error) {
	a := c.meta.LocoAuth(&c.conn.cfg)
	lg := c.login.Log
	sess, err := loco.Dial(loco.Config{Auth: a}, stdliblog.New(&locoWriter{log: lg}, "", 0))
	if err != nil {
		return 0, fmt.Errorf("kknet: opening a LOCO session: %w", err)
	}
	c.mu.Lock()
	c.sess = sess
	c.stopped = false
	c.mu.Unlock()

	state, err := loco.ParseLoginlistResponse(sess.LastLoginlist)
	if err != nil {
		return 0, fmt.Errorf("kknet: reading the chat list from LOGINLIST: %w", err)
	}
	c.mu.Lock()
	for _, r := range state.Rooms {
		c.seenChats[r.ChatID] = chatTypeOf(r.Type)
	}
	c.mu.Unlock()

	lg.Info().Int("chats", len(state.Rooms)).Int64("push_cursor", state.LastTokenID).
		Msg("LOCO session online with a chat list")

	pumpCtx, cancel := context.WithCancel(c.conn.bridge.BackgroundCtx)
	c.mu.Lock()
	c.pumpStop = cancel
	c.mu.Unlock()
	go c.pump(pumpCtx, sess)

	c.ingestChatList(ctx, state)
	return len(state.Rooms), nil
}

func (c *Client) stopSession() {
	c.mu.Lock()
	sess, stop := c.sess, c.pumpStop
	c.sess, c.pumpStop = nil, nil
	c.mu.Unlock()
	if stop != nil {
		stop()
	}
	if sess != nil {
		_ = sess.Close()
	}
}

// portalKeyFor picks the Matrix portal for a chat. Kakao's 1:1 rooms (a friend, a plus friend, the
// self-memo chat) are per-account, so they carry this login as the receiver; anything with more
// members is a shared room and does not.
func (c *Client) portalKeyFor(chatID int64, chatType string) networkid.PortalKey {
	key := networkid.PortalKey{ID: networkid.PortalID(strconv.FormatInt(chatID, 10))}
	if isDirect(chatType) {
		key.Receiver = c.login.ID
	}
	return key
}

// isDirect reports whether a Kakao chat type is a 1:1 conversation. DirectChat is a friend chat,
// PlusChat is a plus-friend chat (the server itself reports activeMembersCount 2 for it), and
// MemoChat is the account's own note-to-self chat.
func isDirect(chatType string) bool {
	switch chatType {
	case "DirectChat", "PlusChat", "MemoChat":
		return true
	default:
		return false
	}
}

func chatTypeOf(t string) string { return strings.TrimSpace(t) }

// ingestChatList turns the chats LOGINLIST named into Matrix rooms.
func (c *Client) ingestChatList(ctx context.Context, state *loco.RoomState) {
	log := c.login.Log
	created, msgs := 0, 0
	for _, r := range state.Rooms {
		if err := c.queueChatResync(ctx, r.ChatID, r.Type); err != nil {
			log.Warn().Err(err).Int64("chat_id", r.ChatID).Str("chat_type", r.Type).
				Msg("Failed to create or update the room for a Kakao chat")
			continue
		}
		created++
		// History: the history verb for plus chats is empty, so seed those with the one message
		// CHATINFO carries. For a friend chat MCHATLOGS gives everything the server has.
		n, err := c.backfillChat(ctx, r.ChatID, r.Type)
		if err != nil {
			log.Warn().Err(err).Int64("chat_id", r.ChatID).
				Msg("Failed to load the messages Kakao offers for a chat")
		}
		msgs += n
	}
	for _, gone := range state.Deleted {
		c.queueChatDelete(ctx, gone)
	}
	log.Info().Int("rooms", created).Int("messages", msgs).
		Msg("Chat list bridged: rooms created/updated and messages loaded")
}

// queueChatResync asks bridgev2 to create or update one room. GetChatInfoFunc defers the CHATINFO /
// GETMEM calls to the framework, which only asks when it actually needs the info.
func (c *Client) queueChatResync(ctx context.Context, chatID int64, chatType string) error {
	key := c.portalKeyFor(chatID, chatType)
	res := c.login.Bridge.QueueRemoteEvent(c.login, &simplevent.ChatResync{
		EventMeta: simplevent.EventMeta{
			Type:         bridgev2.RemoteEventChatResync,
			PortalKey:    key,
			CreatePortal: true,
			StreamOrder:  0,
			LogContext: func(cc zerolog.Context) zerolog.Context {
				return cc.Int64("chat_id", chatID)
			},
		},
		GetChatInfoFunc: func(ctx context.Context, portal *bridgev2.Portal) (*bridgev2.ChatInfo, error) {
			return c.chatInfoFor(ctx, chatID)
		},
	})
	return res.Error
}

func (c *Client) queueChatDelete(ctx context.Context, chatID int64) {
	c.mu.Lock()
	chatType := c.seenChats[chatID]
	delete(c.seenChats, chatID)
	delete(c.rooms, chatID)
	c.mu.Unlock()
	c.login.Bridge.QueueRemoteEvent(c.login, &simplevent.ChatDelete{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventChatDelete,
			PortalKey: c.portalKeyFor(chatID, chatType),
		},
	})
}

// chatInfoFor builds the room metadata bridgev2 needs: room type, members (with ghost profiles), and
// no name for a 1:1 room - Matrix names those after the other member's ghost, whose name we fill in
// from GETMEM. The one chat with no other member is the account's own memo chat, so that one is named
// here instead (see memoChatMeta).
func (c *Client) chatInfoFor(ctx context.Context, chatID int64) (*bridgev2.ChatInfo, error) {
	rd, err := c.room(ctx, chatID)
	if err != nil {
		return nil, err
	}
	info := &bridgev2.ChatInfo{}
	chatType := rd.summary.Type
	if isDirect(chatType) {
		roomType := database.RoomTypeDM
		info.Type = &roomType
	}
	members := bridgev2.ChatMemberMap{}
	for _, m := range rd.members {
		if m.UserID == 0 {
			continue
		}
		members.Set(bridgev2.ChatMember{
			EventSender: c.senderOf(m.UserID),
			Membership:  event.MembershipJoin,
			UserInfo:    userInfoOf(m),
		})
	}
	if len(members) > 0 {
		info.Members = &bridgev2.ChatMemberList{IsFull: true, MemberMap: members}
	}
	if chatType == "MemoChat" {
		name, avatarURL := memoChatMeta(rd.members, c.meta.UserID)
		if name != "" {
			info.Name = &name
		}
		if avatarURL != "" {
			url := avatarURL
			info.Avatar = &bridgev2.Avatar{
				ID: networkid.AvatarID(url),
				Get: func(ctx context.Context) ([]byte, error) {
					return fetchURL(ctx, url)
				},
			}
		}
	}
	return info, nil
}

// memoChatMeta says what a MemoChat room is called and what it looks like: the account's own nickname
// and profile picture, which GETMEM carries for the chat's single member.
//
// A MemoChat is the account's note to itself. bridgev2 titles a direct room after its other member and
// this one has none, so left alone the Matrix room has no name at all; KakaoTalk titles it with the
// account nickname. Naming it here is not overriding something the user owns: Kakao does not let you
// rename your own memo chat, its title follows the account nickname, so re-applying it on each resync
// is the same answer the phone shows.
func memoChatMeta(members []loco.Member, selfUserID int64) (name, avatarURL string) {
	for _, m := range members {
		if m.UserID != selfUserID {
			continue
		}
		return m.Nickname, m.FullProfileImageURL
	}
	// Kakao listed the chat differently than expected; the only member is still the best answer.
	for _, m := range members {
		if m.Nickname != "" || m.FullProfileImageURL != "" {
			return m.Nickname, m.FullProfileImageURL
		}
	}
	return "", ""
}

// senderOf marks our own remote user id as "from me", which is what lets double puppeting (and
// correct display of the bridge user's own messages) work later.
func (c *Client) senderOf(userID int64) bridgev2.EventSender {
	sid := networkid.UserID(strconv.FormatInt(userID, 10))
	if userID == c.meta.UserID {
		return bridgev2.EventSender{IsFromMe: true, SenderLogin: c.login.ID, Sender: sid}
	}
	return bridgev2.EventSender{Sender: sid}
}

// room returns (and caches) what CHATINFO and GETMEM say about a chat. Both are asked together
// because neither is useful alone: CHATINFO has the type and last message, GETMEM the members.
func (c *Client) room(ctx context.Context, chatID int64) (*roomData, error) {
	c.mu.Lock()
	rd, ok := c.rooms[chatID]
	sess := c.sess
	c.mu.Unlock()
	if ok && rd.summary != nil && time.Since(rd.seenAt) < 10*time.Minute {
		return rd, nil
	}
	if sess == nil {
		return nil, fmt.Errorf("kknet: no LOCO session for chat %d", chatID)
	}
	cf, err := sess.ChatInfo(chatID)
	if err != nil {
		return nil, fmt.Errorf("kknet: CHATINFO(%d): %w", chatID, err)
	}
	summary, err := loco.ParseChatInfoResponse(cf.Body)
	if err != nil {
		return nil, err
	}
	mf, err := sess.GetMem(chatID)
	if err != nil {
		return nil, fmt.Errorf("kknet: GETMEM(%d): %w", chatID, err)
	}
	members, err := loco.ParseGetMemResponse(mf.Body)
	if err != nil {
		return nil, err
	}
	if len(members) == 0 && len(summary.DisplayMembers) > 0 {
		// Some chats (plus friends) answer GETMEM with no usable entries; displayMembers carries the
		// same names, so the room still gets a member list.
		members = summary.DisplayMembers
	}
	rd = &roomData{summary: summary, members: members, seenAt: time.Now()}
	c.mu.Lock()
	c.rooms[chatID] = rd
	for _, m := range members {
		c.users[m.UserID] = m
	}
	c.mu.Unlock()
	return rd, nil
}

// backfillChat loads what Kakao offers as history for one chat and queues it oldest-first.
//
// There is no paging loop beyond "keep going while the server keeps answering and eof is false":
// MCHATLOGS returns everything after the logId given, and eof is authoritative. prevId is a chain
// link, not proof that older messages are still stored, so it is not used as a cursor.
func (c *Client) backfillChat(ctx context.Context, chatID int64, chatType string) (int, error) {
	sess := c.session()
	if sess == nil {
		return 0, fmt.Errorf("kknet: no LOCO session")
	}
	const maxPages = 20
	var (
		since int64
		count int
	)
	for page := 0; page < maxPages; page++ {
		f, err := sess.MChatLogs([]int64{chatID}, []int64{since})
		if err != nil {
			return count, fmt.Errorf("kknet: MCHATLOGS(%d): %w", chatID, err)
		}
		logs, eof, err := loco.ParseChatLogs(f.Body)
		if err != nil {
			return count, err
		}
		if len(logs) == 0 {
			break
		}
		for _, cl := range logs {
			if err := c.queueChatLog(ctx, cl); err != nil {
				c.login.Log.Warn().Err(err).Int64("chat_id", chatID).Int64("log_id", cl.LogID).
					Msg("Failed to bridge a Kakao message")
			} else {
				count++
			}
		}
		last := logs[len(logs)-1].LogID
		if eof || last <= since {
			break
		}
		since = last
	}
	// Plus chats: the history verb is empty, but CHATINFO carries the last message. Seeding one
	// message is better than an empty room, and it is the only thing the server offers here.
	if count == 0 && strings.EqualFold(chatType, "PlusChat") {
		if rd, err := c.room(ctx, chatID); err == nil && rd.summary.LastChatLog != nil {
			if err := c.queueChatLog(ctx, *rd.summary.LastChatLog); err == nil {
				count++
			}
		}
	}
	return count, nil
}

// pump consumes server-initiated frames until the session dies, then takes over reconnecting. The
// session dies on its own often enough (a network change, an idle keepalive miss, or Kakao's own
// churn) that reconnecting has to be part of holding the connection, not something the operator
// triggers.
func (c *Client) pump(ctx context.Context, sess *loco.Session) {
	log := c.login.Log
	defer func() {
		if c.sessionStoppedOrReplaced(sess) {
			return
		}
		go c.reconnect(ctx)
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case f, ok := <-sess.Events:
			if !ok {
				log.Info().Msg("LOCO push stream closed")
				return
			}
			c.handlePush(ctx, log, f)
		}
	}
}

// sessionStoppedOrReplaced is the guard that keeps a deliberate Disconnect, or a session that was
// already replaced by a newer one, from starting a reconnect.
func (c *Client) sessionStoppedOrReplaced(sess *loco.Session) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped || (c.sess != nil && c.sess != sess)
}

// reconnect rebuilds the LOCO session with backoff. Kakao has no "resume" for a dropped session:
// a new CHECKIN assigns a chat host and LOGINLIST starts from the last cursor we have, which is why
// the resume cursor is kept in the credential rather than in the session.
func (c *Client) reconnect(ctx context.Context) {
	log := c.login.Log
	delay := 5 * time.Second
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		c.mu.Lock()
		stopped := c.stopped
		c.mu.Unlock()
		if stopped || ctx.Err() != nil {
			return
		}
		log.Info().Dur("retry_in", delay).Msg("Reopening the Kakao message connection")
		c.login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateTransientDisconnect,
			Message:    fmt.Sprintf("Reconnecting to KakaoTalk (retry in %s).", delay),
		})
		if _, err := c.startSession(ctx); err != nil {
			log.Warn().Err(err).Msg("Reopening the Kakao connection failed")
			delay = min(delay*2, 2*time.Minute)
			continue
		}
		c.login.BridgeState.Send(status.BridgeState{
			StateEvent: status.StateConnected,
			Message:    "Reconnected to KakaoTalk.",
		})
		return
	}
}

func (c *Client) handlePush(ctx context.Context, log zerolog.Logger, f loco.Frame) {
	switch f.Cmd {
	case "MSG", "WRITE", "WRITEMEDIA", "SYNCMSG":
		logs, _, err := loco.ParseChatLogs(f.Body)
		if err != nil {
			log.Debug().Err(err).Str("cmd", f.Cmd).Msg("Could not parse a pushed chat log")
			return
		}
		for _, cl := range logs {
			if err := c.queueChatLog(ctx, cl); err != nil {
				log.Warn().Err(err).Int64("chat_id", cl.ChatID).Int64("log_id", cl.LogID).
					Msg("Failed to bridge a pushed Kakao message")
			}
		}
	case "DECUNREAD":
		c.handleReadMarker(ctx, log, f)
	case "KICKOUT":
		// Another connection used this device id - typically one of our own probe tools sharing a
		// credential. The pump ends and reconnects; the other side is not a Kakao user we can do
		// anything about.
		log.Warn().Msg("Kakao closed this session: another connection took the same device id")
	default:
		log.Debug().Str("cmd", f.Cmd).Int("body_len", len(f.Body)).Msg("Ignoring a LOCO push")
	}
}

// reportSessionClosed is only interesting if it happens on its own: Connect owns the session, and a
// closed push stream with Connect still alive means the connection went away.
func (c *Client) reportSessionClosed() {
	c.mu.Lock()
	dead := c.sess == nil
	c.mu.Unlock()
	if dead {
		return
	}
	c.login.BridgeState.Send(status.BridgeState{
		StateEvent: status.StateTransientDisconnect,
		Message:    "The Kakao message connection closed. The bridge will reopen it on the next reconnect.",
	})
}

// queueChatLog converts one Kakao message into one Matrix message. Kakao's logId is the message id:
// it is assigned by the server, stable, and increasing, so re-delivery is recognised rather than
// duplicated.
func (c *Client) queueChatLog(ctx context.Context, cl loco.ChatLog) error {
	_ = ctx
	if cl.ChatID == 0 || cl.LogID == 0 {
		return nil
	}
	if cl.Deleted {
		return nil
	}
	c.mu.Lock()
	chatType := c.seenChats[cl.ChatID]
	c.mu.Unlock()

	// Any type that is not text, not media and not a known channel card is recorded rather than silently
	// turned into a placeholder: that is how the type table gets completed instead of guessed.
	if cl.Type != loco.MsgText && !loco.IsMediaMessageType(cl.Type) && !loco.IsChannelMessageType(cl.Type) {
		c.login.Log.Debug().Int64("chat_id", cl.ChatID).Int64("log_id", cl.LogID).
			Int64("kakao_type", int64(cl.Type)).Str("kakao_type_name", loco.MessageTypeName(cl.Type)).
			Str("text", preview(cl.Text, 80)).Str("attachment", preview(fmt.Sprintf("%v", cl.Attachment), 400)).
			Msg("Kakao sent a message type the bridge does not know")
	}

	// simplevent.Message with a ConvertMessageFunc, rather than a pre-converted message: conversion
	// needs the Matrix room and an intent (media is uploaded through the intent), and those only exist
	// when the framework is handling the event.
	res := c.login.Bridge.QueueRemoteEvent(c.login, &simplevent.Message[loco.ChatLog]{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventMessage,
			PortalKey: c.portalKeyFor(cl.ChatID, chatType),
			Sender:    c.senderOf(cl.AuthorID),
			// Kakao's sendAt is in seconds.
			Timestamp:    time.Unix(cl.SendAt, 0),
			StreamOrder:  cl.LogID,
			CreatePortal: true,
			LogContext: func(cc zerolog.Context) zerolog.Context {
				return cc.Int64("chat_id", cl.ChatID).Int64("log_id", cl.LogID)
			},
		},
		ID:   networkid.MessageID(strconv.FormatInt(cl.LogID, 10)),
		Data: cl,
		ConvertMessageFunc: func(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, data loco.ChatLog) (*bridgev2.ConvertedMessage, error) {
			return c.convertChatLog(ctx, portal, intent, data)
		},
	})
	if res.Error == nil {
		c.rememberBridged(cl.ChatID, cl.LogID)
	}
	return res.Error
}

// maxTrackedLogIDs bounds what the read-marker index keeps per chat. Read markers only ever name a
// recent message in practice; the bound keeps a chat with years of history from growing the process.
const maxTrackedLogIDs = 20000

// rememberBridged records that a Kakao message is now in Matrix. Log ids increase, so the common case
// is an append; anything out of order is inserted in place, and the oldest entries are dropped past the
// bound.
func (c *Client) rememberBridged(chatID, logID int64) {
	if chatID == 0 || logID == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.bridged[chatID]
	if len(list) == 0 || list[len(list)-1] < logID {
		list = append(list, logID)
		if len(list) > maxTrackedLogIDs {
			// Keep the newest. Read markers name recent messages in practice; a marker older than the
			// window is ignored rather than guessed at, and the bridge re-bridges history on restart.
			trimmed := make([]int64, len(list)-maxTrackedLogIDs)
			copy(trimmed, list[len(list)-maxTrackedLogIDs:])
			list = trimmed
		}
		c.bridged[chatID] = list
		return
	}
	if i := sort.Search(len(list), func(i int) bool { return list[i] >= logID }); i >= len(list) || list[i] != logID {
		fresh := make([]int64, 0, len(list)+1)
		fresh = append(fresh, list[:i]...)
		fresh = append(fresh, logID)
		c.bridged[chatID] = append(fresh, list[i:]...)
	}
}

// readReceiptTarget picks which message a Kakao read marker belongs on: the exact message if the bridge
// put it in Matrix, otherwise the newest bridged message before it. It gives up only when the marker is
// older than anything bridged, where guessing would claim the member had read messages they had not.
func readReceiptTarget(bridged []int64, watermark int64) (int64, bool) {
	i := sort.Search(len(bridged), func(i int) bool { return bridged[i] >= watermark })
	if i < len(bridged) && bridged[i] == watermark {
		return watermark, true
	}
	if i == 0 {
		return 0, false
	}
	return bridged[i-1], true
}

// handleReadMarker turns Kakao's read marker into a Matrix read receipt. Kakao says "this member has
// read up to this log id"; the log id is the same identifier the bridge already uses as the Matrix-side
// message id, so the receipt lands on the message the member actually stopped at, and the framework
// writes it as that member's ghost (or as the bridge user, for a marker about our own account).
func (c *Client) handleReadMarker(ctx context.Context, log zerolog.Logger, f loco.Frame) {
	du, err := loco.ParseDecUnread(f.Body)
	if err != nil {
		log.Debug().Err(err).Str("cmd", f.Cmd).Msg("Could not parse a Kakao read marker")
		return
	}
	c.mu.Lock()
	chatType := c.seenChats[du.ChatID]
	bridged := append([]int64(nil), c.bridged[du.ChatID]...)
	c.mu.Unlock()

	target, ok := readReceiptTarget(bridged, du.Watermark)
	if !ok {
		log.Debug().Int64("chat_id", du.ChatID).Int64("user_id", du.UserID).Int64("watermark", du.Watermark).
			Msg("Ignoring a Kakao read marker: no bridged message at or before it")
		return
	}
	res := c.login.Bridge.QueueRemoteEvent(c.login, &simplevent.Receipt{
		EventMeta: simplevent.EventMeta{
			Type:        bridgev2.RemoteEventReadReceipt,
			PortalKey:   c.portalKeyFor(du.ChatID, chatType),
			Sender:      c.senderOf(du.UserID),
			StreamOrder: du.Watermark,
			LogContext: func(cc zerolog.Context) zerolog.Context {
				return cc.Int64("chat_id", du.ChatID).Int64("user_id", du.UserID).Int64("watermark", du.Watermark)
			},
		},
		LastTarget:          networkid.MessageID(strconv.FormatInt(target, 10)),
		ReadUpToStreamOrder: target,
	})
	if res.Error != nil {
		log.Warn().Err(res.Error).Int64("chat_id", du.ChatID).Int64("target_log_id", target).
			Msg("Bridging a Kakao read marker failed")
	}
}

// convertedContentOf builds plain-text content for a Kakao message. It is what remains when there is
// no media to carry: text maps straight across, and any other type becomes a notice that says what the
// message was instead of pretending to be text. Media itself is handled in media.go.
func convertedContentOf(cl loco.ChatLog) *event.MessageEventContent {
	if loco.IsChannelMessageType(cl.Type) {
		return channelContentOf(cl)
	}
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: cl.Text}
	if cl.Type == loco.MsgText {
		return content
	}
	content.MsgType = event.MsgNotice
	label := fmt.Sprintf("(%s message, not bridged)", mediaNameOf(cl.Type))
	if content.Body == "" {
		content.Body = label
	} else {
		content.Body = content.Body + "\n" + label
	}
	return content
}

// mediaNameOf names a Kakao media type in the notice we send when we cannot carry it. The names live in
// the protocol package so the log, the notice and cmd/kkdump all say the same thing.
// preview shortens a string for a log line without splitting a UTF-8 sequence.
func preview(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

func mediaNameOf(t int32) string {
	return loco.MessageTypeName(t)
}

// userInfoOf turns a Kakao profile into ghost metadata. The avatar is fetched by the bridge (the
// profile URLs are on Kakao's CDN, and Matrix wants the bytes uploaded to the homeserver).
func userInfoOf(m loco.Member) *bridgev2.UserInfo {
	if m.UserID == 0 {
		return nil
	}
	info := &bridgev2.UserInfo{}
	if m.Nickname != "" {
		name := m.Nickname
		info.Name = &name
	}
	if m.ProfileImageURL != "" {
		url := m.ProfileImageURL
		info.Avatar = &bridgev2.Avatar{
			ID: networkid.AvatarID(url),
			Get: func(ctx context.Context) ([]byte, error) {
				return fetchURL(ctx, url)
			},
		}
	}
	return info
}

var avatarHTTP = &http.Client{Timeout: 20 * time.Second}

func fetchURL(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	res, err := avatarHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kknet: fetching %s: status %d", url, res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 8<<20))
}
