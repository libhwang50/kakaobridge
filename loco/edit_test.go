// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"testing"
	"unicode/utf16"
)

// TestAnEditKeepsTheMessageAndSaysSo builds the SYNCMODMSG body measured on a real edit of a real
// message: the same logId, the new text, and a revision that counted the edit. The bridge edits the
// Matrix message it already sent rather than sending the text again.
func TestAnEditKeepsTheMessageAndSaysSo(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: int64(1234)},
		{Key: "logId", Type: tInt64, Val: int64(900)},
		{Key: "chatLog", Type: tDoc, Val: []Elem{
			{Key: "logId", Type: tInt64, Val: int64(900)},
			{Key: "chatId", Type: tInt64, Val: int64(1234)},
			{Key: "type", Type: tInt32, Val: int32(MsgFeed)},
			{Key: "message", Type: tString, Val: `{"logId":800,"targetRevision":1,"hidden":true,"feedType":25}`},
		}},
		{Key: "modifiedChatLog", Type: tDoc, Val: []Elem{
			{Key: "logId", Type: tInt64, Val: int64(800)},
			{Key: "chatId", Type: tInt64, Val: int64(1234)},
			{Key: "authorId", Type: tInt64, Val: int64(77)},
			{Key: "type", Type: tInt32, Val: int32(MsgText)},
			{Key: "sendAt", Type: tInt64, Val: int64(1793858792)},
			{Key: "message", Type: tString, Val: "the new text"},
			{Key: "attachment", Type: tString, Val: "{}"},
			{Key: "revision", Type: tInt32, Val: int32(1)},
		}},
		{Key: "status", Type: tInt32, Val: int32(0)},
	})

	edited, notice, err := ParseModifiedChatLog(body)
	if err != nil {
		t.Fatalf("reading the edit: %v", err)
	}
	if edited.LogID != 800 || edited.ChatID != 1234 {
		t.Errorf("the edit names log %d in chat %d, want 800 in 1234", edited.LogID, edited.ChatID)
	}
	if edited.Text != "the new text" {
		t.Errorf("the edit carries %q, want the new text", edited.Text)
	}
	if edited.Revision != 1 {
		t.Errorf("revision is %d, want 1: Kakao counts edits on the message", edited.Revision)
	}
	if edited.Deleted {
		t.Error("an edit is not a deletion")
	}
	feed, ok := FeedMetaOf(notice)
	if !ok {
		t.Fatalf("the notice the edit carries does not read as a feed: %q", notice.Text)
	}
	if feed.FeedType != 25 || feed.LogID != 800 || feed.TargetRevision != 1 {
		t.Errorf("the notice says %+v, want feedType 25 about log 800 at revision 1", feed)
	}
}

// TestAnEditPushWithoutTheMessageIsRefused because a push with nothing in it is not an edit, and guessing
// which message changed would be worse than saying the push could not be read.
func TestAnEditPushWithoutTheMessageIsRefused(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: int64(1234)},
		{Key: "status", Type: tInt32, Val: int32(0)},
	})
	if _, _, err := ParseModifiedChatLog(body); err == nil {
		t.Fatal("a push with no modifiedChatLog was accepted as an edit")
	}
}

