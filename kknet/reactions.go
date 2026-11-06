// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

// Reactions in Kakao are read, not pushed: Kakao tells a listening session about an edit, a member or a
// read marker, but nothing about a reaction (measured over 45 minutes in an Open Chat room with active
// reactions, and on a reaction this bridge's own account made - see docs/protocol-notes.md §12). So the
// bridge asks: once per page while a room's history is read, and again when a room is live and its
// reactions have not been looked at for a while.
//
// Kakao's answer is a per-message state - which items, how many, and who - so it is sent to
// bridgev2 as one ReactionSync per message, which turns the difference into Matrix annotations. The
// Kakao item id is used as the Matrix EmojiID, so adding and removing a reaction on Kakao's side is
// tracked by the identifier rather than by the character shown.

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/rs/zerolog"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/bridgev2/simplevent"

	"github.com/libhwang50/kakaobridge/kakao"
)

// reactionEmoji names the character to show for the reaction items this bridge has watched Kakao's own
// client draw. Every item the reaction picker offers is a mini emoticon (kind 2), a picture rather than
// a Unicode character, so a character for any of them is a choice, not a fact: only the ones seen on
// screen are listed here. The item itself travels with the reaction in reactionExtra either way.
var reactionEmoji = map[string]string{
	"1200509_029": "❤️", // 사랑
	"1200509_021": "👍",  // 엄지척
	"1200509_037": "☑️", // 체크
	"1200509_001": "😆",  // 웃음
	"1200509_002": "😲",  // 놀라움
	"1200509_003": "😢",  // 슬픔
}

// reactionKey says what to show for one reaction item. For an item the bridge has not seen drawn, the
// name Kakao itself gives it is used, which keeps the reaction visible instead of quietly dropping it.
// An item with neither is skipped: there would be nothing to show.
func reactionKey(item kakao.ReactionItem) string {
	if emoji, ok := reactionEmoji[item.Item]; ok {
		return emoji
	}
	if name := item.Alt["ko"]; name != "" {
		return name
	}
	return ""
}

// reactionState is what the bridge remembers about one message's reactions, so that a state it has
// already sent to Matrix is not sent again, and so that a message whose reactions were removed is
// recognised as a change rather than as nothing to do.
type reactionState struct {
	revision int64
	count    int
}

// reactionLookInterval is how long a room's reactions may sit unexamined while it is busy. Kakao does
// not push them, so this is the price of showing them at all; it is deliberately minutes rather than
// seconds, because one look is two calls per message that has reactions.
const reactionLookInterval = 2 * time.Minute

// reactionLookLimit bounds the work one look does: how many messages' reactions it will ask about. A
// page of history in a busy room can name more than this; the rest are picked up on the next look.
const reactionLookLimit = 25

// reactionLookDepth is how far back into a room's bridged history a live look covers, and how many
// messages it asks about at most. Both are deliberately modest: a look is one call for the states plus
// one per message that has reactions, and the messages worth looking at are the recent ones.
const reactionLookDepth = 100

// lookForReactions reads the reactions of the messages in one page, and sends the ones that changed to
// Matrix. It returns how many messages it updated; an error is not fatal to the caller, which is in the
// middle of bridging history.
func (c *Client) lookForReactions(ctx context.Context, log zerolog.Logger, page kakao.RxMetaPage) int {
	chatID := page.ChatID
	rest := c.rest()
	metas, err := rest.RxLogMetas(ctx, page)
	if err != nil {
		log.Debug().Err(err).Int64("chat_id", chatID).Int64("min_log_id", page.MinLogID).
			Int64("max_log_id", page.MaxLogID).Int("limit", page.Limit).
			Msg("Kakao would not say which messages have reactions")
		return 0
	}
	if len(metas) == 0 {
		return 0
	}
	chatType := c.chatTypeFor(ctx, chatID)
	// Only an Open Chat room needs a link id on the per-message call, and its own CHATINFO names it.
	// An ordinary chat is asked without one, which is how the client asks.
	var linkID int64
	if chatType == "OM" {
		rd, err := c.room(ctx, chatID)
		if err != nil || rd.summary == nil || rd.summary.LinkID == 0 {
			log.Debug().Err(err).Int64("chat_id", chatID).
				Msg("An open chat room's link is unknown, so its reactions cannot be read")
			return 0
		}
		linkID = rd.summary.LinkID
	}
	updated := 0
	for _, meta := range metas {
		logID := int64(meta.LogID)
		if logID == 0 || !c.isInMatrix(ctx, chatID, chatType, logID) {
			continue
		}
		if !c.reactionChanged(logID, int64(meta.Revision), len(meta.Reactions)) {
			continue
		}
		if updated >= reactionLookLimit {
			log.Debug().Int64("chat_id", chatID).Int("limit", reactionLookLimit).
				Msg("Stopped reading reactions at the limit for this room")
			break
		}
		if len(meta.Reactions) == 0 {
			// Nothing reacted any more: the empty state is what removes the Matrix annotations.
			c.queueReactions(ctx, log, chatID, chatType, meta, nil)
			updated++
			continue
		}
		people, err := rest.RxLogDetails(ctx, chatID, linkID, logID)
		if err != nil {
			log.Debug().Err(err).Int64("chat_id", chatID).Int64("log_id", logID).
				Msg("Kakao would not say who reacted")
			continue
		}
		c.queueReactions(ctx, log, chatID, chatType, meta, people)
		updated++
	}
	return updated
}

