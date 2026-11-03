// SPDX-License-Identifier: AGPL-3.0-or-later

// Command kkread shows where each member of a chat has read to, and can set our own position.
//
// It exists to measure a question the bridge's behaviour depends on: is a Kakao watermark stored per
// member, or is it one value per chat? CHATONROOM answers with `w` next to `a`, and the first response
// ever decoded had both entries equal, which proves nothing. Running this before and after a phone reads
// a chat does: if only one entry moves, per-member read state is real and durable, and read receipts
// could be seeded on room creation instead of only followed.
//
//	kkread -tokens tokens.json 482943465622166 482943007629834
//	kkread -set 482943007629834:3961726533481510913 482943007629834
//
// Like the other probe tools it uses the same device id as the running bridge, so the bridge gets
// KICKOUT and reconnects; keep it short.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/libhwang50/kakaobridge/loco"
)

func main() {
	tokens := flag.String("tokens", "tokens.json", "credential file; see docs/configuration.md")
	set := flag.String("set", "", `set our own read position first: "chatId:logId" (NOTIREAD)`)
	flag.Parse()

	chatIDs := []int64{}
	for _, a := range flag.Args() {
		id, err := strconv.ParseInt(a, 10, 64)
		if err != nil {
			log.Fatalf("bad chatId %q: %v", a, err)
		}
		chatIDs = append(chatIDs, id)
	}
	setChat, setLog := int64(0), int64(0)
	if *set != "" {
		chatStr, wmStr, ok := strings.Cut(*set, ":")
		if !ok {
			log.Fatalf("-set wants chatId:logId")
		}
		var err error
		if setChat, err = strconv.ParseInt(chatStr, 10, 64); err != nil {
			log.Fatalf("bad chatId: %v", err)
		}
		if setLog, err = strconv.ParseInt(wmStr, 10, 64); err != nil {
			log.Fatalf("bad logId: %v", err)
		}
		chatIDs = append(chatIDs, setChat)
	}
	if len(chatIDs) == 0 {
		flag.Usage()
		os.Exit(2)
	}

	a, err := loco.LoadAuth(*tokens, "26.9.1", "ko")
	if err != nil {
		log.Fatalf("load tokens: %v", err)
	}
	sess, err := loco.Dial(loco.Config{Auth: a}, log.New(os.Stderr, "", log.Ltime))
	if err != nil {
		log.Fatalf("loco dial: %v", err)
	}
	defer sess.Close()

	if setChat != 0 {
		f, err := sess.NotiRead(setChat, setLog)
		if err != nil {
			log.Fatalf("NOTIREAD: %v", err)
		}
		fmt.Printf("NOTIREAD chatId=%d watermark=%d inner=%d\n", setChat, setLog, loco.InnerStatus(f.Body))
	}

	seen := map[int64]bool{}
	for _, chatID := range chatIDs {
		if seen[chatID] {
			continue
		}
		seen[chatID] = true
		if err := show(sess, chatID, a.UserID); err != nil {
			fmt.Printf("chat %d: %v\n", chatID, err)
		}
	}
}

func show(sess *loco.Session, chatID, selfID int64) error {
	orf, err := sess.ChatOnRoom(chatID)
	if err != nil {
		return fmt.Errorf("CHATONROOM: %w", err)
	}
	ri, err := loco.ParseChatOnRoomResponse(orf.Body)
	if err != nil {
		return err
	}
	cif, err := sess.ChatInfo(chatID)
	lastSeen := int64(0)
	if err == nil {
		if cs, cerr := loco.ParseChatInfoResponse(cif.Body); cerr == nil {
			lastSeen = cs.LastSeenLog
		}
	} else {
		fmt.Printf("chat %d: CHATINFO: %v\n", chatID, err)
	}

	fmt.Printf("chat %d type=%s lastLogId=%d selfLastSeenLogId=%d\n",
		chatID, ri.Type, ri.LastLogID, lastSeen)
	for _, m := range ri.ReadMarkers {
		who := "other"
		if m.UserID == selfID {
			who = "self"
		}
		atLast := "no"
		if m.LogID == ri.LastLogID {
			atLast = "yes"
		}
		fmt.Printf("  %-5s user=%-10d watermark=%d at_last_msg=%s\n",
			who, m.UserID, m.LogID, atLast)
	}
	if len(ri.ReadMarkers) == 0 {
		fmt.Printf("  no per-member watermarks (members=%d maxWatermark=%d)\n", len(ri.Members), ri.Watermark)
	}
	return nil
}
