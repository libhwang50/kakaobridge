// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The bodies under testdata/ are real responses captured from the bridge's own registered mobile
// device (os=android, appVer 26.9.1) with cmd/locoprobe, e.g.
//
//	cmd/locoprobe -tokens tokens.json -- \
//	    'CHATINFO chatId:i64=482943465622166' 'GETMEM chatId:i64=482943465622166'
//
// They pin the field names these parsers depend on: c/a/l for ChatOnRoom, chatInfo/displayMembers
// for CHATINFO, members/nickName for GETMEM, chatLogs/eof for MCHATLOGS. A parser that silently
// reads the wrong key would otherwise only show up as rooms with no members.
func body(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if errors.Is(err, os.ErrNotExist) {
		// These captures are traffic from a personal account (real names, chat ids, message text), so
		// they are not published with the bridge. Put a capture of your own in loco/testdata/ to run
		// these; testdata/README.md says how to make one.
		t.Skipf("no capture for %s", name)
	}
	if err != nil {
		t.Fatalf("read testdata: %v", err)
	}
	b, err := hex.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil {
		t.Fatalf("%s is not hex: %v", name, err)
	}
	return b
}

const (
	testChatID  = int64(482943465622166)
	testOtherID = int64(172850882)
	testSelfID  = int64(447137810)
)

