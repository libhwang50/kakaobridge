// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"encoding/json"
	"fmt"
	urlpkg "net/url"
	"strconv"
	"strings"
	"time"
)

// EmojiDescriptor is the `emojis` object of a message that carries mini emoticons. Measured on a message
// whose text was literally "(item)(item) <-- mini emoticons":
//
//	{"emojis":{"total_len":12,"total_item":2,
//	          "items":[{"at":[1],"id":"1200283_027","len":6},{"at":[2],"id":"1200283_034","len":6}]}}
type EmojiDescriptor struct {
	TotalLen  int         `json:"total_len"`
	TotalItem int         `json:"total_item"`
	Items     []MiniEmoji `json:"items"`
}

// MiniEmoji is one inline emoticon: where its placeholder sits in the text and which item it is.
type MiniEmoji struct {
	At  []int  `json:"at"`
	ID  string `json:"id"`
	Len int    `json:"len"`
}

// AttachmentMeta is the JSON that sits in a chatLog's `attachment` field for media messages.
//
// Measured on a photo and a file sent to the self-chat (kept as testdata/mchatlogs_memo.hex):
//
//	{"k":"<dna key>","rsc":"<resource path>","w":1920,"h":1080,"s":10818,"mt":"image/png",
//	 "thumbnailUrl":"https://talk.kakaocdn.net/dna/…?credential=…&expires=…&signature=…&convert=resize&w=120&h=68",
//	 "url":"https://talk.kakaocdn.net/dna/…?credential=…&expires=…&signature=…",
//	 "expire":1794301799197, "hfac":"1:cefef055", "rt":"vk"}
//
// and for a file the same plus {"name":"conflict.mpg","size":34590724}.
//
// Two things matter here. First, the download is a plain signed HTTPS GET: no legacy trailer
// handshake is needed to *fetch* media the server already has (the GETTRAILER/legacy-RSA path in
// docs/protocol-notes.md §8 is something else). Second, the signature expires - `expires`/`expire` -
// so a bridge must fetch at bridging time; history whose signature has expired may only offer the
// thumbnail.
type AttachmentMeta struct {
	DNAKey       string `json:"k"`
	Resource     string `json:"rsc"`
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnailUrl"`
	ThumbnailW   int    `json:"thumbnailWidth"`
	ThumbnailH   int    `json:"thumbnailHeight"`
	Name         string `json:"name"`
	MIME         string `json:"mt"`
	Size         int64  `json:"s"`
	FileSize     int64  `json:"size"`
	Width        int    `json:"w"`
	Height       int    `json:"h"`
	Duration     int    `json:"d"`
	Expire       int64  `json:"expire"`

	// Cmt is what the app calls the alt text of a photo or video: the user types it in the "alt text"
	// editor and it lands here, never in the message body. Measured: a photo sent from the PC client
	// carries {"cmt":"Picture of me skiing with alt text"}.
	Cmt string `json:"cmt"`
	// CS is "" in every sample taken so far (photos, video, file). Unknown purpose; kept so it is not
	// mistaken for a caption.
	CS string `json:"cs"`

	// An emote (sticker) message names an item instead of giving a URL: `path`/`emoticonItemPath` are the
	// object key on KakaoTalk's item CDN (measured: 2212560.emot_047.png, 4412207.emot_048.webp) and
	// `type` is its MIME type. There is no signature and no expiry on these, unlike DNA media.
	// Note that for an emote the `name` field is the category label ("(Emoticons)"), not a file name.
	//
	// The key format is the Android client's own (defpackage/nkp.java): <itemCode>.emot_%03d.webp for an
	// animated sticker, .emot_%03d.png for a static one, .emoji_%03d.png/.webp for a "mini emoticon"
	// (inline emoji) and .thum_%03d.png for a store thumbnail.
	Path             string `json:"path"`
	EmoticonItemPath string `json:"emoticonItemPath"`
	ItemType         string `json:"type"`

	// Emojis describes the mini emoticons (inline emoji) inside a text message: the body carries a
	// "(item)" placeholder per entry at the offsets in at, and id names the item.
	Emojis *EmojiDescriptor `json:"emojis"`

	// A video carries two encodings: `url`/`s`/`w`/`h` for a broadly playable one and, when present,
	// `urlh`/`sh`/`wh`/`hh`/`dh` for a high-efficiency variant of the same clip (measured: a 2:00 clip
	// as talkv_high.mp4 at 1280x720 and talkv_hevc.mp4 at 1920x1080). `rsc` names the second one while
	// `url` serves the first, so the download and the resource path do not have the same file name.
	URLHigh      string `json:"urlh"`
	SizeHigh     int64  `json:"sh"`
	WidthHigh    int    `json:"wh"`
	HeightHigh   int    `json:"hh"`
	DurationHigh int    `json:"dh"`
}

