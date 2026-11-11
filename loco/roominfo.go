// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// RoomInfo is what the LOCO verb ChatOnRoom reports about a single chat: the type, the member ids,
// and the log/watermark cursors. LOGINLIST gives chat ids, types and previews but *no members*, so
// this is the call that turns a chat id into something a room can be built from.
//
// Wire keys, read from a live response (2026-10-28, os=android):
//
//	c  int64   chatId
//	t  string  "DirectChat" | "MemoChat" | "PlusChat" | "MultiChat" (a group chat) | "OM" (open chat)
//	a  array   member userIds (a 1:1 chat lists the other member and self)
//	l  int64   lastLogId
//	w  array   read watermarks, one per member, in the same order as `a`
//	ca int64   createdAt (ms)
//	u  int64   requester userId (echoed back)
type RoomInfo struct {
	ChatID    int64
	Type      string
	Members   []int64
	LastLogID int64
	CreatedAt int64 // milliseconds
	Watermark int64 // highest read watermark the server reports for this chat
	// ReadMarkers is how far each member has read in this chat. A watermark is a logId, the same
	// identifier a message carries, so it names the last message that member has seen. Kakao reports
	// one per member, which is why a receipt can be attributed instead of only counted.
	ReadMarkers []ReadMarker
}

// ReadMarker is one member's read position in a chat.
type ReadMarker struct {
	UserID int64
	LogID  int64
}

// ParseChatOnRoomResponse decodes a ChatOnRoom response body.
func ParseChatOnRoomResponse(body []byte) (*RoomInfo, error) {
	if inner := InnerStatus(body); inner != 0 {
		return nil, fmt.Errorf("loco: ChatOnRoom inner status %d", inner)
	}
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding ChatOnRoom response: %w", err)
	}
	ri := &RoomInfo{
		ChatID:    int64Val(Get(elems, "c")),
		Type:      stringVal(Get(elems, "t")),
		LastLogID: int64Val(Get(elems, "l")),
		CreatedAt: int64Val(Get(elems, "ca")),
	}
	for _, e := range arrayElems(Get(elems, "a")) {
		if id := int64Val(e.Val); id != 0 {
			ri.Members = append(ri.Members, id)
		}
	}
	// `a` and `w` are parallel positional arrays: entry i of `w` is the watermark of entry i of `a`.
	// Nothing on the wire names the pairing, so it is read that way and only trusted when the two
	// arrays agree on how many members there are.
	memberIDs := arrayElems(Get(elems, "a"))
	watermarks := arrayElems(Get(elems, "w"))
	if len(memberIDs) == len(watermarks) {
		for i, e := range watermarks {
			wm := int64Val(e.Val)
			if wm == 0 {
				continue
			}
			ri.ReadMarkers = append(ri.ReadMarkers, ReadMarker{UserID: int64Val(memberIDs[i].Val), LogID: wm})
			if wm > ri.Watermark {
				ri.Watermark = wm
			}
		}
	} else {
		for _, e := range watermarks {
			if wm := int64Val(e.Val); wm > ri.Watermark {
				ri.Watermark = wm
			}
		}
	}
	if ri.ChatID == 0 {
		return nil, fmt.Errorf("loco: ChatOnRoom response has no chatId")
	}
	return ri, nil
}

