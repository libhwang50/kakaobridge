// SPDX-License-Identifier: AGPL-3.0-or-later

// Command kksessions prints the bridge's own KakaoTalk "session list": the account it is logged in
// as, its contacts, and the rooms its LOCO session is told about.
//
//	go run ./cmd/kksessions -tokens tokens.json
//
// The room list comes from the LOCO LOGINLIST response (docs/protocol-notes.md §6: chatDatas is an
// activity delta, so a fresh session is only told about rooms with recent traffic); the contacts come
// from POST /{agent}/friends/diff.json (docs/protocol-notes.md §6). Run this when a bridge deployment
// logs in but shows no rooms - it separates "identity revoked" from "nothing to bridge".
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/libhwang50/kakaobridge/kakao"
	"github.com/libhwang50/kakaobridge/loco"
)

func main() {
	tokens := flag.String("tokens", "tokens.json", "credential file; see docs/configuration.md")
	appver := flag.String("appver", "26.9.1", "client version to announce")
	noLoco := flag.Bool("no-loco", false, "skip the LOCO dial (REST only)")
	chatLogs := flag.String("chatlogs", "", "call MCHATLOGS for this chatId (history depth probe)")
	chatInfo := flag.String("chatinfo", "", "call ChatOnRoom for this chatId and print the response as hex (feed it to cmd/bsondump)")
	dumpRooms := flag.Bool("dump-rooms", false, "print the parsed chat list as JSON (what a portal builder would see)")
	flag.Parse()

	lg := log.New(os.Stderr, "", log.Ltime)
	a, err := loco.LoadAuth(*tokens, *appver, "ko")
	if err != nil {
		lg.Fatal(err)
	}
	plat := "Wd"
	if a.OS == "android" {
		plat = "An"
	}
	c := kakao.New(kakao.Auth{Token: a.Token, DeviceID: a.DUUID, Agent: a.OS,
		Version: a.AppVer, Platform: plat, Lang: a.Lang})

	p, err := c.SelfProfile()
	if err != nil {
		lg.Printf("own profile: %v", err)
	} else {
		fmt.Printf("account: userId=%d nickname=%q status=%q\n", a.UserID, p.Nickname, p.StatusMessage)
	}
	fs, err := c.Friends()
	if err != nil {
		lg.Printf("contacts: %v", err)
	} else {
		fmt.Printf("contacts: %d\n", len(fs))
		for _, f := range fs {
			fmt.Printf("  uid=%-10d aid=%-10d nick=%q suspended=%v memorial=%v\n",
				f.UserID, f.AccountID, f.Nickname, f.Suspended, f.Memorial)
		}
	}
	if *noLoco {
		return
	}
	sess, err := loco.Dial(loco.Config{Auth: a}, lg)
	if err != nil {
		lg.Fatalf("loco: %v", err)
	}
	defer sess.Close()
	st, err := loco.ParseLoginlistResponse(sess.LastLoginlist)
	if err != nil {
		lg.Fatal(err)
	}
	fmt.Printf("loco: rooms=%d cursor=%d minLogId=%d eof=%v deleted=%d\n",
		len(st.Rooms), st.LastTokenID, st.MinLogID, st.EOF, len(st.Deleted))
	if *chatLogs != "" {
		var id int64
		if _, err := fmt.Sscanf(*chatLogs, "%d", &id); err != nil {
			fmt.Println("bad chatId:", err)
			return
		}
		// "chatId" or "chatId:since" - since is a logId to page from (0 = newest window).
		since := int64(0)
		if i := strings.Index(*chatLogs, ":"); i >= 0 {
			if _, err := fmt.Sscanf((*chatLogs)[i+1:], "%d", &since); err != nil {
				fmt.Println("bad since:", err)
				return
			}
		}
		f, err := sess.MChatLogs([]int64{id}, []int64{since})
		if err != nil {
			fmt.Println("MCHATLOGS:", err)
		} else {
			fmt.Printf("MCHATLOGS status=%d bodylen=%d hex=%s\n", f.Status, len(f.Body), hex.EncodeToString(f.Body))
		}
	}
	if *chatInfo != "" {
		var id int64
		if _, err := fmt.Sscanf(*chatInfo, "%d", &id); err != nil {
			fmt.Println("bad chatId:", err)
			return
		}
		f, err := sess.ChatOnRoom(id)
		if err != nil {
			fmt.Println("ChatOnRoom:", err)
		} else {
			fmt.Printf("ChatOnRoom status=%d bodylen=%d hex=%s\n", f.Status, len(f.Body), hex.EncodeToString(f.Body))
		}
	}
	if *dumpRooms {
		enc, err := json.MarshalIndent(st.Rooms, "", "  ")
		if err != nil {
			fmt.Println("marshal rooms:", err)
		} else {
			fmt.Println(string(enc))
		}
	}
	for _, r := range st.Rooms {
		fmt.Printf("  %-11s chatId=%-16d lastLogId=%d preview=%q\n",
			r.Type, r.ChatID, r.LastLog, oneline(r.Preview))
	}
	for _, id := range st.Deleted {
		fmt.Printf("  deleted chatId=%d\n", id)
	}
}

func oneline(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i] + "…"
		}
	}
	if len(s) > 60 {
		return s[:60] + "…"
	}
	return s
}