// AttachmentMetaOf decodes a chat log's attachment. It returns nil (not an error) when there is no
// JSON object there: a plain text message carries "{}", and some plus-chat cards carry a shape with
// none of these fields, which is not something the caller should treat as a failure.
func (cl ChatLog) AttachmentMetaOf() *AttachmentMeta {
	raw, ok := cl.Attachment.(string)
	if !ok {
		return nil
	}
	if _, elems, ok := decodeAttachmentObject(raw); ok {
		_ = elems
	}
	var meta AttachmentMeta
	if err := json.Unmarshal([]byte(raw), &meta); err != nil {
		return nil
	}
	if meta.URL == "" && meta.ThumbnailURL == "" && meta.Resource == "" && meta.Path == "" && meta.Emojis == nil {
		return nil
	}
	if meta.Size == 0 {
		meta.Size = meta.FileSize
	}
	if meta.MIME == "" && meta.ItemType != "" {
		meta.MIME = meta.ItemType // emotes declare their type in `type` instead of `mt`
	}
	if meta.Resource == "" {
		meta.Resource = meta.EmoticonItemPath
	}
	// Kakao writes "image/jpg" for JPEGs, which is not a registered type; Matrix clients cope but the
	// homeserver and every consumer prefer the real one.
	if meta.MIME == "image/jpg" {
		meta.MIME = "image/jpeg"
	}
	return &meta
}

// DurationMS returns the playback length in milliseconds. The unit of `d` is NOT the same everywhere,
// measured on the same capture (loco/testdata/mchatlogs_memo_media.hex): a voice note of 19 KB AAC has
// d=7616 (seven seconds, which matches the bytes at that bitrate) while a video has d=120 and is in
// fact about two minutes long. So voice counts milliseconds and video counts seconds.
func (m *AttachmentMeta) DurationMS(msgType int32) int {
	if m.Duration == 0 {
		return 0
	}
	if msgType == MsgVoice {
		return m.Duration
	}
	return m.Duration * 1000
}

// decodeAttachmentObject reports whether the attachment string is a JSON object (as opposed to "" or
// a bare value), which is what tells a caller "there might be media here".
func decodeAttachmentObject(raw string) (any, []Elem, bool) {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, nil, false
	}
	if _, ok := v.(map[string]any); !ok {
		return v, nil, false
	}
	return v, nil, true
}

// Expired reports whether the signed download URL is past its own expiry. The `expire` field is the
// longer of the two windows on an attachment: measured on the same photo, it ran two weeks past the
// history read that returned it, while the signature in the URL itself (`expires`, unix seconds) ran
// three days. So this says the attachment is still being served at all, which is what the bridge's own
// "fetch it now or maybe never" decision needs; the shorter signature is in SignedUntil.
func (m *AttachmentMeta) Expired(now time.Time) bool {
	if m.Expire == 0 {
		return false
	}
	return time.UnixMilli(m.Expire).Before(now)
}