// ChatLog is one message: what MCHATLOGS returns in `chatLogs`, and what the server pushes in
// MSG/WRITE/WRITEMEDIA frames as `chatLog`.
//
// Type numbers below are from live traffic on this account, not from Kakao's documentation; unknown
// numbers arrive as-is, which is the safe behaviour for a bridge.
// MessageTypeName names a Kakao message type for logs and user-visible text. Types marked "measured"
// are ones whose messages have been read from this account; the rest are from the verb tables and the
// Android client.
func MessageTypeName(t int32) string {
	switch t {
	case MsgFeed:
		return "room event"
	case MsgText:
		return "text"
	case MsgPhoto:
		return "photo"
	case MsgMultiPhoto:
		return "photos"
	case MsgVideo, MsgLargeVideo:
		return "video"
	case MsgVoice:
		return "voice note"
	case MsgFile, MsgLargeFile:
		return "file"
	case MsgContact:
		return "contact card"
	case MsgLink:
		return "link"
	case MsgEmote:
		return "emote"
	case MsgEmoteAnimated, MsgEmoteAnimated2, MsgAnimatedEmot, MsgSpritecon:
		return "animated emote"
	case MsgReply:
		return "reply"
	case MsgPost:
		return "post"
	case MsgNudge:
		return "nudge"
	case MsgMvoip, MsgVoxRoom:
		return "call"
	case MsgProfile:
		return "profile card"
	case MsgSchedule, MsgVote:
		return "schedule or vote"
	case MsgDigitalGift:
		return "gift"
	case MsgAvatar:
		return "avatar change"
	case MsgLocation, MsgOldLocation, MsgSharpSearch:
		return "location"
	case MsgPlus, MsgPlusEvent, MsgPlusViral, MsgLeverage, MsgAlimtalk, MsgPlusLeverage:
		return "channel message"
	case MsgUniversal, MsgUniversalGood:
		return "channel message"
	default:
		return fmt.Sprintf("type %d", t)
	}
}

// IsMediaMessageType reports whether a type carries a file we could carry across as Matrix media.
func IsMediaMessageType(t int32) bool {
	switch t {
	case MsgPhoto, MsgMultiPhoto, MsgVideo, MsgLargeVideo, MsgVoice, MsgFile, MsgLargeFile,
		MsgEmote, MsgEmoteAnimated, MsgEmoteAnimated2, MsgAnimatedEmot, MsgSpritecon:
		return true
	}
	return false
}

// IsStickerMessageType reports whether the message is an item from an emoticon pack rather than a file the
// sender uploaded. Matrix has a first-class event type for those (m.sticker), and that is what the bridge
// sends: clients draw stickers larger and without a message bubble, use info.w/h for the layout, and loop
// them when info.is_animated is set. mautrix-discord bridges Discord stickers the same way.
func IsStickerMessageType(t int32) bool {
	switch t {
	case MsgEmote, MsgEmoteAnimated, MsgEmoteAnimated2, MsgAnimatedEmot, MsgSpritecon:
		return true
	}
	return false
}

// IsAnimatedMessageType is known from the type rather than the bytes: an animated emote is a .webp, and
// so is a static one, which no header in the attachment tells us.
func IsAnimatedMessageType(t int32) bool {
	switch t {
	case MsgEmoteAnimated, MsgEmoteAnimated2, MsgAnimatedEmot, MsgSpritecon:
		return true
	}
	return false
}

const (
	MsgFeed           int32 = 0 // not chat text: an event about the room (invites, joins, team events) - FeedMeta
	MsgText           int32 = 1 // plain text
	MsgPhoto          int32 = 2 // photo; attachment carries the signed URL
	MsgVideo          int32 = 3 // video; attachment carries two encodings
	MsgContact        int32 = 4 // contact card (text/x-vcard)
	MsgVoice          int32 = 5 // "Audio" in the client: a generated talka_<codec>.m4a, d in ms
	MsgAnimatedEmot   int32 = 6 // animated emoticon carried as text
	MsgDigitalGift    int32 = 7 // digital item gift
	MsgLink           int32 = 9 // link share
	MsgOldLocation    int32 = 10
	MsgAvatar         int32 = 11
	MsgEmote          int32 = 12 // "Sticker": emoticon pack item, .emot_NNN.png
	MsgSchedule       int32 = 13
	MsgVote           int32 = 14
	MsgLocation       int32 = 16
	MsgProfile        int32 = 17
	MsgFile           int32 = 18 // uploaded file; attachment has name/size
	MsgEmoteAnimated  int32 = 20 // "AnimatedSticker": .emot_NNN.webp
	MsgNudge          int32 = 21 // "nudge" poke
	MsgSpritecon      int32 = 22
	MsgSharpSearch    int32 = 23
	MsgPost           int32 = 24
	MsgEmoteAnimated2 int32 = 25 // "AnimatedStickerEx"
	MsgReply          int32 = 26 // quoted reply
	MsgMultiPhoto     int32 = 27 // several photos in one log
	MsgLargeVideo     int32 = 28
	MsgLargeFile      int32 = 29
	MsgMvoip          int32 = 51 // call record
	MsgVoxRoom        int32 = 52
	MsgLeverage       int32 = 71 // old plus/leverage notice
	MsgAlimtalk       int32 = 72 // alimtalk (notification) message
	MsgPlusLeverage   int32 = 73
	MsgPlus           int32 = 81 // plus-chat ("channel") message, may carry buttons
	MsgPlusEvent      int32 = 82 // channel event announcement
	MsgPlusViral      int32 = 83
	MsgUniversal      int32 = 100
	MsgUniversalGood  int32 = 101
)