// reactionChanged remembers what was last sent for a message, and says whether this state is new. The
// revision Kakao gives per message moves with every reaction added or removed, which is what makes a
// comparison like this possible without asking who reacted every time.
func (c *Client) reactionChanged(logID, revision int64, count int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.reactions == nil {
		c.reactions = map[int64]reactionState{}
	}
	old, seen := c.reactions[logID]
	if seen && old.revision == revision {
		return false
	}
	// A message that never had reactions and still has none is not a change.
	if count == 0 && (!seen || old.count == 0) {
		return false
	}
	c.reactions[logID] = reactionState{revision: revision, count: count}
	return true
}

// reactionExtra says, next to the character shown, which Kakao reaction this actually is. The character
// is a stand-in for a picture in Kakao's set, so anything that wants to show the real thing, or to send
// the reaction back, needs the item code and its kind rather than a guess from the glyph. The name is
// the one Kakao gives the item, and the key is this bridge's own namespace.
func reactionExtra(item kakao.ReactionItem) map[string]any {
	return map[string]any{
		"cc.libhwang.kakaobridge.reaction": map[string]any{
			"item": item.Item,
			"kind": item.Kind,
			"name": item.Alt["ko"],
		},
	}
}

// queueReactions sends one message's reaction state to Matrix.
func (c *Client) queueReactions(ctx context.Context, log zerolog.Logger, chatID int64, chatType string,
	meta kakao.MessageReactions, people []kakao.Reactor) {
	data := &bridgev2.ReactionSyncData{
		Users: map[networkid.UserID]*bridgev2.ReactionSyncUser{},
		// Kakao names everyone who reacted, so a person missing from this answer really has gone.
		HasAllUsers: true,
	}
	for _, react := range people {
		key := reactionKey(react.ReactionItem)
		if key == "" {
			log.Debug().Str("item", react.Item).Int("kind", react.Kind).
				Msg("A Kakao reaction has no character this bridge knows")
			continue
		}
		for _, user := range react.Users {
			uid := int64(user)
			if uid == 0 {
				continue
			}
			nid := networkid.UserID(strconv.FormatInt(uid, 10))
			entry := data.Users[nid]
			if entry == nil {
				entry = &bridgev2.ReactionSyncUser{HasAllReactions: true}
				data.Users[nid] = entry
			}
			entry.Reactions = append(entry.Reactions, &bridgev2.BackfillReaction{
				Sender:       c.senderOf(uid),
				EmojiID:      networkid.EmojiID(react.Item),
				Emoji:        key,
				ExtraContent: reactionExtra(react.ReactionItem),
			})
		}
	}
	logID := int64(meta.LogID)
	res := c.login.Bridge.QueueRemoteEvent(c.login, &simplevent.ReactionSync{
		EventMeta: simplevent.EventMeta{
			Type:      bridgev2.RemoteEventReactionSync,
			PortalKey: c.portalKeyFor(chatID, chatType),
			// The reaction belongs to the message it is on, and Kakao's revision for it is newer
			// than that message's log id, so it is the order this arrives in.
			StreamOrder: int64(meta.Revision),
			LogContext: func(cc zerolog.Context) zerolog.Context {
				return cc.Int64("chat_id", chatID).Int64("log_id", logID).
					Int64("revision", int64(meta.Revision)).Int("reactions", len(people))
			},
		},
		TargetMessage: networkid.MessageID(strconv.FormatInt(logID, 10)),
		Reactions:     data,
	})
	if res.Error != nil {
		c.forgetReaction(logID)
		log.Debug().Err(res.Error).Int64("chat_id", chatID).Int64("log_id", logID).
			Msg("Could not bridge Kakao reactions")
	}
}

