// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"fmt"
	"unicode"
)

// The LOGINLIST response carries the caller's own room list - which is why the bridge does not need
// the mobile "drawer" REST API (see docs/protocol-notes.md §1 and §6) to know its sessions.
//
// Verified shape (docs/protocol-notes.md §6):
//
//	{status, userId, revision, revisionInfo, rp, minLogId, sb, chatDatas[], delChatIds[], kc,
//	 mcmRevision, lastTokenId, lastChatId, ltk, lbk, eof}
//
// Each chatDatas entry is a *nested* document whose keys come with a leading type marker byte
// (LOCO's inner structures are compressed BSON: the single-char names come from a shared key
// dictionary, so the wire shows e.g. 0x12 'c' for an int64 field named "c"). findKey below matches
// tolerantly so the bridge does not have to know that dictionary.

// ChatData is one room as the server describes it at login.
type ChatData struct {
	ChatID   int64   `json:"chatId"` // "c"
	Type     string  `json:"type"`   // "t": "MemoChat" | "DirectChat" | "OpenChat..."
	Members  []int64 `json:"memberIds,omitempty"`
	LastLog  int64   `json:"lastLogId,omitempty"` // "s": newest logId in the room
	LastType int     `json:"lastMsgType,omitempty"`
	Preview  string  `json:"preview,omitempty"` // "l".message
}

// RoomState is what a LOGINLIST response contributes to a session.
type RoomState struct {
	Rooms       []ChatData
	Deleted     []int64
	LastTokenID int64
	MinLogID    int64
	EOF         bool
}

// findKey looks a field up in a nested document, tolerating the compressed-BSON marker byte.
func findKey(elems []Elem, want string) any {
	for _, e := range elems {
		if e.Key == want || stripMarker(e.Key) == want {
			return e.Val
		}
	}
	return nil
}

func stripMarker(k string) string {
	if k == "" {
		return k
	}
	r := []rune(k)[0]
	if !unicode.IsPrint(r) && r != 0 {
		return string([]rune(k)[1:])
	}
	return k
}

func toInt64(v any) int64 { return int64Val(v) }

func elemsOf(v any) []Elem {
	switch x := v.(type) {
	case []Elem:
		return x
	}
	return nil
}

// ParseLoginlistResponse decodes the LOGINLIST response body (a bare BSON document, not a frame).
// The nested key-compression caveat is documented above; nothing here depends on knowing the
// dictionary because findKey matches on the printed name.
func ParseLoginlistResponse(body []byte) (*RoomState, error) {
	doc, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loginlist body: %w", err)
	}
	st := &RoomState{}
	if v := findKey(doc, "lastTokenId"); v != nil {
		st.LastTokenID = toInt64(v)
	}
	if v := findKey(doc, "minLogId"); v != nil {
		st.MinLogID = toInt64(v)
	}
	if v, ok := findKey(doc, "eof").(bool); ok {
		st.EOF = v
	}
	for _, e := range arrayElems(findKey(doc, "delChatIds")) {
		st.Deleted = append(st.Deleted, toInt64(e.Val))
	}
	for _, room := range arrayElems(findKey(doc, "chatDatas")) {
		d := elemsOf(room.Val)
		if len(d) == 0 {
			continue
		}
		var c ChatData
		c.ChatID = toInt64(findKey(d, "c"))
		if s, ok := findKey(d, "t").(string); ok {
			c.Type = s
		}
		c.LastLog = toInt64(findKey(d, "s"))
		for _, m := range arrayElems(findKey(d, "r")) {
			c.Members = append(c.Members, toInt64(m.Val))
		}
		if l := elemsOf(findKey(d, "l")); len(l) > 0 {
			if s, ok := findKey(l, "message").(string); ok {
				c.Preview = s
			}
			c.LastType = int(int32Val(findKey(l, "type")))
		}
		if c.ChatID != 0 {
			st.Rooms = append(st.Rooms, c)
		}
	}
	return st, nil
}

// arrayElems accepts both a BSON array (numeric keys) and a BSON doc encoded as []Elem.
func arrayElems(v any) []Elem {
	switch x := v.(type) {
	case []Elem:
		return x
	}
	return nil
}

// ChatIDOfPush finds which chat a server push refers to. Not every verb spells the field the same way -
// some use the shared key dictionary ("c"), others spell it out ("chatId") - and some wrap it in a
// nested document, so the lookup goes one level down. A push that names no chat is reported as such:
// guessing a chat id would put the change in the wrong room.
func ChatIDOfPush(body []byte) (int64, bool) {
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return 0, false
	}
	if id, ok := chatIDIn(elems); ok {
		return id, true
	}
	for _, e := range elems {
		if nested, ok := e.Val.([]Elem); ok {
			if id, ok := chatIDIn(nested); ok {
				return id, true
			}
		}
	}
	return 0, false
}

func chatIDIn(elems []Elem) (int64, bool) {
	for _, key := range []string{"chatId", "chatid", "c"} {
		if id := int64Val(findKey(elems, key)); id != 0 {
			return id, true
		}
	}
	return 0, false
}

// LeftPush is a LEFT push: one chat the server has just taken out of this account's list. The shape read
// off the bridge's own session:
//
//	{chatId, lastTokenId, serviceId, status}
//
// There is no user id in it, because it is not about another member: this is the live twin of
// delChatIds. Two were read from one session - one 90 ms after the room notice that came with deleting a
// Team Chat, and one two minutes after a group chat this account had left, while the room itself still
// exists for whoever is in it (docs/protocol-notes.md §6).
type LeftPush struct {
	ChatID      int64 `json:"chatId"`
	LastTokenID int64 `json:"lastTokenId"`
}

func ParseLeftPush(body []byte) (*LeftPush, error) {
	if inner := InnerStatus(body); inner != 0 {
		return nil, fmt.Errorf("loco: %w", &InnerError{Verb: "LEFT", Status: inner})
	}
	elems, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding LEFT push: %w", err)
	}
	id, ok := chatIDIn(elems)
	if !ok {
		return nil, fmt.Errorf("loco: LEFT push names no chat")
	}
	out := &LeftPush{ChatID: id}
	if v := findKey(elems, "lastTokenId"); v != nil {
		out.LastTokenID = toInt64(v)
	}
	return out, nil
}