// Kakao chat-log message types. The values and names are the Android client's own table
// read off the mobile client's own ChatMessageType table, not guesses: types 5, 12, 18 and 20 were also
// observed in this account's history, which is what corrected earlier guesses (4 is a contact share, not
// a link; 27 is a multi-photo post, not an emote; 71/72 are the old leverage/alimtalk formats, not
// plus-chat cards). Negative values in that table are synthetic feed rows, never chat logs, so they are
// not reproduced here.

// IsChannelMessageType reports a message sent by a bot or channel. Those can carry buttons and other
// interactive parts, and Matrix has no agreed representation for interactive messages (no accepted MSC as
// of writing), so they bridge as their text plus a note that the rest is in KakaoTalk - the same thing
// KakaoTalk's own desktop client does with a "Please check on mobile." footer.
func IsChannelMessageType(t int32) bool {
	switch t {
	case MsgLeverage, MsgAlimtalk, MsgPlusLeverage, MsgPlus, MsgPlusEvent, MsgPlusViral,
		MsgUniversal, MsgUniversalGood:
		return true
	}
	return false
}

type ChatLog struct {
	LogID    int64  // server-assigned id; stable, so it is the bridge's message id
	ChatID   int64  `json:"chatId"`
	AuthorID int64  `json:"authorId"`
	Type     int32  `json:"type"`
	SendAt   int64  // unix *seconds*
	Text     string // the `message` field: body for text, alt/description for other types
	MsgID    int64  // client-assigned id (echoed back in WRITE acks)
	PrevID   int64  // previous log in the chat; not a paging cursor (see ParseChatLogs)
	// Revision counts edits of this message: measured 0 for a new message and 1 after one edit, with the
	// same logId and the new text in place of the old. Kakao edits a message rather than replacing it.
	Revision int32
	// Deleted says Kakao no longer shows this message. It is set from a `deleted` field when one is
	// present, and from the 0x4000 type bit (see MsgDeletedBit), which is what "Delete for Everyone"
	// actually does. A message deleted only for one member stays in history with no marker at all: it is
	// hidden per account, and nothing here can see that.
	Deleted bool
	// Attachment is the raw `attachment` element: a BSON doc with media meta for photo/file/video,
	// "{}" for plain text, and a list for plus cards. Kept raw on purpose: the media pipeline wants
	// the original fields, and guessing here would lose them.
	Attachment any
}

// ParseChatLogs decodes a message list from either shape the server uses:
//
//	{status, chatLogs: [...], eof}   MCHATLOGS (history)
//	{chatLog: {...}}                a live MSG/WRITE push
//
// eof says there are no more logs *in the direction asked*. `since` in the request is a lower-bound
// cursor, not an upper bound: MCHATLOGS(chat, 0) returns the oldest page, and
// MCHATLOGS(chat, lastLogId) returns whatever arrived since. prevId is a chain link, not a signal
// that older logs still exist - measured on a chat where eof was true while prevId still pointed
// backwards.
func ParseChatLogs(body []byte) (logs []ChatLog, eof bool, err error) {
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, false, fmt.Errorf("loco: decoding chat log list: %w", err)
	}
	if inner := InnerStatus(body); inner != 0 {
		return nil, false, fmt.Errorf("loco: chat log list inner status %d", inner)
	}
	eof = boolVal(Get(elems, "eof"))
	if single, ok := Get(elems, "chatLog").([]Elem); ok {
		logs = append(logs, chatLogOf(single))
		return logs, eof, nil
	}
	for _, lg := range arrayElems(Get(elems, "chatLogs")) {
		if inner, ok := lg.Val.([]Elem); ok {
			logs = append(logs, chatLogOf(inner))
		}
	}
	return logs, eof, nil
}

