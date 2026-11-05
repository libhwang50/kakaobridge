// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"strings"
	"unicode/utf16"

	"github.com/libhwang50/kakaobridge/loco"
)

// Style flags per position in a message. They are combined, so one character can be both bold and
// spoiler; the values only have to stay distinct.
const (
	styleBold = 1 << iota
	styleItalic
	styleStrike
	styleSpoiler
)

// styledHTML turns the ranges Kakao keeps beside a message's text into the HTML body Matrix shows next
// to it. Kakao's model is "plain text plus ranges", Matrix's is "one HTML document", and the two do not
// agree about how overlapping runs nest. So this does not try to reproduce the nesting a person would
// write: it works out which styles cover each character and emits one tag per run of unchanged styles.
// Overlaps, a style spanning a line break, and a spoiler around bold all come out correct, just plainly
// marked up.
//
// Kakao counts with UTF-16 code units (see loco.StyleRange for the measurement and the caveat), so the
// ranges are read against the same indexing. A range that does not fit the text is dropped: an offset we
// cannot place is a reason to show the text without that style, not a reason to format the wrong words.
//
// It returns "" when there is nothing to mark up, which is the signal to send plain text.
func styledHTML(text string, rt *loco.RichText) string {
	if !rt.Any() || text == "" {
		return ""
	}
	runes := []rune(text)
	// units[i] is where rune i sits in Kakao's index space, and total is the length of that space.
	units := make([]int, len(runes))
	total := 0
	for i, r := range runes {
		units[i] = total
		total += utf16.RuneLen(r)
	}
	marks := make([]int, len(runes))
	apply := func(rs []loco.StyleRange, flag int) {
		for _, r := range rs {
			if r.Loc < 0 || r.Length <= 0 || r.Loc+r.Length > total {
				continue
			}
			start, end := r.Loc, r.Loc+r.Length
			for i := range runes {
				if units[i] >= start && units[i] < end {
					marks[i] |= flag
				}
			}
		}
	}
	apply(rt.Bold, styleBold)
	apply(rt.Italic, styleItalic)
	apply(rt.Strike, styleStrike)
	apply(rt.Spoilers, styleSpoiler)

	var b strings.Builder
	for i := 0; i < len(runes); {
		mark := marks[i]
		j := i + 1
		for j < len(runes) && marks[j] == mark {
			j++
		}
		chunk := escapeHTML(string(runes[i:j]))
		if mark != 0 {
			// A fixed order keeps the tags nested rather than crossed, which HTML does not allow.
			if mark&styleBold != 0 {
				chunk = "<strong>" + chunk + "</strong>"
			}
			if mark&styleItalic != 0 {
				chunk = "<em>" + chunk + "</em>"
			}
			if mark&styleStrike != 0 {
				chunk = "<del>" + chunk + "</del>"
			}
			if mark&styleSpoiler != 0 {
				// Both spellings clients use for a spoiler: the class the Matrix spec defines, and the
				// data attribute this user's client emits and hides on. A span carrying only the class
				// showed as ordinary text there, so the bridge sends both and lets each client read its own.
				chunk = `<span class="mx-spoiler" data-mx-spoiler="">` + chunk + "</span>"
			}
		}
		b.WriteString(chunk)
		i = j
	}
	html := b.String()
	if html == escapeHTML(text) {
		// Nothing matched, e.g. every range was out of bounds. Better no formatted body than an
		// identical one.
		return ""
	}
	return html
}

// escapeHTML covers what a text body needs: the three characters that change meaning in HTML. Kakao's
// own text never contains HTML of its own, so a message typed as "<b>" stays visible as "<b>".
func escapeHTML(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	return strings.ReplaceAll(s, ">", "&gt;")
}
