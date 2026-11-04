// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

// A Team Chat describes itself in two places. Its title and picture are on the LOCO socket (chatMeta
// types 3 and 4, see docs/protocol-notes.md §7a), but the intro the client calls "About team" is not:
// CHATINFO, ChatOnRoom and GETMEM were read for a room whose intro was set, and none of them carried
// the text. The client asks a second service for it instead, which its own code names alcatraz - a POST
// with {"chatId":…} to the path below answers with the name, the intro, the host and the room's roles,
// and the client writes an edit to the sibling update path. A chat that is not a Team Chat answers
// 404/code 90001, so that is how the two are told apart here rather than by guessing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// teamBase is the gateway the shipped clients use, the same host as the media APIs (see pilsnerBase).
// A var only so a test can point it at a local server.
var teamBase = pilsnerBase

// ErrNotTeamChat is the answer for a chat that has no team profile - a plain group chat or an Open Chat
// room. It is a real measurement rather than a timeout: the gateway says the request is invalid.
var ErrNotTeamChat = errors.New("kakao: this chat has no team profile")

// TeamChat is a Team Chat's own profile. The fields are the ones the service returns; Permission holds
// the roles the room enforces (measured on a test room: INVITE: STAFF, WRITE: MEMBER, BOT: MEMBER) and
// Staff names its admins. Nothing here is inferred: a field is present because it was read.
type TeamChat struct {
	ChatID       int64             `json:"chatId"`
	Name         string            `json:"name"`
	Description  string            `json:"description"`
	HostID       string            `json:"hostId"`
	Status       string            `json:"status"`
	Revision     int64             `json:"revision"`
	CreatedAt    int64             `json:"createdAt"`
	Permission   map[string]string `json:"permission"`
	Staff        []string          `json:"staff"`
	Restrictions []string          `json:"restrictions"`
}

// TeamChat asks the team service what it knows about one chat. Only Team Chats answer.
func (c *Client) TeamChat(ctx context.Context, chatID int64) (*TeamChat, error) {
	body := `{"chatId":` + strconv.FormatInt(chatID, 10) + `}`
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, teamBase+"alcatraz/v1/chat/info", strings.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("kakao: asking for a team profile: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	c.SetAuth(req)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kakao: asking for a team profile: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("kakao: reading a team profile: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("%w: %s", ErrNotTeamChat, trim(string(data), 120))
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kakao: team profile for chat %d: http %d: %s", chatID, resp.StatusCode, trim(string(data), 200))
	}
	var t TeamChat
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("kakao: decoding a team profile: %w (%s)", err, trim(string(data), 160))
	}
	return &t, nil
}