// MsgDeletedBit is not a message type. Kakao's "Delete for Everyone" leaves the log in the chat with its
// logId and its text, and sets this bit on top of the original type: measured, a text message (type 1)
// came back as 16385 = 0x4001. Its attachment gains {"lastFeedId":<the notice below>}, and the chat
// gains a type-0 notice saying {"feedType":14,"logId":<this log>,"hidden":true}. Nothing else in the
// type table reaches this high (the largest measured type is 101), so the bit is safe to read as a flag,
// and chatLogOf strips it so the media paths still see the type the message really is.
const MsgDeletedBit int32 = 0x4000

// ParseModifiedChatLog reads SYNCMODMSG, the push Kakao sends when a message is edited. Measured body:
//
//	chatId, logId     the chat, and the id of the room notice in chatLog
//	chatLog           that notice: type 0 with {"logId":<edited log>,"targetRevision":1,
//	                  "hidden":true,"feedType":25}
//	modifiedChatLog   the edited message itself: same logId, the new text, revision 1
//	noSeen, status
//
// The push carries the whole new log, so there is nothing to fetch afterwards. Note that Kakao edits in
// place: the same logId, with revision counting edits.
func ParseModifiedChatLog(body []byte) (edited, notice ChatLog, err error) {
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return ChatLog{}, ChatLog{}, fmt.Errorf("loco: decoding an edit push: %w", err)
	}
	if inner := InnerStatus(body); inner != 0 {
		return ChatLog{}, ChatLog{}, fmt.Errorf("loco: edit push inner status %d", inner)
	}
	mod, ok := findVal(elems, "modifiedChatLog").([]Elem)
	if !ok {
		return ChatLog{}, ChatLog{}, fmt.Errorf("loco: edit push carries no modifiedChatLog")
	}
	edited = chatLogOf(mod)
	if n, ok := findVal(elems, "chatLog").([]Elem); ok {
		notice = chatLogOf(n)
	}
	return edited, notice, nil
}

func chatLogOf(elems []Elem) ChatLog {
	cl := ChatLog{
		LogID:    int64Val(findVal(elems, "logId")),
		ChatID:   int64Val(findVal(elems, "chatId")),
		AuthorID: int64Val(findVal(elems, "authorId")),
		Type:     int32Val(findVal(elems, "type")),
		Revision: int32Val(findVal(elems, "revision")),
		SendAt:   int64Val(findVal(elems, "sendAt")),
		Text:     stringVal(findVal(elems, "message")),
		MsgID:    int64Val(findVal(elems, "msgId")),
		PrevID:   int64Val(findVal(elems, "prevId")),
	}
	if del := findVal(elems, "deleted"); del != nil {
		cl.Deleted = boolVal(del)
	}
	if cl.Type&MsgDeletedBit != 0 {
		cl.Deleted = true
		cl.Type &^= MsgDeletedBit
	}
	cl.Attachment = findVal(elems, "attachment")
	return cl
}

func findVal(elems []Elem, key string) any {
	for _, e := range elems {
		if e.Key == key {
			return e.Val
		}
	}
	return nil
}

// Member is one entry of GETMEM's `members` array: a user with their profile as this room sees it.
// Kakao sends profile data per room rather than globally, which is why ghost names come from here.
type Member struct {
	UserID          int64
	AccountID       int64
	Nickname        string
	ProfileImageURL string
	// FullProfileImageURL is the same picture in the larger size the server offers. A room avatar is
	// displayed much bigger than a contact thumbnail, so a room asks for this one while a ghost keeps
	// the small copy it already had. Measured fields in one GETMEM member: profileImageUrl (a 110x110
	// thumb), fullProfileImageUrl (640x640), originalProfileImageUrl (unresized).
	FullProfileImageURL string
	StatusMessage       string
	Suspended           bool
	Memorial            bool
}