func TestParseChatOnRoomResponse(t *testing.T) {
	ri, err := ParseChatOnRoomResponse(body(t, "chatonroom_direct.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ri.ChatID != testChatID || ri.Type != "DirectChat" {
		t.Errorf("got chat %d type %q", ri.ChatID, ri.Type)
	}
	if len(ri.Members) != 2 || ri.Members[0] != testOtherID || ri.Members[1] != testSelfID {
		t.Errorf("members = %v, want [%d %d]", ri.Members, testOtherID, testSelfID)
	}
	if ri.LastLogID == 0 || ri.Watermark == 0 {
		t.Errorf("cursors missing: lastLog=%d watermark=%d", ri.LastLogID, ri.Watermark)
	}
}

func TestParseChatInfoResponse(t *testing.T) {
	cs, err := ParseChatInfoResponse(body(t, "chatinfo_direct.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cs.ChatID != testChatID || cs.Type != "DirectChat" || cs.ActiveMember != 2 {
		t.Errorf("got chat %d type %q members %d", cs.ChatID, cs.Type, cs.ActiveMember)
	}
	if cs.LastLogID == 0 {
		t.Error("lastLogId missing; a room cannot be seeded or ordered without it")
	}
	// For a 1:1 chat displayMembers lists the *other* side, with the name a room title would need.
	if len(cs.DisplayMembers) != 1 || cs.DisplayMembers[0].UserID != testOtherID {
		t.Fatalf("displayMembers = %+v", cs.DisplayMembers)
	}
	if cs.DisplayMembers[0].Nickname == "" {
		t.Error("displayMember has no nickname; ghost names come from this field")
	}
	// A friend chat carries no lastChatLog in CHATINFO (plus chats do). Nothing should be invented.
	if cs.LastChatLog != nil {
		t.Errorf("unexpected lastChatLog for a DirectChat: %+v", cs.LastChatLog)
	}
}

func TestParseGetMemResponse(t *testing.T) {
	members, err := ParseGetMemResponse(body(t, "getmem_direct.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(members) != 2 {
		t.Fatalf("got %d members, want 2", len(members))
	}
	byID := map[int64]Member{}
	for _, m := range members {
		byID[m.UserID] = m
	}
	for _, id := range []int64{testOtherID, testSelfID} {
		m, ok := byID[id]
		if !ok {
			t.Fatalf("member %d missing from %v", id, members)
		}
		if m.Nickname == "" {
			t.Errorf("member %d has no nickname", id)
		}
	}
}

func TestParseChatLogs(t *testing.T) {
	logs, eof, err := ParseChatLogs(body(t, "mchatlogs_direct.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !eof {
		t.Error("eof should be true for this body; paging logic depends on it being read")
	}
	if len(logs) != 2 {
		t.Fatalf("got %d logs, want 2", len(logs))
	}
	for _, cl := range logs {
		if cl.ChatID != testChatID || cl.LogID == 0 || cl.AuthorID == 0 || cl.SendAt == 0 {
			t.Errorf("incomplete chat log: %+v", cl)
		}
		if cl.Type != MsgText || cl.Text == "" {
			t.Errorf("unexpected message type/content: type=%d text=%q", cl.Type, cl.Text)
		}
	}
	if logs[0].LogID >= logs[1].LogID {
		t.Errorf("server returned them out of order: %d then %d", logs[0].LogID, logs[1].LogID)
	}
	if logs[1].PrevID != logs[0].LogID {
		t.Errorf("prevId chain not read: %d != %d", logs[1].PrevID, logs[0].LogID)
	}
}

// The history verb gives nothing for a plus chat even though that chat has messages; the parsers must
// report "no logs, eof" rather than an error, because the caller falls back to CHATINFO's last
// message in that case.
func TestParseEmptyChatLogsIsNotAnError(t *testing.T) {
	logs, eof, err := ParseChatLogs(EncodeDoc([]Elem{{Key: "status", Type: 0x10, Val: int32(0)}, {Key: "eof", Type: 0x08, Val: true}}))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(logs) != 0 || !eof {
		t.Errorf("got %d logs eof=%v", len(logs), eof)
	}
}

func TestParseWriteResponse(t *testing.T) {
	wr, err := ParseWriteResponse(body(t, "write_response.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if wr.ChatID != 482943007629834 || wr.LogID == 0 || wr.SendTime == 0 {
		t.Errorf("incomplete write result: %+v", wr)
	}
	if wr.MsgID == 0 {
		t.Error("the client msgId should come back; it is how a write is matched to a Matrix event")
	}
	if _, err := ParseWriteResponse(EncodeDoc([]Elem{{Key: "status", Type: 0x10, Val: int32(-100)}})); err == nil {
		t.Error("a rejected WRITE must surface as an error, not an empty result")
	}
}

// The self-chat history in testdata contains a real photo and a real file, which is what pins the
// attachment field names (url/mt/w/h/s/rsc/name) and the fact that downloads are plain signed HTTPS.
func TestAttachmentMetaFromRealMessages(t *testing.T) {
	logs, _, err := ParseChatLogs(body(t, "mchatlogs_memo.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var photo, file *ChatLog
	for i := range logs {
		switch logs[i].Type {
		case MsgPhoto:
			photo = &logs[i]
		case MsgFile:
			file = &logs[i]
		case MsgText:
			if m := logs[i].AttachmentMetaOf(); m != nil {
				t.Errorf("a text message must not look like media: %+v", m)
			}
		}
	}
	if photo == nil || file == nil {
		t.Fatalf("expected a photo and a file in this capture, got types %v", typesOf(logs))
	}
	pm := photo.AttachmentMetaOf()
	if pm == nil {
		t.Fatal("photo has no attachment meta")
	}
	if !strings.HasPrefix(pm.DownloadURL(), "https://") {
		t.Errorf("photo download url = %q", pm.DownloadURL())
	}
	if pm.MIME != "image/png" || pm.Width != 1920 || pm.Height != 1080 || pm.Size != 10818 {
		t.Errorf("photo meta wrong: %+v", pm)
	}
	if !strings.HasSuffix(pm.FileName(), ".png") {
		t.Errorf("photo file name = %q; it must come from the resource path", pm.FileName())
	}
	if pm.ThumbnailURL == "" || !strings.Contains(pm.ThumbnailURL, "convert=resize") {
		t.Errorf("photo thumbnail url missing: %q", pm.ThumbnailURL)
	}
	fm := file.AttachmentMetaOf()
	if fm == nil || fm.Name != "conflict.mpg" || fm.Size != 34590724 {
		t.Errorf("file meta wrong: %+v", fm)
	}
}

func typesOf(logs []ChatLog) []int32 {
	out := make([]int32, len(logs))
	for i, l := range logs {
		out[i] = l.Type
	}
	return out
}

// This capture is the same image sent from the Windows client and from the Android client, plus a voice
// note and a video. It pins the parts of the attachment shape that a bridge has to get right: the body
// text is a per-client placeholder, the alt text is a separate field, the units of `d` differ per media
// type, and a video ships two encodings whose URL and resource path do not name the same file.
func TestAttachmentShapesFromBothClients(t *testing.T) {
	logs, eof, err := ParseChatLogs(body(t, "mchatlogs_memo_media.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !eof {
		t.Error("expected eof on a self-chat history")
	}
	byType := map[int32][]ChatLog{}
	for _, l := range logs {
		byType[l.Type] = append(byType[l.Type], l)
	}
	for _, typ := range []int32{MsgPhoto, MsgVoice, MsgVideo, MsgFile} {
		if len(byType[typ]) == 0 {
			t.Fatalf("expected a type %d message in this capture, got %v", typ, typesOf(logs))
		}
	}

	var sawKoreanPlaceholder, sawEnglishPlaceholder, alt, jpeg bool
	for _, p := range byType[MsgPhoto] {
		m := p.AttachmentMetaOf()
		if m == nil {
			t.Fatalf("photo %d has no attachment meta", p.LogID)
		}
		switch p.Text {
		case "사진":
			sawKoreanPlaceholder = true // sent by the Windows client
		case "photo":
			sawEnglishPlaceholder = true // sent by the Android client
		}
		if m.Cmt == "Picture of me skiing with alt text" {
			alt = true
		}
		if m.MIME == "image/jpeg" {
			jpeg = true // the wire says image/jpg; the parser normalises it
		}
	}
	if !sawKoreanPlaceholder || !sawEnglishPlaceholder {
		t.Error("the same image from the two clients must carry different placeholder bodies")
	}
	if !alt {
		t.Error("the alt text (cmt) must be readable")
	}
	if !jpeg {
		t.Error("image/jpg must be normalised to image/jpeg")
	}

	voice := byType[MsgVoice][0].AttachmentMetaOf()
	if voice.DurationMS(MsgVoice) != 7616 {
		t.Errorf("voice duration: want 7616ms (its 19KB of AAC says seconds, not hours), got %d", voice.DurationMS(MsgVoice))
	}
	if got := voice.FileName(); got != "talka_aac.m4a" {
		t.Errorf("voice file name = %q", got)
	}
	if voice.DurationMS(MsgPhoto) != 7616000 {
		t.Error("non-voice media counts seconds, which is what the video shows")
	}

	video := byType[MsgVideo][0].AttachmentMetaOf()
	if video.DurationMS(MsgVideo) != 120000 {
		t.Errorf("video duration: want 120000ms (the clip is 2:01 long), got %d", video.DurationMS(MsgVideo))
	}
	if video.Width != 1280 || video.Height != 720 {
		t.Errorf("video dimensions wrong: %+v", video)
	}
	if got := video.FileName(); got != "talkv_high.mp4" {
		t.Errorf("the file name must describe the URL we fetch, not rsc: got %q", got)
	}
	if video.URLHigh == "" || video.SizeHigh != 40255724 {
		t.Errorf("the second (high-efficiency) encoding must be visible: urlh=%q sh=%d", video.URLHigh, video.SizeHigh)
	}

	file := byType[MsgFile][0].AttachmentMetaOf()
	if got := file.FileName(); got != "conflict.mpg" {
		t.Errorf("an uploaded file keeps its own name: got %q", got)
	}
}

// The numbers and names below are the Android client's own table (defpackage/tp9.java, ChatMessageType),
// cross-checked against messages seen in this account's history. The test exists because earlier guesses
// here were wrong in ways that silently did nothing (27 as "emote" is really a multi-photo post, 71/72
// are the legacy leverage/alimtalk formats, 4 is a contact card).
func TestMessageTypesMatchTheClientTable(t *testing.T) {
	want := map[string]int32{
		"MsgText": 1, "MsgPhoto": 2, "MsgVideo": 3, "MsgContact": 4, "MsgVoice": 5,
		"MsgAnimatedEmot": 6, "MsgDigitalGift": 7, "MsgLink": 9, "MsgEmote": 12,
		"MsgSchedule": 13, "MsgVote": 14, "MsgLocation": 16, "MsgProfile": 17, "MsgFile": 18,
		"MsgEmoteAnimated": 20, "MsgNudge": 21, "MsgPost": 24, "MsgReply": 26,
		"MsgMultiPhoto": 27, "MsgLargeVideo": 28, "MsgLargeFile": 29,
		"MsgLeverage": 71, "MsgAlimtalk": 72, "MsgPlusLeverage": 73,
		"MsgPlus": 81, "MsgPlusEvent": 82, "MsgPlusViral": 83,
	}
	got := map[string]int32{
		"MsgText": MsgText, "MsgPhoto": MsgPhoto, "MsgVideo": MsgVideo, "MsgContact": MsgContact,
		"MsgVoice": MsgVoice, "MsgAnimatedEmot": MsgAnimatedEmot, "MsgDigitalGift": MsgDigitalGift,
		"MsgLink": MsgLink, "MsgEmote": MsgEmote, "MsgSchedule": MsgSchedule, "MsgVote": MsgVote,
		"MsgLocation": MsgLocation, "MsgProfile": MsgProfile, "MsgFile": MsgFile,
		"MsgEmoteAnimated": MsgEmoteAnimated, "MsgNudge": MsgNudge, "MsgPost": MsgPost,
		"MsgReply": MsgReply, "MsgMultiPhoto": MsgMultiPhoto, "MsgLargeVideo": MsgLargeVideo,
		"MsgLargeFile": MsgLargeFile, "MsgLeverage": MsgLeverage, "MsgAlimtalk": MsgAlimtalk,
		"MsgPlusLeverage": MsgPlusLeverage, "MsgPlus": MsgPlus, "MsgPlusEvent": MsgPlusEvent,
		"MsgPlusViral": MsgPlusViral,
		// types that exist only as synthetic feed rows in the client (negative values) are not chat logs
		"IsChannelMessageType(81)": 1, "IsChannelMessageType(1)": 0, "IsChannelMessageType(71)": 1,
	}
	for name, v := range want {
		if got[name] != v {
			t.Errorf("%s = %d, want %d per the client table", name, got[name], v)
		}
	}
	for expr, wantBool := range map[string]int32{"81": 1, "1": 0, "71": 1, "72": 1, "83": 1} {
		var v int32
		if b := IsChannelMessageType(int32(mustAtoi(expr))); b {
			v = 1
		}
		if v != wantBool {
			t.Errorf("IsChannelMessageType(%s) = %v", expr, v == 1)
		}
	}
}

func mustAtoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// Emotes are the one media shape with no URL in the message: they name an item on KakaoTalk's item CDN,
// and the two emoticon types (a static png and an animated webp) differ only by message type.
func TestEmoteItemsResolveToTheItemCDN(t *testing.T) {
	logs, _, err := ParseChatLogs(body(t, "mchatlogs_memo_emotes.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var static, animated *ChatLog
	var inline *ChatLog
	for i := range logs {
		switch logs[i].Type {
		case MsgEmote:
			static = &logs[i]
		case MsgEmoteAnimated:
			animated = &logs[i]
		case MsgText:
			if m := logs[i].AttachmentMetaOf(); m != nil && m.Emojis != nil {
				inline = &logs[i]
			}
		}
	}
	if static == nil || animated == nil || inline == nil {
		t.Fatalf("expected a static emote, an animated emote and a text message with mini emoticons")
	}

	sm := static.AttachmentMetaOf()
	if sm.MIME != "image/png" {
		t.Errorf("static emote mime = %q", sm.MIME)
	}
	if got := sm.ItemURL(); got != itemCDN+"2212560.emot_047.png" {
		t.Errorf("static emote item url = %q", got)
	}
	if got := sm.FileName(); got != "2212560.emot_047.png" {
		t.Errorf("file name must be the item key, not the category label: %q", got)
	}
	if IsAnimatedMessageType(MsgEmote) {
		t.Error("type 12 is the static sticker")
	}

	am := animated.AttachmentMetaOf()
	if am.MIME != "image/webp" {
		t.Errorf("animated emote mime = %q", am.MIME)
	}
	// The three emotes in this capture are 2212560.emot_047.png (static) and 4412207.emot_{048,006}.webp
	// (animated); which item is which is not knowable from the message, only the shape is.
	if !strings.HasPrefix(am.ItemURL(), itemCDN) || !strings.HasSuffix(am.ItemURL(), ".webp") {
		t.Errorf("animated emote item url = %q", am.ItemURL())
	}
	if am.FileName() == "(Emoticons)" {
		t.Error("the category label must never become a file name")
	}
	if !IsAnimatedMessageType(MsgEmoteAnimated) {
		t.Error("type 20 is the animated sticker, and only the type says so")
	}

	im := inline.AttachmentMetaOf()
	if im.Emojis.TotalItem != 2 || len(im.Emojis.Items) != 2 {
		t.Fatalf("mini emoticon descriptor wrong: %+v", im.Emojis)
	}
	if im.Emojis.Items[0].ID != "1200283_027" || im.Emojis.Items[0].Len != 6 {
		t.Errorf("mini emoticon entry wrong: %+v", im.Emojis.Items[0])
	}
	if got := MiniEmojiURL(im.Emojis.Items[0].ID); got != itemCDN+"1200283.emoji_027.png" {
		t.Errorf("mini emoticon url = %q", got)
	}
	if !strings.Contains(inline.Text, "(item)") {
		t.Error("the message body carries the placeholder the descriptor points at")
	}
}

// CHATONROOM's `a` and `w` are positional: watermark i belongs to member i. A real response says both
// sides of a 1:1 chat have read to the newest message, which is exactly what a seeded receipt needs.
func TestChatOnRoomPairsWatermarksWithMembers(t *testing.T) {
	ri, err := ParseChatOnRoomResponse(body(t, "chatonroom_direct.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ri.ReadMarkers) != 2 {
		t.Fatalf("read markers = %+v, want one per member", ri.ReadMarkers)
	}
	for i, want := range ri.Members {
		if ri.ReadMarkers[i].UserID != want {
			t.Errorf("marker %d names user %d, but member %d is %d", i, ri.ReadMarkers[i].UserID, i, want)
		}
		if ri.ReadMarkers[i].LogID == 0 {
			t.Errorf("marker %d has no watermark", i)
		}
	}
	if ri.Watermark < ri.ReadMarkers[0].LogID {
		t.Error("the highest watermark must still be reported for cursors")
	}
}

// testdata/decunread_push.hex is a real DECUNREAD push body, recovered from the memory of the
// running PC client and matching the frame transcript: chatId, userId,
// a watermark that is a logId, a null serviceId, and status 0 as part of the payload.
func TestParseDecUnreadFromACapturedPush(t *testing.T) {
	du, err := ParseDecUnread(body(t, "decunread_push.hex"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	const measuredWatermark = int64(3956272871373338627)
	if du.ChatID != testChatID || du.UserID != testSelfID || du.Watermark != measuredWatermark {
		t.Errorf("got %+v", du)
	}
}

func TestDecUnreadNeedsAWatermark(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: testChatID},
		{Key: "userId", Type: tInt64, Val: testSelfID},
		{Key: "status", Type: tInt32, Val: int32(0)},
	})
	if _, err := ParseDecUnread(body); err == nil {
		t.Error("a read marker with no watermark names no message, so it must be an error")
	}
}

// A group chat keeps its name, its picture and its team state in chatMetas on the chat itself. The
// body below is written from a measured MultiChat answer rather than captured, with names that are not
// the ones on the account that was measured.
func TestGroupChatMetasGiveTitlePictureAndHost(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "status", Type: tInt32, Val: int32(0)},
		{Key: "chatInfo", Type: tDoc, Val: []Elem{
			{Key: "chatId", Type: tInt64, Val: int64(484100000000000)},
			{Key: "type", Type: tString, Val: "MultiChat"},
			{Key: "chatMetas", Type: tArr, Val: []Elem{
				{Key: "0", Type: tDoc, Val: []Elem{
					{Key: "type", Type: tInt32, Val: int32(3)},
					{Key: "authorId", Type: tInt64, Val: int64(1)},
					{Key: "content", Type: tString, Val: "BRIDGE TEST"},
				}},
				{Key: "1", Type: tDoc, Val: []Elem{
					{Key: "type", Type: tInt32, Val: int32(4)},
					{Key: "content", Type: tString,
						Val: `{"imageUrl":"https://cdn/small.jpg","fullImageUrl":"https://cdn/full.jpg"}`},
				}},
				{Key: "2", Type: tDoc, Val: []Elem{
					{Key: "type", Type: tInt32, Val: int32(15)},
					{Key: "content", Type: tString,
						Val: `{"warehouse":true, "hostId": "447137810", "revision" : 1}`},
				}},
				{Key: "3", Type: tDoc, Val: []Elem{
					{Key: "type", Type: tInt32, Val: int32(1)}, // some other setting: kept, not read
					{Key: "content", Type: tString, Val: "1"},
				}},
			}},
		}},
	})
	cs, err := ParseChatInfoResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.Title(); got != "BRIDGE TEST" {
		t.Errorf("Title() = %q, want the type-3 content", got)
	}
	if full, thumb := cs.RoomImage(); full != "https://cdn/full.jpg" || thumb != "https://cdn/small.jpg" {
		t.Errorf("RoomImage() = %q, %q", full, thumb)
	}
	if host, ok := cs.TeamHost(); !ok || host != 447137810 {
		t.Errorf("TeamHost() = %d, %v", host, ok)
	}
	if len(cs.Metas) != 4 {
		t.Errorf("chatMetas are kept verbatim so a later type can be read; got %d", len(cs.Metas))
	}
}

// A group created without a name has no type-3 meta, and a plain group has no type-15 one: the client
// titles that case from the member list itself, so the parser must invent nothing.
func TestGroupChatWithoutMetasNamesNothing(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "status", Type: tInt32, Val: int32(0)},
		{Key: "chatInfo", Type: tDoc, Val: []Elem{
			{Key: "chatId", Type: tInt64, Val: int64(1)},
			{Key: "type", Type: tString, Val: "MultiChat"},
			{Key: "chatMetas", Type: tArr, Val: []Elem{}},
		}},
	})
	cs, err := ParseChatInfoResponse(body)
	if err != nil {
		t.Fatal(err)
	}
	if got := cs.Title(); got != "" {
		t.Errorf("Title() = %q, want nothing", got)
	}
	if _, ok := cs.TeamHost(); ok {
		t.Error("a MultiChat with no type-15 meta is not a Team Chat")
	}
	if full, thumb := cs.RoomImage(); full != "" || thumb != "" {
		t.Errorf("RoomImage() = %q, %q, want nothing", full, thumb)
	}
}