// SignedUntil reads the expiry of a signed DNA URL out of the URL: `expires` in unix seconds. Measured
// on two history reads of the same photo, each signature was valid exactly three days from the moment
// it was handed over.
func SignedUntil(rawURL string) (time.Time, bool) {
	u, err := urlpkg.Parse(rawURL)
	if err != nil {
		return time.Time{}, false
	}
	sec, err := strconv.ParseInt(u.Query().Get("expires"), 10, 64)
	if err != nil || sec <= 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// DownloadURL returns the best URL to fetch: the full resource if present, otherwise the thumbnail
// (which is the same image resized, and sometimes the only thing still signed).
func (m *AttachmentMeta) DownloadURL() string {
	if m.URL != "" {
		return m.URL
	}
	if m.ThumbnailURL != "" {
		return m.ThumbnailURL
	}
	return m.ItemURL()
}

// itemCDN is where KakaoTalk serves sticker/emote and mini-emoticon items from. Unlike DNA media these
// are plain unsigned objects; measured with curl:
//
//	https://item.kakaocdn.net/dw/2212560.emot_047.png  -> 200 image/png   30357 bytes (360x360)
//	https://item.kakaocdn.net/dw/4412207.emot_048.webp -> 200 image/webp 320210 bytes (animated)
//	https://item.kakaocdn.net/dw/1200283.emoji_027.png -> 200 image/png   11546 bytes (180x180)
const itemCDN = "https://item.kakaocdn.net/dw/"

// ItemURL builds the download URL for an emote/sticker item; empty when the message names no item.
func (m *AttachmentMeta) ItemURL() string {
	key := m.Path
	if key == "" {
		key = m.EmoticonItemPath
	}
	if key == "" {
		return ""
	}
	return itemCDN + key
}

// StillPreviewItemURL is the item key for the pack's static preview of a sticker: the client builds
// store thumbnails as <itemCode>.thum_<index>.png (defpackage/nkp.java), and on the CDN those are
// ordinary PNGs (measured: 4412207.thum_034.png -> 200 image/png, 210x210).
//
// That matters because the animated asset itself is not an image a general client can open: fetching
// 4412207.emot_034.webp gives 258358 bytes served as image/webp with no RIFF/WEBP or VP8X header, no
// gzip/zlib/deflate/lzma/brotli stream at any offset in its head, and a trailer mentioning
// "(OS_X_10.12@x86_64)" - a Kakao container that only their own clients decode. Uploading those bytes
// verbatim leaves every Matrix client with an image it cannot open (Sable: "Media unavailable",
// Element: "Unable to load image"). Static sticker items (.emot_NNN.png) are ordinary PNGs, and query
// parameters such as ?convert=gif are ignored by this CDN, so the still preview is the fallback.
func (m *AttachmentMeta) StillPreviewItemURL() string {
	key := m.Path
	if key == "" {
		key = m.EmoticonItemPath
	}
	i := strings.Index(key, ".emot_")
	if i <= 0 {
		return ""
	}
	rest := key[i+len(".emot_"):]
	digits := rest
	if dot := strings.IndexByte(rest, '.'); dot >= 0 {
		digits = rest[:dot]
	}
	if digits == "" {
		return ""
	}
	return itemCDN + key[:i] + ".thum_" + digits + ".png"
}

// MiniEmojiURL resolves one entry of a text message's `emojis` descriptor: the id <itemCode>_<index>
// maps to the item key <itemCode>.emoji_<index, three digits>.png. Measured: 1200283_027 gives a
// 180x180 PNG. A reaction's item code is the same shape and resolves the same way - every one of the
// 114 built-in reaction items, `1200509_001` to `_114`, answers 200 image/png at this address, with no
// signature and no expiry, and the index must keep its three digits (a two-digit one 404s). Whether the
// item is animated changes the extension to .webp, which the message does not say, so a caller that needs
// animation must ask the item store.
func MiniEmojiURL(id string) string {
	set, idx, ok := strings.Cut(id, "_")
	if !ok || set == "" || idx == "" {
		return ""
	}
	return itemCDN + set + ".emoji_" + idx + ".png"
}

// FileName names the attachment. `name` is only sent for a file upload; generated media is named by
// its resource path (EnvWMQ1Drvjg.jpg, talka_aac.m4a, talkv_hevc.mp4). When the download URL points at
// a different encoding than `rsc` (the case for video), the URL wins, since that is the file we fetch.
func (m *AttachmentMeta) FileName() string {
	// An emote is named by its item key; its `name` field is a category label.
	if m.Path != "" || m.EmoticonItemPath != "" {
		key := m.Path
		if key == "" {
			key = m.EmoticonItemPath
		}
		return baseName(key)
	}
	if m.Name != "" {
		return m.Name
	}
	if url := m.DownloadURL(); url != "" {
		if p, err := urlpkg.Parse(url); err == nil {
			if base := baseName(p.Path); base != "" {
				return base
			}
		}
	}
	return baseName(m.Resource)
}

func baseName(s string) string {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return s[i+1:]
		}
	}
	return s
}

