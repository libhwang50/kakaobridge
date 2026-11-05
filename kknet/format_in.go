// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"strings"
	"unicode/utf16"

	"golang.org/x/net/html"
	"maunium.net/go/mautrix/event"

	"github.com/libhwang50/kakaobridge/loco"
)

// matrixStyle turns what a Matrix client formatted into what Kakao wants: the text on its own, plus the
// ranges saying which parts of it are styled. It is styledHTML in the other direction, and it exists
// because the plain body of a formatted Matrix message is the markup the person typed. This pair came from
// a real message:
//
//	body:           **Bold**, *Italic*, ~~Strikethrough~~, ||Spoiler|| from Sable Client
//	formatted_body: <strong>Bold</strong>, <em>Italic</em>, <del>Strikethrough</del>,
//	                <span data-mx-spoiler="">Spoiler</span> from Sable Client
//
// Sending the body, as the bridge used to, put the asterisks in Kakao's bubble.
//
// Only the marks Kakao has are carried over: bold, italic, strikethrough, spoiler. What it has nothing for
// - underline, code, a quote, a list - keeps its words and loses its markup, which is what its own client
// does with the same text. A link names its address when the words are not the address, since the address
// is the part that would otherwise be lost.
//
// It reports false when there is nothing here to convert, which is the signal to send the body as it came.
func matrixStyle(content *event.MessageEventContent) (text string, styles *loco.RichText, ok bool) {
	if content.Format != event.FormatHTML || content.FormattedBody == "" {
		return "", nil, false
	}
	doc, err := html.Parse(strings.NewReader(content.FormattedBody))
	if err != nil {
		return "", nil, false
	}
	w := &htmlReader{}
	w.walk(doc, 0, false)

	// Trim the whitespace the document structure adds around the message, keeping the marks in step.
	runes := []rune(w.b.String())
	lo, hi := 0, len(runes)
	for lo < hi && unicodeSpace(runes[lo]) {
		lo++
	}
	for hi > lo && unicodeSpace(runes[hi-1]) {
		hi--
	}
	if lo == hi {
		return "", nil, false
	}
	runes, marks := runes[lo:hi], w.marks[lo:hi]
	return string(runes), w.styles(runes, marks), true
}

// htmlReader collects the text of a Matrix message and, beside it, which styles cover each character. The
// ranges are read back out of that afterwards rather than emitted while walking: a client nests its marks
// however it likes, and Kakao only wants to know what covers each position.
type htmlReader struct {
	b     strings.Builder
	marks []int // one per rune written, using the style flags in format.go
	last  rune  // last rune written, so whitespace can be collapsed the way a renderer does
}

func (w *htmlReader) walk(n *html.Node, flags int, literal bool) {
	switch n.Type {
	case html.TextNode:
		w.push(n.Data, flags, literal)
		return
	case html.ElementNode:
		switch n.Data {
		case "script", "style":
			return
		case "br":
			w.push("\n", flags, true)
			return
		case "p", "div", "blockquote", "ul", "ol", "li", "section", "article",
			"h1", "h2", "h3", "h4", "h5", "h6":
			// Blocks become newlines. Kakao has no block markup and no list markers, so a list arrives
			// as its items, one per line.
			w.newLine(flags)
			w.walkChildren(n, flags, literal)
			w.newLine(flags)
			return
		}
		child := flags
		switch n.Data {
		case "strong", "b":
			child |= styleBold
		case "em", "i":
			child |= styleItalic
		case "del", "s", "strike":
			child |= styleStrike
		case "span":
			if spoilerSpan(n) {
				child |= styleSpoiler
			}
		}
		if n.Data == "a" {
			href := attr(n, "href")
			w.walkChildren(n, child, literal)
			if href != "" && href != rawText(n) {
				w.push(" ("+href+")", flags, true)
			}
			return
		}
		w.walkChildren(n, child, literal || n.Data == "pre" || n.Data == "code")
		return
	}
	w.walkChildren(n, flags, literal)
}

func (w *htmlReader) walkChildren(n *html.Node, flags int, literal bool) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.walk(c, flags, literal)
	}
}

// push writes text, collapsing whitespace runs the way a renderer does when the source is HTML: newlines
// the client put in for readability are spaces to the person reading, and two tags touching each other do
// not make two spaces. Text inside pre and code is kept exactly as written.
func (w *htmlReader) push(s string, flags int, literal bool) {
	for _, r := range s {
		if !literal {
			if r == '\n' || r == '\r' || r == '\t' {
				r = ' '
			}
			if r == ' ' && (w.last == 0 || w.last == ' ' || w.last == '\n') {
				continue
			}
		}
		w.b.WriteRune(r)
		w.marks = append(w.marks, flags)
		w.last = r
	}
}

// newLine separates blocks. A paragraph break is a newline in a Kakao bubble too, which is the one piece
// of block structure that survives.
func (w *htmlReader) newLine(flags int) {
	if w.b.Len() == 0 || w.last == '\n' {
		return
	}
	// A space at the end of a block is nothing to a reader, and a trailing space before a newline is
	// noise in a bubble that shows them.
	for w.last == ' ' {
		runes := []rune(w.b.String())
		runes = runes[:len(runes)-1]
		w.marks = w.marks[:len(w.marks)-1]
		w.b.Reset()
		w.b.WriteString(string(runes))
		if len(runes) == 0 {
			w.last = 0
		} else {
			w.last = runes[len(runes)-1]
		}
	}
	w.push("\n", flags, true)
}

// styles reads the marks back as the ranges Kakao keeps beside a message, counted in the units Kakao
// counts in. Adjacent characters with the same marks become one range, so a mark the client split across
// several tags still arrives as one.
func (w *htmlReader) styles(runes []rune, marks []int) *loco.RichText {
	pos := make([]int, len(runes)+1)
	for i, r := range runes {
		pos[i+1] = pos[i] + utf16.RuneLen(r)
	}
	collect := func(flag int) []loco.StyleRange {
		var out []loco.StyleRange
		for i := 0; i < len(runes); {
			if marks[i]&flag == 0 {
				i++
				continue
			}
			j := i
			for j < len(runes) && marks[j]&flag != 0 {
				j++
			}
			out = append(out, loco.StyleRange{Loc: pos[i], Length: pos[j] - pos[i]})
			i = j
		}
		return out
	}
	return &loco.RichText{
		Bold:     collect(styleBold),
		Italic:   collect(styleItalic),
		Strike:   collect(styleStrike),
		Spoilers: collect(styleSpoiler),
	}
}

// spoilerSpan recognises both spellings clients use. The class is the one the Matrix spec defines; the
// data attribute is the one this user's client emits, and it did not hide a span carrying only the class.
func spoilerSpan(n *html.Node) bool {
	for _, a := range n.Attr {
		switch a.Key {
		case "data-mx-spoiler":
			return true
		case "class":
			for _, c := range strings.Fields(a.Val) {
				if c == "mx-spoiler" {
					return true
				}
			}
		}
	}
	return false
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

// rawText is what an element says, tags ignored and whitespace untouched, used only to decide whether a
// link's words already are its address.
func rawText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			walk(k)
		}
	}
	walk(n)
	return b.String()
}

func unicodeSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '\v' || r == '\f'
}