// ChatSummary is what CHATINFO reports about one chat: enough to create and name a room, and enough
// to seed it with the last message when the history verb has nothing to give (plus chats).
type ChatSummary struct {
	ChatID       int64
	Type         string
	ActiveMember int
	NewMessages  int
	LastLogID    int64
	LastSeenLog  int64
	LastChatLog  *ChatLog
	// DisplayMembers are the users the client shows for this chat (for a 1:1 chat: the other side),
	// with nickname and profile image.
	DisplayMembers []Member
	// Metas are the room-level settings the server keeps for this chat (see ChatMeta). Only group
	// chats (type MultiChat) carry any.
	Metas     []ChatMeta
	PushAlert bool
	// LinkID and OpenToken are carried only by Open Chat rooms (type OM): they name the Open Link that
	// holds the room's name, intro and picture, which the chat itself never reports (see OpenLink).
	LinkID    int64
	OpenToken int64
}

// ChatMeta is one entry of CHATINFO's chatMetas array: a room-level setting, changed by a member and
// replayed to everyone. Measured on a MultiChat (Kakao's group chat):
//
//	type 3  the room title, in content as plain text
//	type 4  the room picture, in content as {"imageUrl":…, "fullImageUrl":…}
//	type 15 team-chat state, in content as {"warehouse":true, "hostId":"…", "revision":N} - its
//	        presence is what makes a MultiChat a Team Chat, and hostId names the host
//
// Other types exist (the client has settings for notifications and such); they are kept verbatim and
// ignored here.
type ChatMeta struct {
	Type      int
	Revision  int64
	AuthorID  int64
	Content   string
	UpdatedAt int
}

func ParseGetMemResponse(body []byte) ([]Member, error) {
	if inner := InnerStatus(body); inner != 0 {
		return nil, fmt.Errorf("loco: GETMEM inner status %d", inner)
	}
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding GETMEM response: %w", err)
	}
	var out []Member
	for _, e := range arrayElems(Get(elems, "members")) {
		if doc, ok := e.Val.([]Elem); ok {
			out = append(out, memberOf(doc))
		}
	}
	return out, nil
}

// ParseMemberResponse reads what MEMBER answers: profiles for the user ids a chat was asked about.
// GETMEM lists a room, MEMBER answers for named people, which is how a person the bridge has no profile
// for gets a name.
//
// The answer is about a room, not about a person, and that matters. In an ordinary chat it spells the
// fields out (`nickName`, `profileImageUrl`, `fullProfileImageUrl`, `originalProfileImageUrl`); in an Open
// Chat room the same user id answers with the open profile that room shows, under the short keys:
//
//	{"userId":1,"nickName":"an open name",
//	 "pi":"…/img_s.jpg","fpi":"…/img_l.jpg","opi":"…/img.jpg","mt":1,"ptp":16}
//
// The same account answered "a real name" in a group chat and "an open name" with open.kakaocdn.net
// pictures in an Open Chat room. Members come back in no particular order, so a caller must key them by
// user id; empty image fields mean the default avatar, not a failed lookup.
func ParseMemberResponse(body []byte) ([]Member, error) {
	if inner := InnerStatus(body); inner != 0 {
		return nil, fmt.Errorf("loco: MEMBER inner status %d", inner)
	}
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding MEMBER response: %w", err)
	}
	var out []Member
	for _, e := range arrayElems(Get(elems, "members")) {
		if doc, ok := e.Val.([]Elem); ok {
			out = append(out, memberOf(doc))
		}
	}
	return out, nil
}

