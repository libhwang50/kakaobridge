// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"encoding/json"
)

// FeedMeta is the body of a chat log whose type is MsgFeed (0): not something someone typed, but an
// event about the room itself. The client draws these as a small centred line in the reader's own
// language ("X invited Y"), so there is nothing language-neutral to bridge; the useful part is that the
// room's state has changed and should be re-read.
//
// Three shapes, all measured on the bridge's own device (a Regular Chat, a Team Chat and an Open Chat
// room, 2026-11-03):
//
//	{"feedType":1,"inviter":{…},"members":[…],"byLink":false}          members were invited
//	{"feedType":2,"member":{…},"kicked":true,"hidden":false}          a member left; kicked says whether
//	                                                                  they were removed
//	{"feedType":4,"members":[…]}                                      a member arrived (open chat room)
//	{"feedType":18,"eventType":1,"eventUser":{…}}                     a team-chat event
//
// The invitation and the arrival say the same thing in different shapes, and the team event says
// something we have not identified. What is certain is that the member list changed, or that the room
// itself did. The first and the third also reach the device as NEWMEM pushes, the second as DELMEM
// ("roominfo.go" ChatIDOfPush), so a change is not only visible in history.
type FeedMeta struct {
	FeedType  int        `json:"feedType"`
	ByLink    bool       `json:"byLink"`
	EventType int        `json:"eventType"`
	Inviter   *FeedUser  `json:"inviter"`
	EventUser *FeedUser  `json:"eventUser"`
	Members   []FeedUser `json:"members"`
	// Member names the one person a notice is about, which is how a departure is described. Kicked says
	// whether they were removed by someone else rather than left on their own.
	Member *FeedUser `json:"member"`
	Kicked *bool     `json:"kicked"`
	Hidden *bool     `json:"hidden"`
}

// FeedUser is one person named by a feed message. The nickname is the room-visible one, which is not
// always the contact name.
type FeedUser struct {
	UserID   int64  `json:"userId"`
	Nickname string `json:"nickName"`
}

// FeedMetaOf decodes a feed body. It says false when the text is not a feed object - no feedType key,
// or not JSON at all - so an unmeasured shape stays visible in the logs instead of being mistaken for a
// membership change.
func FeedMetaOf(cl ChatLog) (*FeedMeta, bool) {
	if cl.Type != MsgFeed || cl.Text == "" {
		return nil, false
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cl.Text), &keys); err != nil {
		return nil, false
	}
	if _, ok := keys["feedType"]; !ok {
		return nil, false
	}
	var fm FeedMeta
	if err := json.Unmarshal([]byte(cl.Text), &fm); err != nil {
		return nil, false
	}
	return &fm, true
}

// Named reports the people a feed message is about: the ones invited or arriving, or the single member a
// team event names. It never includes the person who did the inviting, whose arrival was already known.
func (fm *FeedMeta) Named() []FeedUser {
	if len(fm.Members) > 0 {
		return fm.Members
	}
	if fm.Member != nil {
		return []FeedUser{*fm.Member}
	}
	if fm.EventUser != nil {
		return []FeedUser{*fm.EventUser}
	}
	return nil
}