// ErrNoAttachment is returned by callers that need media but got a chat log without usable media.
var ErrNoAttachment = fmt.Errorf("loco: message has no downloadable attachment")

// attachmentOf decodes a chat log's `attachment` into v. The field is a JSON string on the wire, and it
// is "{}" for a plain message. Every shape of attachment this package reads - media, quoted replies,
// styles - goes through here.
func attachmentOf(cl ChatLog, v any) bool {
	raw, ok := cl.Attachment.(string)
	if !ok || raw == "" || raw == "{}" {
		return false
	}
	return json.Unmarshal([]byte(raw), v) == nil
}

// ReplyMeta is the attachment of a quoted reply (type 26, MsgReply). Measured on a reply in the
// self-chat, whose own text was only "Reply to the message above":
//
//	{"src_userId":447137810,"src_logId":3965989223323015169,"src_type":1,
//	 "src_message":"^ 이미지, 영상 전송 가능","src_spoilers":[]}
//
// Nothing in the message text refers to the quote: the client draws the quoted bubble from these fields
// alone. src_logId is the same log id the bridge already uses as its Matrix message id, so a Kakao reply
// has a direct Matrix equivalent. src_type is the type of the quoted message, which need not be text.
type ReplyMeta struct {
	UserID int64  `json:"src_userId"`
	LogID  int64  `json:"src_logId"`
	Type   int32  `json:"src_type"`
	Text   string `json:"src_message"`
}

// ReplyMetaOf reads what a reply quotes. It says false for anything that is not a reply, and for a reply
// with no source id, where there would be nothing to point at.
func ReplyMetaOf(cl ChatLog) (*ReplyMeta, bool) {
	var meta ReplyMeta
	if !attachmentOf(cl, &meta) || meta.LogID == 0 {
		return nil, false
	}
	return &meta, true
}

// StyleRange is one span of a message carrying a style. On the wire it is a pair: "bold":[[19,10]] means
// ten characters starting at index 19. The client's own model (com.kakao.talk.chat.richtext.RichTextRange)
// calls the two numbers loc and length.
//
// Measured on the self-chat, against the text
// "Rich text testing. Bold text. Italic text. Strikethrough.  Spoiler.":
//
//	{"richText":{"bold":[[19,10]],"strike":[[43,15]]}}
//	{"spoilers":[{"loc":59,"len":8}]}
//	{"richText":{"italic":[[0,12]]}}
//
// Those offsets land exactly on "Bold text.", on "Strikethrough." plus the space after it, and on
// "Spoiler.". The samples are ASCII, so they cannot say whether an index counts bytes, characters or
// UTF-16 code units; the client is a JVM app whose text APIs count UTF-16, so that is what the bridge
// assumes, and a range that does not fit the text is dropped rather than guessed at.
type StyleRange struct {
	Loc    int
	Length int
}

func (r *StyleRange) UnmarshalJSON(data []byte) error {
	var pair []int
	if err := json.Unmarshal(data, &pair); err != nil {
		return fmt.Errorf("loco: style range is not a [loc, length] pair: %w", err)
	}
	if len(pair) != 2 {
		return fmt.Errorf("loco: style range %s is not a [loc, length] pair", data)
	}
	r.Loc, r.Length = pair[0], pair[1]
	return nil
}

