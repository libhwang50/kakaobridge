// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

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
// account itself has nothing to compose: naming nothing is the honest answer, and the open-chat REST
// service is where the phone gets the real one.
func TestGroupWithNothingToSayAboutItselfNamesNothing(t *testing.T) {
	name, avatar := groupMeta(groupSummary(), groupMembers(groupSelfID, "Me"), groupSelfID)
	if name != "" || avatar != "" {
		t.Errorf("name/avatar = %q/%q, want nothing", name, avatar)
	}
}