func ParseChatInfoResponse(body []byte) (*ChatSummary, error) {
	if inner := InnerStatus(body); inner != 0 {
		// Typed, because -401 is the answer a chat this account is no longer in gives, and the caller has
		// to tell that apart from a socket that went away.
		return nil, fmt.Errorf("loco: %w", &InnerError{Verb: "CHATINFO", Status: inner})
	}
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding CHATINFO response: %w", err)
	}
	info, ok := Get(elems, "chatInfo").([]Elem)
	if !ok {
		return nil, fmt.Errorf("loco: CHATINFO response has no chatInfo")
	}
	cs := &ChatSummary{
		ChatID:       int64Val(findVal(info, "chatId")),
		Type:         stringVal(findVal(info, "type")),
		ActiveMember: int(int32Val(findVal(info, "activeMembersCount"))),
		NewMessages:  int(int32Val(findVal(info, "newMessageCount"))),
		LastLogID:    int64Val(findVal(info, "lastLogId")),
		LastSeenLog:  int64Val(findVal(info, "lastSeenLogId")),
		PushAlert:    boolVal(findVal(info, "pushAlert")),
		LinkID:       int64Val(findVal(info, "li")),
		OpenToken:    int64Val(findVal(info, "otk")),
	}
	if doc, ok := findVal(info, "lastChatLog").([]Elem); ok {
		cl := chatLogOf(doc)
		cs.LastChatLog = &cl
	}
	for _, e := range arrayElems(findVal(info, "displayMembers")) {
		if doc, ok := e.Val.([]Elem); ok {
			cs.DisplayMembers = append(cs.DisplayMembers, memberOf(doc))
		}
	}
	for _, e := range arrayElems(findVal(info, "chatMetas")) {
		if doc, ok := e.Val.([]Elem); ok {
			cs.Metas = append(cs.Metas, ChatMeta{
				Type:      int(int32Val(findVal(doc, "type"))),
				Revision:  int64Val(findVal(doc, "revision")),
				AuthorID:  int64Val(findVal(doc, "authorId")),
				Content:   stringVal(findVal(doc, "content")),
				UpdatedAt: int(int32Val(findVal(doc, "updatedAt"))),
			})
		}
	}
	return cs, nil
}

// Meta returns the first chat meta of the given type, or nil.
func (cs *ChatSummary) Meta(typ int) *ChatMeta {
	for i := range cs.Metas {
		if cs.Metas[i].Type == typ {
			return &cs.Metas[i]
		}
	}
	return nil
}

// Title is the room name a group chat was given (chatMeta type 3). An unnamed group has no such meta:
// the client composes a title from the member list locally, which is why the caller must not treat an
// empty answer as "this room is nameless to Kakao too".
func (cs *ChatSummary) Title() string {
	if m := cs.Meta(chatMetaTitle); m != nil {
		return strings.TrimSpace(m.Content)
	}
	return ""
}

// RoomImage is the picture a Team Chat can be given (chatMeta type 4), full size first.
func (cs *ChatSummary) RoomImage() (full, thumb string) {
	m := cs.Meta(chatMetaImage)
	if m == nil {
		return "", ""
	}
	var v struct {
		ImageURL     string `json:"imageUrl"`
		FullImageURL string `json:"fullImageUrl"`
	}
	if err := json.Unmarshal([]byte(m.Content), &v); err != nil {
		return "", ""
	}
	return v.FullImageURL, v.ImageURL
}

// TeamHost reports the host of a Team Chat; the chatMeta that carries it (type 15) is also what tells
// a Team Chat from a plain group chat, so its absence is the answer for the latter.
func (cs *ChatSummary) TeamHost() (int64, bool) {
	m := cs.Meta(chatMetaTeam)
	if m == nil {
		return 0, false
	}
	var v struct {
		HostID string `json:"hostId"`
	}
	if err := json.Unmarshal([]byte(m.Content), &v); err != nil {
		return 0, false
	}
	host, err := strconv.ParseInt(strings.TrimSpace(v.HostID), 10, 64)
	if err != nil || host == 0 {
		return 0, false
	}
	return host, true
}

const (
	chatMetaTitle = 3
	chatMetaImage = 4
	chatMetaTeam  = 15
)