// RichText holds the styles Kakao keeps beside a message's text. The client's own model
// (com.kakao.talk.chat.richtext.RichText) carries exactly three lists of ranges - bold, italic, strike -
// and a separate spoiler list, which is what this reads. Other style-shaped names appear in the app's
// strings (underline, highlight, color, subText, linkText) but not in that model, so they are not invented
// here.
type RichText struct {
	Bold     []StyleRange
	Italic   []StyleRange
	Strike   []StyleRange
	Spoilers []StyleRange
}

// RichTextOf reads the styles of a message. It says false when there are none, which is every message
// without a richText or spoilers object.
func RichTextOf(cl ChatLog) (*RichText, bool) {
	var doc struct {
		RichText struct {
			Bold   []StyleRange `json:"bold"`
			Italic []StyleRange `json:"italic"`
			Strike []StyleRange `json:"strike"`
		} `json:"richText"`
		Spoilers []struct {
			Loc    int `json:"loc"`
			Length int `json:"len"`
		} `json:"spoilers"`
	}
	if !attachmentOf(cl, &doc) {
		return nil, false
	}
	rt := &RichText{Bold: doc.RichText.Bold, Italic: doc.RichText.Italic, Strike: doc.RichText.Strike}
	for _, s := range doc.Spoilers {
		rt.Spoilers = append(rt.Spoilers, StyleRange{Loc: s.Loc, Length: s.Length})
	}
	if !rt.Any() {
		return nil, false
	}
	return rt, true
}

// Any says whether there is a style to draw at all.
func (rt *RichText) Any() bool {
	return rt != nil && len(rt.Bold)+len(rt.Italic)+len(rt.Strike)+len(rt.Spoilers) > 0
}

// writeStyles is the `extra` field of WRITE, spelled the way the client spells it: styles are
// [loc, length] pairs under richText, spoilers are {"loc","len"} objects beside it.
type writeStyles struct {
	RichText *struct {
		Bold   [][]int `json:"bold,omitempty"`
		Italic [][]int `json:"italic,omitempty"`
		Strike [][]int `json:"strike,omitempty"`
	} `json:"richText,omitempty"`
	Spoilers []struct {
		Loc int `json:"loc"`
		Len int `json:"len"`
	} `json:"spoilers,omitempty"`
}

func writePairs(rs []StyleRange) [][]int {
	out := make([][]int, 0, len(rs))
	for _, r := range rs {
		out = append(out, []int{r.Loc, r.Length})
	}
	return out
}

// WriteExtra renders styles as the JSON a client sends beside a message it is writing. WRITE carries it in
// the `extra` field, and Kakao stores it back as the message's attachment. Measured from the client's own
// connection: selecting a composed message and pressing Bold sent
//
//	extra={"richText":{"bold":[[0,18]]}}
//
// and the bolded word of a Korean message came back as attachment={"richText":{"bold":[[13,2]]}} over
// "이미지 영상 전송 가능 확인" - fifteen characters, thirty-seven bytes, so the positions name
// characters, not bytes, exactly as StyleRange does on the way in.
//
// It returns "" when there is nothing to style, and then the field is left out entirely, as an unstyled
// WRITE leaves it out.
func WriteExtra(rt *RichText) string {
	if !rt.Any() {
		return ""
	}
	doc := writeStyles{}
	if len(rt.Bold)+len(rt.Italic)+len(rt.Strike) > 0 {
		doc.RichText = &struct {
			Bold   [][]int `json:"bold,omitempty"`
			Italic [][]int `json:"italic,omitempty"`
			Strike [][]int `json:"strike,omitempty"`
		}{writePairs(rt.Bold), writePairs(rt.Italic), writePairs(rt.Strike)}
	}
	for _, s := range rt.Spoilers {
		doc.Spoilers = append(doc.Spoilers, struct {
			Loc int `json:"loc"`
			Len int `json:"len"`
		}{s.Loc, s.Length})
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return ""
	}
	return string(raw)
}
