// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

	"maunium.net/go/mautrix/event"

	"github.com/libhwang50/kakaobridge/loco"
)

// formatted runs the conversion over what a Matrix client would send for that body and HTML.
func formatted(t *testing.T, body, html string) (string, *loco.RichText) {
	t.Helper()
	text, styles, ok := matrixStyle(&event.MessageEventContent{
		MsgType:       event.MsgText,
		Body:          body,
		Format:        event.FormatHTML,
		FormattedBody: html,
	})
	if !ok {
		t.Fatalf("a formatted message must be converted, not sent as its markup: %q", html)
	}
	return text, styles
}

func ranges(t *testing.T, what string, got, want []loco.StyleRange) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s: got %v, want %v", what, got, want)
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s: range %d is %v, want %v", what, i, got[i], want[i])
		}
	}
}

// The body and the HTML here are the pair a real message carried. The body is what the person typed in
// Markdown; Kakao would have shown the asterisks, which is the bug this conversion exists for.
func TestFormattedMatrixTextBecomesKakaoRanges(t *testing.T) {
	body := "**Bold**, *Italic*, ~~Strikethrough~~, ||Spoiler|| from Sable Client"
	html := `<strong>Bold</strong>, <em>Italic</em>, ` +
		`<del>Strikethrough</del>, <span data-mx-spoiler="">Spoiler</span> from Sable Client`
	text, styles := formatted(t, body, html)

	want := "Bold, Italic, Strikethrough, Spoiler from Sable Client"
	if text != want {
		t.Errorf("text must be what the reader sees, without the markup:\n got %q\nwant %q", text, want)
	}
	ranges(t, "bold", styles.Bold, []loco.StyleRange{{Loc: 0, Length: 4}})
	ranges(t, "italic", styles.Italic, []loco.StyleRange{{Loc: 6, Length: 6}})
	ranges(t, "strike", styles.Strike, []loco.StyleRange{{Loc: 14, Length: 13}})
	ranges(t, "spoiler", styles.Spoilers, []loco.StyleRange{{Loc: 29, Length: 7}})

	// And the field must be spelled the way the client spells it: pairs for styles, objects for spoilers.
	if got := loco.WriteExtra(styles); got !=
		`{"richText":{"bold":[[0,4]],"italic":[[6,6]],"strike":[[14,13]]},"spoilers":[{"loc":29,"len":7}]}` {
		t.Errorf("the extra field of WRITE is spelled wrong: %s", got)
	}
}

// Two measurements of the client's own output, so the field is not a guess: selecting a composed message
// and pressing Bold sent the first, and a bolded Korean word came back as the second over
// "이미지 영상 전송 가능 확인" - fifteen characters, thirty-seven bytes, so positions count
// characters and not bytes.
func TestTheFieldCarriesWhatTheClientSends(t *testing.T) {
	whole := &loco.RichText{Bold: []loco.StyleRange{{Loc: 0, Length: 18}}}
	if got := loco.WriteExtra(whole); got != `{"richText":{"bold":[[0,18]]}}` {
		t.Errorf("got %s, want the exact field a real client sent", got)
	}
	word := &loco.RichText{Bold: []loco.StyleRange{{Loc: 13, Length: 2}}}
	if got := loco.WriteExtra(word); got != `{"richText":{"bold":[[13,2]]}}` {
		t.Errorf("got %s, want the exact attachment a real client produced", got)
	}
	if got := loco.WriteExtra(nil); got != "" {
		t.Errorf("an unstyled message sends no extra field at all, got %q", got)
	}
	only := &loco.RichText{Spoilers: []loco.StyleRange{{Loc: 0, Length: 3}}}
	if got := loco.WriteExtra(only); got != `{"spoilers":[{"loc":0,"len":3}]}` {
		t.Errorf("a spoiler alone must not invent a richText key: %s", got)
	}
}

func TestAKoreanRangeCountsCharactersNotBytes(t *testing.T) {
	text, styles := formatted(t,
		"이미지 영상 전송 가능 **확인**",
		"이미지 영상 전송 가능 <strong>확인</strong>")
	if text != "이미지 영상 전송 가능 확인" {
		t.Errorf("text: %q", text)
	}
	ranges(t, "bold", styles.Bold, []loco.StyleRange{{Loc: 13, Length: 2}})
}

func TestAnEmojiCountsAsTwoUnitsOnTheWayOut(t *testing.T) {
	text, styles := formatted(t, "😀 **hi**", `😀 <strong>hi</strong>`)
	if text != "😀 hi" {
		t.Errorf("text: %q", text)
	}
	// The emoji takes two units and the space one, so the word starts at three.
	ranges(t, "bold", styles.Bold, []loco.StyleRange{{Loc: 3, Length: 2}})
}

