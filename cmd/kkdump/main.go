// SPDX-License-Identifier: AGPL-3.0-or-later

// Command kkdump prints the chat logs inside a saved LOCO response body (MCHATLOGS or CHATLOGS), one
// line per message: type, timestamp, the text field, and the attachment JSON.
//
// It exists because the interesting part of a media message is exactly those three fields together, and
// reading them out of BSON by hand (as earlier runs did with python) keeps getting the framing wrong.
//
// Usage: kkdump [-type N] <file.hex>        # or -hex "<hex>"
package main

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/libhwang50/kakaobridge/loco"
)

func main() {
	hexArg := flag.String("hex", "", "response body as hex instead of a file")
	onlyType := flag.Int("type", -1, "only show this Kakao message type")
	full := flag.Bool("full", false, "do not shorten the attachment JSON")
	flag.Parse()

	raw := *hexArg
	if raw == "" {
		if flag.NArg() < 1 {
			fmt.Fprintln(os.Stderr, "usage: kkdump [-type N] [-full] <file.hex>")
			os.Exit(2)
		}
		data, err := os.ReadFile(flag.Arg(0))
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		raw = strings.TrimSpace(string(data))
	}
	body, err := decodeHex(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode:", err)
		os.Exit(1)
	}

	logs, eof, err := loco.ParseChatLogs(body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse:", err)
		os.Exit(1)
	}
	fmt.Printf("# %d chat logs, eof=%v\n", len(logs), eof)
	for _, cl := range logs {
		if *onlyType >= 0 && cl.Type != int32(*onlyType) {
			continue
		}
		fmt.Printf("\ntype=%d(%s) log_id=%d at=%s deleted=%v\n  text: %q\n",
			cl.Type, loco.MessageTypeName(cl.Type), cl.LogID,
			time.Unix(cl.SendAt, 0).Format(time.RFC3339), cl.Deleted, cl.Text)
		if meta := cl.AttachmentMetaOf(); meta != nil {
			fmt.Printf("  media: url=%s\n         name=%q mime=%q %dx%d size=%d dur=%dms alt=%q\n         thumb=%s\n         rsc=%s\n         high: url=%q size=%d %dx%d\n",
				orNone(meta.DownloadURL()), meta.FileName(), meta.MIME, meta.Width, meta.Height,
				meta.Size, meta.DurationMS(cl.Type), meta.Cmt, orNone(meta.ThumbnailURL),
				meta.Resource, meta.URLHigh, meta.SizeHigh, meta.WidthHigh, meta.HeightHigh)
		}
		if cl.Attachment != nil {
			att := fmt.Sprintf("%v", cl.Attachment)
			if !*full && len(att) > 600 {
				att = att[:600] + "…(truncated, -full for all)"
			}
			fmt.Printf("  attachment: %s\n", att)
		}
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func decodeHex(s string) ([]byte, error) {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\n', '\t', '\r':
			return -1
		}
		return r
	}, s)
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var v int
		if _, err := fmt.Sscanf(s[2*i:2*i+2], "%02x", &v); err != nil {
			return nil, fmt.Errorf("byte %d: %w", i, err)
		}
		out[i] = byte(v)
	}
	return out, nil
}