func memberOf(elems []Elem) Member {
	return Member{
		UserID:    int64Val(findVal(elems, "userId")),
		AccountID: int64Val(findVal(elems, "accountId")),
		Nickname:  stringVal(findVal(elems, "nickName")),
		// Two key spellings, both measured: CHATINFO's displayMembers spell these out
		// (profileImageUrl / fullProfileImageUrl / originalProfileImageUrl) while a GETMEM member uses the
		// shared dictionary's short keys (pi / fpi / opi), so both are read. A room avatar asks for the
		// full-size one; a 110x110 thumb is only good enough for a contact list.
		ProfileImageURL: firstNonEmpty(stringVal(findVal(elems, "profileImageUrl")),
			stringVal(findVal(elems, "pi")),
			stringVal(findVal(elems, "fullProfileImageUrl")), stringVal(findVal(elems, "fpi")),
			stringVal(findVal(elems, "originalProfileImageUrl")), stringVal(findVal(elems, "opi"))),
		FullProfileImageURL: firstNonEmpty(stringVal(findVal(elems, "fullProfileImageUrl")),
			stringVal(findVal(elems, "fpi")),
			stringVal(findVal(elems, "originalProfileImageUrl")), stringVal(findVal(elems, "opi")),
			stringVal(findVal(elems, "profileImageUrl")), stringVal(findVal(elems, "pi"))),
		StatusMessage: stringVal(findVal(elems, "statusMessage")),
		Suspended:     boolVal(findVal(elems, "suspended")),
		Memorial:      boolVal(findVal(elems, "memorial")),
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// DecUnread is the server's push telling clients that a member's unread count went down, i.e. that
// member read the chat up to a watermark. Measured from a live push :
//
//	{"chatId":482943465622166,"userId":447137810,"watermark":3956272871373338627,"serviceId":null,"status":0}
//
// The watermark is a logId, so it names the last message that member has seen. `serviceId` was null in
// every capture; it appears only to mark pushes that arrive from another Kakao service.
type DecUnread struct {
	ChatID    int64
	UserID    int64
	Watermark int64
}

// ParseDecUnread decodes a DECUNREAD push body. It does not treat the embedded `status` as an error:
// pushes carry status 0 as part of the payload.
func ParseDecUnread(body []byte) (*DecUnread, error) {
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding DecUnread push: %w", err)
	}
	du := &DecUnread{
		ChatID:    int64Val(Get(elems, "chatId")),
		UserID:    int64Val(Get(elems, "userId")),
		Watermark: int64Val(Get(elems, "watermark")),
	}
	if du.ChatID == 0 || du.Watermark == 0 {
		return nil, fmt.Errorf("loco: DecUnread push has no chatId or watermark: chatId=%d watermark=%d", du.ChatID, du.Watermark)
	}
	return du, nil
}

// WriteResult is what a WRITE (send a message) answers with. The server assigns logId here, so the
// bridge can return the real remote id to Matrix immediately instead of waiting for an echo.
//
// Measured response (kept as testdata/write_response.hex):
//
//	{chatId, logId, sendAt (seconds), msgId (the client id we sent back), prevId, status}
type WriteResult struct {
	ChatID   int64
	LogID    int64
	MsgID    int64
	PrevID   int64
	SendTime int64 // unix seconds
}

func ParseWriteResponse(body []byte) (*WriteResult, error) {
	if inner := InnerStatus(body); inner != 0 {
		return nil, fmt.Errorf("loco: send rejected with status %d", inner)
	}
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding WRITE response: %w", err)
	}
	wr := &WriteResult{
		ChatID:   int64Val(findVal(elems, "chatId")),
		LogID:    int64Val(findVal(elems, "logId")),
		MsgID:    int64Val(findVal(elems, "msgId")),
		PrevID:   int64Val(findVal(elems, "prevId")),
		SendTime: int64Val(findVal(elems, "sendAt")),
	}
	if wr.LogID == 0 {
		return nil, fmt.Errorf("loco: send response has no logId")
	}
	return wr, nil
}
