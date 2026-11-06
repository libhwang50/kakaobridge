// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

// A reaction is the one part of a message that is neither in the chat log nor pushed over LOCO. It
// lives on the messaging gateway next to media, keyed by the message's log id. Both read calls are
// measured with this bridge's own credential (docs/protocol-notes.md §12):
//
//	GET /emoticon/chat/rx/logmetas?chatId=&minLogId=&maxLogId=&order=&limit=
//	GET /emoticon/chat/rx/log-details?chatId=[&linkId=]&logId=
//
// The first says which messages inside a log-id range carry reactions and how many of each kind, the
// second says who put them there. `linkId` is left out for an ordinary chat and is required for an
// Open Chat room: the same call without it answers {"status":-400} there. `limit` is not optional on
// the range call: the same range without it answers {"status":-500}.
//
// Nothing here writes. The write call (chat/rx/do) is on this gateway too, but its body has not been
// measured, so the bridge does not guess it and does not react on Kakao's behalf yet.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// quotedInt64 is an id the gateway spells as a JSON string ("3965989416160335874"). It accepts a bare
// number too, so a change in that spelling cannot make a reaction silently disappear.
type quotedInt64 int64

func (q *quotedInt64) UnmarshalJSON(data []byte) error {
	s := string(data)
	if len(s) >= 2 && s[0] == '"' {
		s = s[1 : len(s)-1]
	}
	if s == "" || s == "null" {
		*q = 0
		return nil
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("kakao: %q is not an id: %w", s, err)
	}
	*q = quotedInt64(n)
	return nil
}

// ReactionItem is one reaction. `k` is the kind: the shipped client spells them out as -1 unknown, 1
// reaction, 2 mini emoticon, and only 2 has been seen on the wire — what the reaction picker offers is
// its mini-emoticon set, which is why an item is a picture with a name rather than a character. `o`
// names the item inside that set ("1200509_029"), `a` is the alt text the client reads out for a
// screen reader, and `c` is how many people chose it.
type ReactionItem struct {
	Kind  int               `json:"k"`
	Item  string            `json:"o"`
	Count int               `json:"c,omitempty"`
	Alt   map[string]string `json:"a,omitempty"`
}

// MessageReactions is one message's reaction state as logmetas describes it. Revision changes with
// every reaction added or removed, which is how the bridge can tell a state it has already applied
// from a new one without asking who reacted.
type MessageReactions struct {
	LogID     quotedInt64    `json:"logId"`
	Revision  quotedInt64    `json:"revision"`
	Reactions []ReactionItem `json:"-"`
	Mine      []ReactionItem `json:"-"`
}

type logMetaWire struct {
	LogID    quotedInt64 `json:"logId"`
	Revision quotedInt64 `json:"revision"`
	Content  string      `json:"content"`
	Extra    string      `json:"extra"`
}

// Reactor is one kind of reaction and the people behind it. In an Open Chat room the ids here are the
// room's own participant ids, the same ones a message from that person is attributed to.
type Reactor struct {
	ReactionItem
	Users []quotedInt64 `json:"u"`
}

// RxMetaPage names the messages whose reactions to ask about. The bounds are log ids, which is what lets
// one call cover one page of history. NewestFirst with a Limit is how a live room is asked for its most
// recently changed states instead of its oldest.
type RxMetaPage struct {
	ChatID      int64
	MinLogID    int64
	MaxLogID    int64
	NewestFirst bool
	Limit       int
}

// defaultMetaLimit is used when a page names no limit, because the gateway refuses the call without one:
// the same range with limit=50 answers, and without it answers {"status":-500}. A thousand is accepted,
// so the number is a working bound rather than a server maximum.
const defaultMetaLimit = 100

// RxLogMetas reads the reaction state of every message in that range.
func (c *Client) RxLogMetas(ctx context.Context, page RxMetaPage) ([]MessageReactions, error) {
	order := "asc"
	if page.NewestFirst {
		order = "desc"
	}
	limit := page.Limit
	if limit <= 0 {
		limit = defaultMetaLimit
	}
	q := url.Values{
		"chatId":   {strconv.FormatInt(page.ChatID, 10)},
		"minLogId": {strconv.FormatInt(page.MinLogID, 10)},
		"maxLogId": {strconv.FormatInt(page.MaxLogID, 10)},
		"order":    {order},
		"limit":    {strconv.Itoa(limit)},
	}
	var out struct {
		LogMetas []logMetaWire `json:"logmetas"`
	}
	if err := c.getEmoticon(ctx, "emoticon/chat/rx/logmetas", q, &out); err != nil {
		return nil, err
	}
	metas := make([]MessageReactions, 0, len(out.LogMetas))
	for _, w := range out.LogMetas {
		m := MessageReactions{LogID: w.LogID, Revision: w.Revision}
		// The two halves are strings of JSON, not objects: the counts live in `content` and the
		// caller's own reactions in `extra`. An empty `rx` list is a real answer - it says the
		// reactions that were there are gone.
		if w.Content != "" {
			var body struct {
				Rx []ReactionItem `json:"rx"`
			}
			if err := json.Unmarshal([]byte(w.Content), &body); err != nil {
				return nil, fmt.Errorf("kakao: reading the reactions of message %d: %w", int64(w.LogID), err)
			}
			m.Reactions = body.Rx
		}
		if w.Extra != "" {
			var body struct {
				MyRx []ReactionItem `json:"myRx"`
			}
			if err := json.Unmarshal([]byte(w.Extra), &body); err != nil {
				return nil, fmt.Errorf("kakao: reading our own reactions on message %d: %w", int64(w.LogID), err)
			}
			m.Mine = body.MyRx
		}
		metas = append(metas, m)
	}
	return metas, nil
}

// RxLogDetails asks who reacted to one message. linkID is the Open Link of an Open Chat room and is
// left out (0) for any other chat, which is how the client sends it.
func (c *Client) RxLogDetails(ctx context.Context, chatID, linkID, logID int64) ([]Reactor, error) {
	q := url.Values{
		"chatId": {strconv.FormatInt(chatID, 10)},
		"logId":  {strconv.FormatInt(logID, 10)},
	}
	if linkID != 0 {
		q.Set("linkId", strconv.FormatInt(linkID, 10))
	}
	var out struct {
		Details []Reactor `json:"details"`
	}
	if err := c.getEmoticon(ctx, "emoticon/chat/rx/log-details", q, &out); err != nil {
		return nil, err
	}
	return out.Details, nil
}

// getEmoticon calls the reaction API on the same gateway as the media API, with the same headers that
// call needs. A refusal is a Kakao status, and it can arrive with an HTTP 400, so the status is read
// out of the body first.
func (c *Client) getEmoticon(ctx context.Context, path string, q url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pilsnerBase+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	req.Header.Set("talk-agent", c.A.Agent+"/"+c.A.Version)
	req.Header.Set("talk-language", c.A.Lang)
	c.SetAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("kakao: asking for reactions: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("kakao: reading the reaction answer: %w", err)
	}
	var refusal struct {
		Status  int    `json:"status"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(data, &refusal); err != nil {
		return fmt.Errorf("kakao: the reaction answer is not JSON: %s", trim(string(data), 160))
	}
	if refusal.Status != 0 {
		return fmt.Errorf("kakao: %s refused: status %d (%s)", path, refusal.Status, trim(refusal.Message, 120))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kakao: %s: http %d: %s", path, resp.StatusCode, trim(string(data), 160))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("kakao: reading %s: %w", path, err)
	}
	return nil
}
