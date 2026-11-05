// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"strings"
	"testing"
)

// MEMBER answers about a room rather than about a person, and the two shapes measured here are what that
// means in practice: an ordinary chat spells the profile fields out, while an Open Chat room answers the
// same user id with the open profile it shows, under the short keys. Synthetic names and ids; the key
// spellings are the fact being pinned.
func TestAMemberAnswerIsAboutARoom(t *testing.T) {
	group := EncodeDoc([]Elem{
		{Key: "status", Type: tInt32, Val: int32(0)},
		{Key: "chatId", Type: tInt64, Val: int64(484107646109432)},
		{Key: "members", Type: tArr, Val: []Elem{
			// The server does not keep the requested order.
			{Type: tDoc, Val: []Elem{
				{Key: "userId", Type: tInt64, Val: int64(211558139)},
				{Key: "nickName", Type: tString, Val: "a friend"},
				{Key: "profileImageUrl", Type: tString, Val: "https://cdn.example/thumb.jpg"},
				{Key: "fullProfileImageUrl", Type: tString, Val: "https://cdn.example/full.jpg"},
				{Key: "suspended", Type: tBool, Val: false},
			}},
			{Type: tDoc, Val: []Elem{
				{Key: "userId", Type: tInt64, Val: int64(172850882)},
				{Key: "nickName", Type: tString, Val: "default picture"},
				{Key: "profileImageUrl", Type: tString, Val: ""},
				{Key: "fullProfileImageUrl", Type: tString, Val: ""},
			}},
		}},
	})
	members, err := ParseMemberResponse(group)
	if err != nil {
		t.Fatalf("a group chat's answer must parse: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want both", len(members))
	}
	byID := map[int64]Member{}
	for _, m := range members {
		byID[m.UserID] = m
	}
	if byID[211558139].Nickname != "a friend" ||
		byID[211558139].ProfileImageURL != "https://cdn.example/thumb.jpg" ||
		byID[211558139].FullProfileImageURL != "https://cdn.example/full.jpg" {
		t.Errorf("the picture fields must be read in the long spelling: %+v", byID[211558139])
	}
	// An account with the default avatar answers with empty image fields. That is normal, not a failure.
	if byID[172850882].Nickname != "default picture" || byID[172850882].ProfileImageURL != "" {
		t.Errorf("a default avatar must stay empty rather than become a guess: %+v", byID[172850882])
	}

	open := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: int64(18498428165535132)},
		{Key: "members", Type: tArr, Val: []Elem{{Type: tDoc, Val: []Elem{
			{Key: "userId", Type: tInt64, Val: int64(447137810)},
			{Key: "nickName", Type: tString, Val: "an open name"},
			{Key: "pi", Type: tString, Val: "https://open.example/img_s.jpg"},
			{Key: "fpi", Type: tString, Val: "https://open.example/img_l.jpg"},
			{Key: "opi", Type: tString, Val: "https://open.example/img.jpg"},
			{Key: "mt", Type: tInt32, Val: int32(1)},
			{Key: "ptp", Type: tInt32, Val: int32(16)},
		}}}},
		{Key: "status", Type: tInt32, Val: int32(0)},
	})
	members, err = ParseMemberResponse(open)
	if err != nil {
		t.Fatalf("an Open Chat room's answer must parse: %v", err)
	}
	if len(members) != 1 {
		t.Fatalf("got %d members, want one", len(members))
	}
	m := members[0]
	if m.Nickname != "an open name" ||
		m.ProfileImageURL != "https://open.example/img_s.jpg" ||
		m.FullProfileImageURL != "https://open.example/img_l.jpg" {
		t.Errorf("the same user id in an Open Chat room answers with that room's open profile: %+v", m)
	}
}

func TestAMemberAnswerWithNoOneInIt(t *testing.T) {
	none := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: int64(1)},
		{Key: "status", Type: tInt32, Val: int32(0)},
	})
	if members, err := ParseMemberResponse(none); err != nil || len(members) != 0 {
		t.Errorf("an answer that names nobody is empty, not an error: %v %v", members, err)
	}
	// A chat this credential is not part of answers with an inner error rather than a member list.
	bad := EncodeDoc([]Elem{
		{Key: "status", Type: tInt32, Val: int32(-203)},
		{Key: "errMsg", Type: tString, Val: ""},
	})
	_, err := ParseMemberResponse(bad)
	if err == nil || !strings.Contains(err.Error(), "MEMBER") {
		t.Errorf("an inner error must be named by the verb it came from, got %v", err)
	}
}
