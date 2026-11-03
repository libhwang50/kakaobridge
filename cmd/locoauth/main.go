// SPDX-License-Identifier: AGPL-3.0-or-later

// Command locoauth authenticates LOCO with the bridge's OWN credentials and reports what the
// servers answer. It is the check that the CHECKIN/LOGINLIST builders in loco.Auth are accepted:
//
//	go run ./cmd/locoauth -tokens tokens.json
//	go run ./cmd/locoauth -harvest harvest.json        # old replay path, for comparison
//
// -ping sends PING after login; -to <chatId> sends a WRITE to prove the session is usable.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/libhwang50/kakaobridge/loco"
)

func main() {
	tokens := flag.String("tokens", "tokens.json", "credential file; see docs/configuration.md")
	harv := flag.String("harvest", "", "instead of own credentials, replay these captured bodies")
	appVer := flag.String("appver", "26.9.1", "app version to announce (must match the build that minted the token)")
	lang := flag.String("lang", "ko", "language code to announce")
	ticket := flag.String("ticket", "ticket-loco.kakao.com", "ticket host")
	doPing := flag.Bool("ping", false, "send PING after login")
	keep := flag.Int("keep", 0, "hold the session open for N seconds (PING every 25s), logging server pushes: this is how to test whether a LIVE session is what makes the device appear on the phone")
	dump := flag.String("dump", "", "write the LOGINLIST response body here (a bare BSON doc; decode it with cmd/bsondump)")
	to := flag.Int64("to", 0, "chatId: send a WRITE to prove the session works")
	msg := flag.String("msg", "locoauth self-test", "WRITE text")
	flag.Parse()

	lg := log.New(os.Stderr, "", log.Ltime)
	cfg := loco.Config{TicketHost: *ticket}
	if *harv != "" {
		var h struct {
			Checkin   string `json:"checkin_body"`
			Loginlist string `json:"loginlist_body"`
		}
		b, err := os.ReadFile(*harv)
		if err != nil {
			lg.Fatal(err)
		}
		if err := json.Unmarshal(b, &h); err != nil {
			lg.Fatal(err)
		}
		cfg.CheckinHex, cfg.LoginlistHex = h.Checkin, h.Loginlist
		lg.Printf("replaying captured bodies from %s", *harv)
	} else {
		a, err := loco.LoadAuth(*tokens, *appVer, *lang)
		if err != nil {
			lg.Fatal(err)
		}
		cfg.Auth = a
	}

	t0 := time.Now()
	s, err := loco.Dial(cfg, lg)
	if err != nil {
		lg.Fatalf("LOCO auth failed: %v", err)
	}
	defer s.Close()
	lg.Printf("SESSION OK in %s", time.Since(t0).Truncate(time.Millisecond))
	if *dump != "" {
		// The LOGINLIST response body is captured right after it is received; grab it off the
		// session's own log by asking for it again over a throwaway connection is overkill -
		// instead the session exposes it via Session.LastLoginlist (set in loco.Dial).
		if err := os.WriteFile(*dump, s.LastLoginlist, 0o600); err != nil {
			lg.Printf("dump: %v", err)
		} else {
			lg.Printf("wrote LOGINLIST response (%d B) to %s", len(s.LastLoginlist), *dump)
		}
	}
	if *keep > 0 {
		lg.Printf("holding session open for %ds (PING every 25s); watch the phone now", *keep)
		end := time.Now().Add(time.Duration(*keep) * time.Second)
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for time.Now().Before(end) {
			select {
			case f := <-s.Events:
				elems, _, _ := loco.DecodeDoc(f.Body, 0)
				lg.Printf("PUSH pid=%d cmd=%s inner=%v keys=%v", f.PID, f.Cmd,
					loco.InnerStatus(f.Body), keysOf(elems))
			case <-ping.C:
				if _, err := s.Request("PING", nil, 6*time.Second); err != nil {
					lg.Printf("ping: %v", err)
				} else {
					lg.Printf("ping ok (session still live)")
				}
			}
		}
	}
	if *doPing {
		f, err := s.Request("PING", nil, 6*time.Second)
		lg.Printf("PING -> frame.status=%d inner=%v bodylen=%d err=%v",
			f.Status, loco.InnerStatus(f.Body), len(f.Body), err)
	}
	if *to != 0 {
		f, err := s.WriteMsg(*to, *msg)
		if err != nil {
			lg.Printf("WRITE error: %v", err)
		} else {
			elems, _, _ := loco.DecodeDoc(f.Body, 0)
			lg.Printf("WRITE -> frame.status=%d inner=%v logId=%v sendAt=%v",
				f.Status, loco.Get(elems, "status"), loco.Get(elems, "logId"), loco.Get(elems, "sendAt"))
		}
	}
	fmt.Println("done")
}

// hexBytes prints up to 96 bytes of a frame body as hex (hexdump-style), enough to identify the
// flagged record's header without a dump tool.
func hexBytes(b []byte) string {
	const n = 96
	if len(b) > n {
		b = b[:n]
	}
	var s string
	for i, c := range b {
		if i%16 == 0 {
			s += fmt.Sprintf("\n  %04x ", i)
		}
		s += fmt.Sprintf("%02x ", c)
	}
	return s
}

func keysOf(e []loco.Elem) []string {
	var out []string
	for _, x := range e {
		out = append(out, x.Key)
	}
	return out
}