// forgetReaction drops the remembered state so a later look tries again.
func (c *Client) forgetReaction(logID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.reactions, logID)
}

// isInMatrix says whether the message a reaction belongs to is in Matrix. On a message the bridge
// skipped there is no event to react to. The list of what this connection bridged answers it without a
// database call; a message bridged before this connection existed is asked of the database, because a
// reaction on it is worth bridging just the same.
func (c *Client) isInMatrix(ctx context.Context, chatID int64, chatType string, logID int64) bool {
	if c.wasBridged(chatID, logID) {
		return true
	}
	key := c.portalKeyFor(chatID, chatType)
	msg, err := c.login.Bridge.DB.Message.GetFirstPartByID(ctx, key.Receiver,
		networkid.MessageID(strconv.FormatInt(logID, 10)))
	return err == nil && msg != nil
}

// wasBridged says whether this connection put the message in Matrix.
func (c *Client) wasBridged(chatID, logID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.bridged[chatID]
	i := sort.Search(len(list), func(i int) bool { return list[i] >= logID })
	return i < len(list) && list[i] == logID
}

// bridgedRange names the oldest and newest Kakao message the bridge put in Matrix for this chat, which
// is the range worth asking reactions about.
func (c *Client) bridgedRange(chatID int64) (min, max int64, ok bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	list := c.bridged[chatID]
	if len(list) == 0 {
		return 0, 0, false
	}
	return list[0], list[len(list)-1], true
}

// lookForReactionsSoon looks at a room's reactions in the background, at most once per
// reactionLookInterval per room. It is called when a room is live, because that is the only sign the
// bridge has that its reactions may have changed.
func (c *Client) lookForReactionsSoon(chatID int64) {
	c.mu.Lock()
	if c.reactionAt == nil {
		c.reactionAt = map[int64]time.Time{}
	}
	last, seen := c.reactionAt[chatID]
	if seen && time.Since(last) < reactionLookInterval {
		c.mu.Unlock()
		return
	}
	c.reactionAt[chatID] = time.Now()
	c.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		page, ok := c.recentPage(ctx, chatID)
		if !ok {
			return
		}
		c.lookForReactions(ctx, c.login.Log, page)
	}()
}

// recentPage names the range of the messages a room has in Matrix, newest first. What this connection
// bridged is kept in memory for the read receipts and is enough for a busy room; a message bridged in an
// earlier session is only in the database, and a reaction on it is worth reading just the same.
func (c *Client) recentPage(ctx context.Context, chatID int64) (kakao.RxMetaPage, bool) {
	page := kakao.RxMetaPage{ChatID: chatID, NewestFirst: true, Limit: reactionLookDepth}
	if min, max, ok := c.bridgedRange(chatID); ok {
		page.MinLogID, page.MaxLogID = min, max
	}
	key := c.portalKeyFor(chatID, c.chatTypeFor(ctx, chatID))
	rows, err := c.login.Bridge.DB.Message.GetLastNInPortal(ctx, key, reactionLookDepth)
	if err != nil {
		c.login.Log.Debug().Err(err).Int64("chat_id", chatID).
			Msg("Could not list this room's messages to look for reactions")
	}
	for _, row := range rows {
		id, err := strconv.ParseInt(string(row.ID), 10, 64)
		if err != nil {
			continue
		}
		if id > page.MaxLogID {
			page.MaxLogID = id
		}
		if page.MinLogID == 0 || id < page.MinLogID {
			page.MinLogID = id
		}
	}
	if page.MinLogID == 0 || page.MaxLogID == 0 {
		return page, false
	}
	return page, true
}