// TestADeletedMessageStaysItselfAndSaysItIsGone covers the delete shape measured on a real room: Kakao
// leaves the log, its text and its logId, and sets 0x4000 on the type. Reading that bit must not lose the
// type the message really is, because a deleted photo is still described as a photo.
func TestADeletedMessageStaysItselfAndSaysItIsGone(t *testing.T) {
	body := EncodeDoc([]Elem{
		{Key: "status", Type: tInt32, Val: int32(0)},
		{Key: "eof", Type: tBool, Val: true},
		{Key: "chatLogs", Type: tArr, Val: []Elem{
			{Type: tDoc, Val: []Elem{
				{Key: "logId", Type: tInt64, Val: int64(800)},
				{Key: "chatId", Type: tInt64, Val: int64(1234)},
				{Key: "authorId", Type: tInt64, Val: int64(77)},
				{Key: "type", Type: tInt32, Val: int32(MsgPhoto | MsgDeletedBit)},
				{Key: "message", Type: tString, Val: "still here"},
				{Key: "attachment", Type: tString, Val: `{"lastFeedId":901}`},
			}},
			{Type: tDoc, Val: []Elem{
				{Key: "logId", Type: tInt64, Val: int64(901)},
				{Key: "chatId", Type: tInt64, Val: int64(1234)},
				{Key: "type", Type: tInt32, Val: int32(MsgFeed)},
				{Key: "message", Type: tString, Val: `{"feedType":14,"logId":800,"hidden":true}`},
			}},
		}},
	})

	logs, _, err := ParseChatLogs(body)
	if err != nil {
		t.Fatalf("reading the history: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("got %d logs, want 2", len(logs))
	}
	gone, notice := logs[0], logs[1]
	if !gone.Deleted {
		t.Error("a log carrying the 0x4000 bit was not read as deleted")
	}
	if gone.Type != MsgPhoto {
		t.Errorf("the type reads as %d; the delete bit must not hide what the message is", gone.Type)
	}
	if gone.Text != "still here" {
		t.Errorf("text is %q; Kakao leaves it, so the bridge should not invent anything", gone.Text)
	}
	feed, ok := FeedMetaOf(notice)
	if !ok {
		t.Fatalf("the delete notice does not read as a feed: %q", notice.Text)
	}
	if feed.FeedType != 14 || feed.LogID != 800 {
		t.Errorf("the notice says %+v, want feedType 14 naming log 800", feed)
	}
}

// TestStylesPointAtTheTextTheyCover uses the two attachments measured on the bridge's own self-chat,
// against the text they were sent with. The offsets are the client's, so the check is that each range
// selects the words the person styled.
func TestStylesPointAtTheTextTheyCover(t *testing.T) {
	const text = "Rich text testing. Bold text. Italic text. Strikethrough.  Spoiler."
	rt, ok := RichTextOf(ChatLog{
		Type:       MsgText,
		Text:       text,
		Attachment: `{"spoilers":[{"loc":59,"len":8}],"richText":{"bold":[[19,10]],"strike":[[43,15]]}}`,
	})
	if !ok {
		t.Fatal("an attachment with styles was read as having none")
	}
	if got := covered(text, rt.Bold); got != "Bold text." {
		t.Errorf("bold covers %q, want the words the person bolded", got)
	}
	if got := covered(text, rt.Strike); got != "Strikethrough. " {
		t.Errorf("strike covers %q, want the words and the space the client struck through", got)
	}
	if got := covered(text, rt.Spoilers); got != "Spoiler." {
		t.Errorf("the spoiler covers %q, want the hidden word", got)
	}
	if len(rt.Italic) != 0 {
		t.Errorf("italic has %d ranges, want none", len(rt.Italic))
	}

	const italicText = "Italic text."
	italic, ok := RichTextOf(ChatLog{Type: MsgText, Text: italicText, Attachment: `{"richText":{"italic":[[0,12]]}}`})
	if !ok {
		t.Fatal("an italic range was not read")
	}
	if got := covered(italicText, italic.Italic); got != italicText {
		t.Errorf("italic covers %q, want the whole message", got)
	}
}

// TestAPlainMessageClaimsNoStyles, including the attachments that carry something else: a quoted reply's
// source, or the notice a deletion leaves behind. Neither is a style, and reading one as one would decorate
// plain text.
func TestAPlainMessageClaimsNoStyles(t *testing.T) {
	for _, attachment := range []any{
		nil, "", "{}", `{"lastFeedId":901}`,
		`{"src_userId":7,"src_logId":800,"src_type":1,"src_message":"quoted","src_spoilers":[]}`,
		`{"k":"abc","url":"https://example.invalid/x.png"}`,
		`{"richText":{},"spoilers":[]}`,
	} {
		if rt, ok := RichTextOf(ChatLog{Type: MsgText, Text: "plain", Attachment: attachment}); ok {
			t.Errorf("%v was read as styles %+v", attachment, rt)
		}
	}
}

// TestAReplyNamesTheMessageItQuotes uses the reply measured on the bridge's own self-chat. The message
// text holds only what was typed; the quote is entirely in the attachment, and its id is the id the bridge
// already uses for that message.
func TestAReplyNamesTheMessageItQuotes(t *testing.T) {
	cl := ChatLog{
		Type: MsgReply,
		Text: "Reply to the message above",
		Attachment: `{"src_userId":447137810,"src_logId":3965989223323015169,"src_type":1,` +
			`"src_message":"^ \uc774\ubbf8\uc9c0, \uc601\uc0c1 \uc804\uc1a1 \uac00\ub2a5","src_spoilers":[]}`,
	}
	reply, ok := ReplyMetaOf(cl)
	if !ok {
		t.Fatal("a quoted reply was not read as one")
	}
	if reply.LogID != 3965989223323015169 {
		t.Errorf("quotes log %d, want 3965989223323015169", reply.LogID)
	}
	if reply.UserID != 447137810 || reply.Type != MsgText {
		t.Errorf("quotes %+v, want the person and a text message", reply)
	}
	if reply.Text == "" {
		t.Error("the quoted text was dropped; it is the only copy of it Kakao sends")
	}
}

func TestAReplyWithNothingToQuoteIsNotClaimed(t *testing.T) {
	for _, attachment := range []string{
		`{"src_userId":7}`,
		`{"src_message":"orphan"}`,
		`{"lastFeedId":901}`,
	} {
		if reply, ok := ReplyMetaOf(ChatLog{Type: MsgReply, Text: "x", Attachment: attachment}); ok {
			t.Errorf("%v was read as a reply to %v", attachment, reply)
		}
	}
}

// covered reads the text the way the client indexes it: UTF-16 code units. That is what a JVM app's text
// APIs count, and it is the assumption this bridge makes (see StyleRange). For ASCII the three ways of
// counting agree; for anything else the difference matters, so it is spelled out here rather than hidden.
func covered(text string, ranges []StyleRange) string {
	var out []string
	for _, r := range ranges {
		units := utf16.Encode([]rune(text))
		if r.Loc < 0 || r.Length <= 0 || r.Loc+r.Length > len(units) {
			out = append(out, "<out of range>")
			continue
		}
		out = append(out, string(utf16.Decode(units[r.Loc:r.Loc+r.Length])))
	}
	return joinAll(out)
}

func joinAll(parts []string) string {
	all := ""
	for _, p := range parts {
		all += p
	}
	return all
}
