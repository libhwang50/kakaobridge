// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import "testing"

// The three shapes below are copied from real type-0 chat logs in a Regular Chat, a Team Chat and an
// Open Chat room; the nicknames are the ones the account has, the numbers are its own.
func TestFeedShapesAreRead(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		feedType   int
		inviter    int64
		namedCount int
		eventType  int
	}{
		{
			name:       "invitations",
			text:       `{"inviter":{"userId":447137810,"nickName":"someone"},"members":[{"userId":172850882,"nickName":"a"},{"userId":211558139,"nickName":"b"}],"byLink":false,"feedType":1}`,
			feedType:   1,
			inviter:    447137810,
			namedCount: 2,
		},
		{
			name:       "a member arriving",
			text:       `{"feedType":4,"members":[{"userId":447137810,"nickName":"someone"}]}`,
			feedType:   4,
			namedCount: 1,
		},
		{
			name:       "a team event",
			text:       `{"eventType":1,"eventUser":{"userId":447137810,"nickName":"someone"},"feedType":18}`,
			feedType:   18,
			namedCount: 1,
			eventType:  1,
		},
	}
	for _, tc := range cases {
		cl := ChatLog{Type: MsgFeed, Text: tc.text}
		fm, ok := FeedMetaOf(cl)
		if !ok {
			t.Fatalf("%s: not recognised as a room event", tc.name)
		}
		if fm.FeedType != tc.feedType {
			t.Errorf("%s: feedType = %d, want %d", tc.name, fm.FeedType, tc.feedType)
		}
		if fm.EventType != tc.eventType {
			t.Errorf("%s: eventType = %d, want %d", tc.name, fm.EventType, tc.eventType)
		}
		if tc.inviter != 0 {
			if fm.Inviter == nil || fm.Inviter.UserID != tc.inviter {
				t.Errorf("%s: inviter = %+v, want userId %d", tc.name, fm.Inviter, tc.inviter)
			}
		}
		if named := fm.Named(); len(named) != tc.namedCount {
			t.Errorf("%s: Named() = %d people, want %d", tc.name, len(named), tc.namedCount)
		}
	}
}

// An invitation names the people who arrived, not the person who invited them: their membership was
// already there, and an invite would be bridged as a pointless join.
func TestFeedNamedSkipsTheInviter(t *testing.T) {
	fm, ok := FeedMetaOf(ChatLog{Type: MsgFeed, Text: `{"inviter":{"userId":7,"nickName":"host"},"members":[{"userId":8,"nickName":"guest"}],"feedType":1}`})
	if !ok {
		t.Fatal("not a feed")
	}
	named := fm.Named()
	if len(named) != 1 || named[0].UserID != 8 {
		t.Errorf("Named() = %+v, want only the invited member", named)
	}
}

// Anything that is not a feed object stays unrecognised, so an unexpected body is logged instead of
// being mistaken for a membership change.
func TestFeedNeedsAFeedType(t *testing.T) {
	for _, text := range []string{
		`{"members":[{"userId":8}]}`, // a room event without its type is not actionable
		`plain text`,                 // ordinary chat text
		``,                           // nothing at all
		`{"feedType":"one"}`,         // wrong type for the field
	} {
		if fm, ok := FeedMetaOf(ChatLog{Type: MsgFeed, Text: text}); ok {
			t.Errorf("%q read as a room event: %+v", text, fm)
		}
	}
	// A text message whose body happens to look like a feed is still a text message.
	if _, ok := FeedMetaOf(ChatLog{Type: MsgText, Text: `{"feedType":1,"members":[{"userId":8}]}`}); ok {
		t.Error("a type-1 log is chat text whatever its body says")
	}
}
