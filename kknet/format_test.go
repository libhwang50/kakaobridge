// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

	"github.com/libhwang50/kakaobridge/loco"
)

// TestStyledTextBecomesMatrixHTML is the case measured on the bridge's own self-chat: the text as Kakao
// sends it, and the ranges it put beside it. The HTML has to mark up exactly the words the person styled,
// and leave the rest as it was typed.
func TestStyledTextBecomesMatrixHTML(t *testing.T) {
	const text = "Rich text testing. Bold text. Italic text. Strikethrough.  Spoiler."
	rt := &loco.RichText{
		Bold:     []loco.StyleRange{{Loc: 19, Length: 10}},
		Strike:   []loco.StyleRange{{Loc: 43, Length: 15}},
		Spoilers: []loco.StyleRange{{Loc: 59, Length: 8}},
	}
	want := `Rich text testing. <strong>Bold text.</strong> Italic text. <del>Strikethrough. </del> ` +
		`<span class="mx-spoiler">Spoiler.</span>`
	if got := styledHTML(text, rt); got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

// TestOverlappingStylesNest: HTML does not allow crossed tags, and Kakao's ranges overlap freely. Runs of
// equal styles are emitted one after another, so the tags nest and a client that understands neither style
// still shows the right words.
func TestOverlappingStylesNest(t *testing.T) {
	const text = "0123456789abcdefghij"
	rt := &loco.RichText{
		Bold:   []loco.StyleRange{{Loc: 0, Length: 10}},
		Italic: []loco.StyleRange{{Loc: 5, Length: 10}},
	}
	// bold covers 0-9 and italic 5-14, so the middle of one sits inside the other. The bridge always
	// nests in one order, which is what keeps the tags from crossing.
	want := "<strong>01234</strong><em><strong>56789</strong></em><em>abcde</em>fghij"
	if got := styledHTML(text, rt); got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

// TestASpoilerAroundBoldKeepsBoth because a Kakao message can hide a styled word, and Matrix clients hide
// spoilers by class rather than by nesting.
func TestASpoilerAroundBoldKeepsBoth(t *testing.T) {
	const text = "the hidden word"
	rt := &loco.RichText{
		Bold:     []loco.StyleRange{{Loc: 4, Length: 6}},
		Spoilers: []loco.StyleRange{{Loc: 0, Length: len(text)}},
	}
	want := `<span class="mx-spoiler">the </span>` +
		`<span class="mx-spoiler"><strong>hidden</strong></span>` +
		`<span class="mx-spoiler"> word</span>`
	if got := styledHTML(text, rt); got != want {
		t.Errorf("got\n  %s\nwant\n  %s", got, want)
	}
}

// TestARangeThatDoesNotFitIsDropped rather than moved: an offset the bridge cannot place is a reason to
// show the text unstyled, not a reason to decorate the wrong words.
func TestARangeThatDoesNotFitIsDropped(t *testing.T) {
	for name, rt := range map[string]*loco.RichText{
		"past the end":    {Bold: []loco.StyleRange{{Loc: 100, Length: 5}}},
		"zero length":     {Bold: []loco.StyleRange{{Loc: 0, Length: 0}}},
		"negative start":  {Bold: []loco.StyleRange{{Loc: -3, Length: 5}}},
		"negative length": {Bold: []loco.StyleRange{{Loc: 0, Length: -5}}},
	} {
		if got := styledHTML("short", rt); got != "" {
			t.Errorf("%s produced %q, want plain text", name, got)
		}
	}
}

// TestTextThatLooksLikeHTMLStaysText: a message typed as markup is not markup in Kakao, so it must arrive
// as what was typed.
func TestTextThatLooksLikeHTMLStaysText(t *testing.T) {
	const text = `a <b>&c</b>`
	rt := &loco.RichText{Bold: []loco.StyleRange{{Loc: 2, Length: 5}}}
	want := `a <strong>&lt;b&gt;&amp;c</strong>&lt;/b&gt;`
	if got := styledHTML(text, rt); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestAnEmojiCountsAsTwoUnits pins the indexing the bridge uses. Kakao's own text APIs count UTF-16 code
// units, and an emoji is two of them, so a range covering one emoji has length 2. A character after the
// emoji then sits at unit 3, not 2 - getting this wrong would style the words next to an emoji instead of
// the ones the person styled.
func TestAnEmojiCountsAsTwoUnits(t *testing.T) {
	const text = "a😀b"
	rt := &loco.RichText{Bold: []loco.StyleRange{{Loc: 1, Length: 2}}}
	if got := styledHTML(text, rt); got != "a<strong>😀</strong>b" {
		t.Errorf("got %q, want the emoji itself bold", got)
	}
}

// TestASpanAcrossALineBreakSurvives: Kakao styles a multi-line selection as one range, and Matrix's HTML
// keeps newlines in the body.
func TestASpanAcrossALineBreakSurvives(t *testing.T) {
	const text = "one\ntwo"
	rt := &loco.RichText{Bold: []loco.StyleRange{{Loc: 0, Length: len(text)}}}
	if got := styledHTML(text, rt); got != "<strong>one\ntwo</strong>" {
		t.Errorf("got %q, want both lines bold", got)
	}
}

// TestNoStylesMeansNoHTML: an unstyled message stays a plain text body, which is what every message before
// this feature was, and what a client should keep receiving.
func TestNoStylesMeansNoHTML(t *testing.T) {
	if got := styledHTML("plain text", &loco.RichText{}); got != "" {
		t.Errorf("got %q, want nothing", got)
	}
	if got := styledHTML("plain text", &loco.RichText{Bold: []loco.StyleRange{{Loc: 99, Length: 2}}}); got != "" {
		t.Errorf("got %q, want nothing when no range fits", got)
	}
}