// Kakao marks one range per styled run; a client may split the same style over several tags. What matters
// on the way in is the positions, so the runs are merged.
func TestAMarkTheClientSplitIsOneRange(t *testing.T) {
	text, styles := formatted(t, "**ab**", `<strong>a</strong><strong>b</strong>`)
	if text != "ab" {
		t.Errorf("text: %q", text)
	}
	ranges(t, "bold", styles.Bold, []loco.StyleRange{{Loc: 0, Length: 2}})
}

func TestNestedMarksKeepBoth(t *testing.T) {
	text, styles := formatted(t, "**bold *and italic***", `<strong>bold <em>and italic</em></strong>`)
	if text != "bold and italic" {
		t.Errorf("text: %q", text)
	}
	ranges(t, "bold", styles.Bold, []loco.StyleRange{{Loc: 0, Length: 15}})
	ranges(t, "italic", styles.Italic, []loco.StyleRange{{Loc: 5, Length: 10}})
}

// A spoiler spelled only with the class is still a spoiler: the spec spells it that way, and the bridge
// emits that spelling itself, so the two ends have to agree.
func TestASpanMarkedWithTheClassIsStillASpoiler(t *testing.T) {
	_, styles := formatted(t, "||hush||", `<span class="mx-spoiler">hush</span>`)
	ranges(t, "spoiler", styles.Spoilers, []loco.StyleRange{{Loc: 0, Length: 4}})
}

// A link is the one thing whose words and address differ. Kakao has no link markup, so the address goes
// into the text rather than disappearing.
func TestALinkKeepsItsWordsAndNamesItsAddress(t *testing.T) {
	text, styles := formatted(t, "[matrix](https://matrix.org/)", `<a href="https://matrix.org/">matrix</a>`)
	if text != "matrix (https://matrix.org/)" {
		t.Errorf("text: %q", text)
	}
	if styles.Any() {
		t.Errorf("a link is not a style Kakao has: %+v", styles)
	}
	if text2, _ := formatted(t, "https://matrix.org/", `<a href="https://matrix.org/">https://matrix.org/</a>`); text2 != "https://matrix.org/" {
		t.Errorf("a link whose words are its address must not repeat it: %q", text2)
	}
}

// Paragraph breaks are the one piece of block structure a Kakao bubble shows, so they survive; the markup
// Kakao has nothing for keeps its words and loses its decoration.
func TestBlocksBecomeLineBreaksAndUndecoratedMarkupKeepsItsWords(t *testing.T) {
	text, styles := formatted(t, "one\n\ntwo", "<p>one</p><p>two</p>")
	if text != "one\ntwo" {
		t.Errorf("paragraphs: %q", text)
	}
	if styles.Any() {
		t.Errorf("no style was asked for: %+v", styles)
	}

	text, styles = formatted(t, "under code quoted",
		"<u>under</u> <code>code</code> <blockquote>quoted</blockquote>")
	if text != "under code\nquoted" {
		t.Errorf("a quote becomes its own line, nothing more: %q", text)
	}
	if styles.Any() {
		t.Errorf("Kakao has no underline or code: %+v", styles)
	}

	// Whitespace the client put in the source for readability is not part of the message.
	text, _ = formatted(t, "one two", "<strong>one</strong>\n  <em>two</em>")
	if text != "one two" {
		t.Errorf("whitespace between tags: %q", text)
	}
	if got, _ := formatted(t, "  keep\ninside  ", "<pre>  keep\ninside  </pre>"); got != "keep\ninside" {
		t.Errorf("only the padding around the whole message is trimmed, not what is inside pre: %q", got)
	}
}

// An unformatted message has nothing to convert, and the caller must then send its body like before.
func TestPlainTextIsSentAsItCame(t *testing.T) {
	for _, c := range []*event.MessageEventContent{
		{MsgType: event.MsgText, Body: "plain **text** with markers"},
		{MsgType: event.MsgText, Body: "html", Format: event.FormatHTML}, // says HTML, sent none
	} {
		if _, _, ok := matrixStyle(c); ok {
			t.Errorf("%q must not be converted", c.Body)
		}
		if text, _, err := kakaoTextOf(c); err != nil || text != c.Body {
			t.Errorf("%q must reach Kakao unchanged: %q %v", c.Body, text, err)
		}
	}
	// A formatted message must not send the markup: that is the whole point.
	text, styles, err := kakaoTextOf(&event.MessageEventContent{
		MsgType: event.MsgText, Body: "**hi**", Format: event.FormatHTML, FormattedBody: "<strong>hi</strong>",
	})
	if err != nil || text != "hi" || !styles.Any() {
		t.Errorf("markup must not reach a Kakao bubble: %q %+v %v", text, styles, err)
	}
}
