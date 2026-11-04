// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"
	"time"

	"maunium.net/go/mautrix/bridgev2"

	"github.com/libhwang50/kakaobridge/kakao"
	"github.com/libhwang50/kakaobridge/loco"
)

const groupSelfID = int64(447137810)

func groupSummary(metas ...loco.ChatMeta) *loco.ChatSummary {
	return &loco.ChatSummary{Type: "MultiChat", Metas: metas}
}

func groupMembers(idsAndNames ...any) []loco.Member {
	var out []loco.Member
	for i := 0; i+1 < len(idsAndNames); i += 2 {
		out = append(out, loco.Member{
			UserID:   int64(idsAndNames[i].(int64)),
			Nickname: idsAndNames[i+1].(string),
		})
	}
	return out
}

// A named group is named by Kakao, and a Team Chat's picture is its room avatar; both are the client's
// own answers, read from the chat's metas.
func TestGroupNameAndPictureComeFromTheChatMetas(t *testing.T) {
	summary := groupSummary(
		loco.ChatMeta{Type: 3, Content: "BRIDGE TEST"},
		loco.ChatMeta{Type: 4,
			Content: `{"imageUrl":"https://cdn/small.jpg","fullImageUrl":"https://cdn/full.jpg"}`},
		loco.ChatMeta{Type: 15, Content: `{"hostId": "447137810"}`},
	)
	name, avatar := groupMeta(summary, groupMembers(int64(1), "Hajin"), groupSelfID)
	if name != "BRIDGE TEST" {
		t.Errorf("name = %q, want the title the room was given", name)
	}
	if avatar != "https://cdn/full.jpg" {
		t.Errorf("avatar = %q, want the full-size room picture", avatar)
	}
}

// A group created without a name has no title meta, and the phone titles it from its members - so the
// bridge does the same rather than leaving the room blank. The account's own name is not part of it.
func TestUnnamedGroupIsTitledByItsMembers(t *testing.T) {
	members := groupMembers(
		groupSelfID, "Me",
		int64(1), "Hajin",
		int64(2), "Jian",
	)
	name, avatar := groupMeta(groupSummary(), members, groupSelfID)
	if name != "Hajin, Jian" {
		t.Errorf("name = %q, want the other members", name)
	}
	if avatar != "" {
		t.Errorf("avatar = %q, a plain group has no picture", avatar)
	}

	big := groupMembers(int64(1), "a", int64(2), "b", int64(3), "c", int64(4), "d")
	if name, _ := groupMeta(groupSummary(), big, groupSelfID); name != "a, b, c (+1)" {
		t.Errorf("name = %q, want a short list with the rest counted", name)
	}
}

// An open chat room (type OM) carries no title or picture in LOCO, and a room whose only member is the
// account itself has nothing to compose: naming nothing is the honest answer. The phone does show a name
// for such a room, but it reads it from an open-chat service the bridge has not measured yet, so the
// room is left unnamed rather than given a made-up one.
func TestGroupWithNothingToSayAboutItselfNamesNothing(t *testing.T) {
	name, avatar := groupMeta(groupSummary(), groupMembers(groupSelfID, "Me"), groupSelfID)
	if name != "" || avatar != "" {
		t.Errorf("name/avatar = %q/%q, want nothing", name, avatar)
	}
}

// A Team Chat's intro is not on the LOCO socket, so it arrives from the team service and is put where
// Matrix shows a room's own description. The name from chatMeta keeps winning when both sources have
// one; the team answer only fills a gap.
func TestTeamIntroBecomesTheRoomTopic(t *testing.T) {
	name := "named by the chat itself"
	info := &bridgev2.ChatInfo{Name: &name}
	applyTeamInfo(info, &kakao.TeamChat{Name: "named by the team service", Description: "what this room is for"})
	if info.Topic == nil || *info.Topic != "what this room is for" {
		t.Errorf("topic = %v, want the room's own intro", info.Topic)
	}
	if info.Name == nil || *info.Name != name {
		t.Errorf("name = %v, want the chat's own title to win", info.Name)
	}

	unnamed := &bridgev2.ChatInfo{}
	applyTeamInfo(unnamed, &kakao.TeamChat{Name: "a team room"})
	if unnamed.Name == nil || *unnamed.Name != "a team room" {
		t.Errorf("name = %v, want the team service to fill a room with no title", unnamed.Name)
	}
	if unnamed.Topic != nil {
		t.Errorf("topic = %v, want nothing for a room with no intro", unnamed.Topic)
	}

	applyTeamInfo(unnamed, nil)
}

// A room notice replayed from history is not a change happening now; only the live one is worth a
// round trip.
func TestOnlyALiveRoomNoticeIsNews(t *testing.T) {
	now := time.Unix(1793700000, 0)
	if !feedIsNews(now.Unix()-10, now) {
		t.Error("a notice from seconds ago is news")
	}
	if feedIsNews(now.Unix()-3600, now) {
		t.Error("an hour-old notice is history, and the import reads the room anyway")
	}
}
