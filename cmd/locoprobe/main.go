// SPDX-License-Identifier: AGPL-3.0-or-later

// Command locoprobe calls one LOCO verb with a hand-written body and prints the response.
//
// It exists because the shipped clients' verb vocabulary is readable in the APK
// the mobile client ships a table of every LOCO verb while the request bodies are not
// always obvious, and a verb that answers status 0 tells us the body shape is right. LOCO is strict
// about BSON types (a 0x09 array where it wants 0x04 gets status -300), so types are spelled out.
//
//	locoprobe -tokens tokens.json \
//	    -call 'LCHATLIST chatId:i64=0 limitCount:i32=50'
//	locoprobe -call 'GETMEM chatId:i64=482943465622166' -call 'CHATINFO chatId:i64=482943465622166'
//	locoprobe -ship '482943465622166:/tmp/ship_probe.jpg'   # media upload ticket (SHIP)
//
// Field order is the order given; that matters for some verbs. Response bodies are printed as hex so
// they can be piped into cmd/bsondump.
package main

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"flag"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/libhwang50/kakaobridge/kakao"
	"github.com/libhwang50/kakaobridge/loco"
)

type calls []string

func (c *calls) String() string     { return strings.Join(*c, " ") }
func (c *calls) Set(v string) error { *c = append(*c, v); return nil }

// probeDims is read inside sendMedia, so it is registered at package level.
var probeDims = flag.String("dims", "", `offer width x height in a video commit, e.g. "320x240" (for probing what the server requires)`)

func main() {
	tokens := flag.String("tokens", "tokens.json", "credential file; see docs/configuration.md")
	keep := flag.Int("keep", 0, "hold the session open this many seconds after the calls")
	write := flag.String("write", "", `send a message first: "chatId:text" (proves the write path and triggers a push to other devices)`)
	read := flag.String("read", "", `report a read position: "chatId:logId" (NOTIREAD; Kakao then pushes DECUNREAD)`)
	ship := flag.String("ship", "", `ask for a media upload ticket: "chatId:/path/to/file.jpg" (SHIP)`)
	sendPhoto := flag.String("sendphoto", "", `send a real photo: "chatId:/path/to/file.png" (prepare + offset-up upload + WRITEMEDIA)`)
	sendVideo := flag.String("sendvideo", "", `send a real video: "chatId:/path/to/file.mp4" (same path, video commit body)`)
	sendFile := flag.String("sendfile", "", `send a real file: "chatId:/path/to/file.zip" (same path, file commit body with checksum and name)`)
	flag.Parse()

	var specs calls
	// -call is repeatable; flag.Parse has no repeatable-string type, so read them from args after --.
	specs = append(specs, flag.Args()...)

	a, err := loco.LoadAuth(*tokens, "26.9.1", "ko")
	if err != nil {
		log.Fatalf("load tokens: %v", err)
	}
	sess, err := loco.Dial(loco.Config{Auth: a}, log.New(os.Stderr, "", log.Ltime))
	if err != nil {
		log.Fatalf("loco dial: %v", err)
	}
	defer sess.Close()
	if *write != "" {
		chatStr, text, ok := strings.Cut(*write, ":")
		if !ok {
			log.Fatalf("-write wants chatId:text")
		}
		var id int64
		if _, err := fmt.Sscanf(chatStr, "%d", &id); err != nil {
			log.Fatalf("bad chatId: %v", err)
		}
		f, err := sess.WriteMsg(id, text)
		if err != nil {
			log.Fatalf("WRITE: %v", err)
		}
		fmt.Printf("WRITE status=%d inner=%d bodylen=%d hex=%s\n", f.Status, loco.InnerStatus(f.Body), len(f.Body), hex.EncodeToString(f.Body))
	}
	for _, m := range []struct{ kind, spec string }{
		{"image", *sendPhoto}, {"video", *sendVideo}, {"file", *sendFile},
	} {
		if m.spec != "" {
			sendMedia(a, sess, m.kind, m.spec)
		}
	}
	if *ship != "" {
		chatStr, path, ok := strings.Cut(*ship, ":")
		if !ok {
			log.Fatalf("-ship wants chatId:/path/to/file")
		}
		var id int64
		if _, err := fmt.Sscanf(chatStr, "%d", &id); err != nil {
			log.Fatalf("bad chatId: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatalf("read %s: %v", path, err)
		}
		sum := sha1.Sum(data)
		ext := ""
		if i := strings.LastIndexByte(path, '.'); i >= 0 {
			ext = strings.ToLower(path[i+1:])
		}
		f, err := sess.Ship(id, 2, int64(len(data)), hex.EncodeToString(sum[:]), ext)
		if err != nil {
			log.Fatalf("SHIP: %v", err)
		}
		fmt.Printf("SHIP file=%s bytes=%d sha1=%s ext=%s\n", path, len(data), hex.EncodeToString(sum[:]), ext)
		fmt.Printf("SHIP status=%d inner=%d bodylen=%d hex=%s\n", f.Status, loco.InnerStatus(f.Body), len(f.Body), hex.EncodeToString(f.Body))
	}
	for _, spec := range specs {
		if err := probe(sess, spec); err != nil {
			fmt.Printf("%s: %v\n", spec, err)
		}
	}
	if *read != "" {
		chatStr, wmStr, ok := strings.Cut(*read, ":")
		if !ok {
			log.Fatalf("-read wants chatId:logId")
		}
		var chatID, wm int64
		if _, err := fmt.Sscanf(chatStr, "%d", &chatID); err != nil {
			log.Fatalf("bad chatId: %v", err)
		}
		if _, err := fmt.Sscanf(wmStr, "%d", &wm); err != nil {
			log.Fatalf("bad watermark: %v", err)
		}
		f, err := sess.NotiRead(chatID, wm)
		if err != nil {
			log.Fatalf("NOTIREAD: %v", err)
		}
		fmt.Printf("NOTIREAD status=%d inner=%d bodylen=%d hex=%s\n", f.Status, loco.InnerStatus(f.Body), len(f.Body), hex.EncodeToString(f.Body))
	}
	if *keep > 0 {
		// Pushes are printed rather than dropped: they are the only way to see the server's own
		// frames (DECUNREAD and friends) and keep their bodies for tests.
		deadline := time.After(time.Duration(*keep) * time.Second)
		for {
			select {
			case <-deadline:
				return
			case f, ok := <-sess.Events:
				if !ok {
					fmt.Println("push stream closed")
					return
				}
				fmt.Printf("PUSH cmd=%s pid=%d status=%d bodylen=%d hex=%s\n",
					f.Cmd, f.PID, f.Status, len(f.Body), hex.EncodeToString(f.Body))
			}
		}
	}
}

func probe(sess *loco.Session, spec string) error {
	parts := strings.Fields(spec)
	if len(parts) == 0 {
		return fmt.Errorf("empty call")
	}
	verb := parts[0]
	elems := []loco.Elem{}
	for _, p := range parts[1:] {
		e, err := parseField(p)
		if err != nil {
			return err
		}
		elems = append(elems, e)
	}
	f, err := sess.Request(verb, loco.EncodeDoc(elems), 15*time.Second)
	if err != nil {
		return fmt.Errorf("%s: %w", verb, err)
	}
	fmt.Printf("%s status=%d inner=%d bodylen=%d hex=%s\n",
		verb, f.Status, loco.InnerStatus(f.Body), len(f.Body), hex.EncodeToString(f.Body))
	return nil
}

// parseField reads "key:type=value"; types are i32, i64, str, bool, or i64list.
func parseField(p string) (loco.Elem, error) {
	key, rest, ok := strings.Cut(p, ":")
	if !ok {
		return loco.Elem{}, fmt.Errorf("%q: expected key:type=value", p)
	}
	typ, raw, ok := strings.Cut(rest, "=")
	if !ok {
		return loco.Elem{}, fmt.Errorf("%q: expected key:type=value", p)
	}
	switch typ {
	case "i32":
		n, err := strconv.ParseInt(raw, 10, 32)
		return loco.Elem{Key: key, Type: 0x10, Val: int32(n)}, err
	case "i64":
		n, err := strconv.ParseInt(raw, 10, 64)
		return loco.Elem{Key: key, Type: 0x12, Val: n}, err
	case "str":
		return loco.Elem{Key: key, Type: 0x02, Val: raw}, nil
	case "bool":
		b, err := strconv.ParseBool(raw)
		return loco.Elem{Key: key, Type: 0x08, Val: b}, err
	case "i64list":
		vals := []any{}
		for _, s := range strings.Split(raw, ",") {
			if s == "" {
				continue
			}
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				return loco.Elem{}, err
			}
			vals = append(vals, n)
		}
		return loco.Elem{Key: key, Type: 0x04, Val: loco.Array(vals...)}, nil
	default:
		return loco.Elem{}, fmt.Errorf("%q: unknown type %q", p, typ)
	}
}

// probeDimensions reads the size out of the image itself, which is what the shipped client puts in the
// photo body so Kakao can lay out the bubble before the image arrives.
func probeDimensions(data []byte) (int, int) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return img.Bounds().Dx(), img.Bounds().Dy()
}

// sendMedia runs the whole outbound media path for one kind, so the gateway's answer and the commit body
// can be read side by side (docs/protocol-notes.md §9-§10). The kinds differ only in the mediaType
// asked for and the commit body, which is exactly what a probe should show.
func sendMedia(a *loco.Auth, sess *loco.Session, kind, spec string) {
	chatStr, path, ok := strings.Cut(spec, ":")
	if !ok {
		log.Fatalf("-%s wants chatId:/path/to/file", kind)
	}
	var id int64
	if _, err := fmt.Sscanf(chatStr, "%d", &id); err != nil {
		log.Fatalf("bad chatId: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read: %v", err)
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	rc := kakao.New(kakao.Auth{
		Token: a.Token, DeviceID: a.DUUID, Agent: a.OS, Version: a.AppVer,
		Platform: map[string]string{"android": "An"}[a.OS], Lang: a.Lang,
	})
	ctx := context.Background()
	ticket, err := rc.PrepareMedia(ctx, id, kind, int64(len(data)), ext)
	if err != nil {
		log.Fatalf("prepare(%s): %v", kind, err)
	}
	fmt.Printf("PREPARE kind=%s resource=%s uploadKey=%s type=%s chunk=%d\n",
		kind, ticket.Resource, ticket.UploadKey, ticket.ResourceType, ticket.ChunkSize)
	upBody, err := rc.UploadMedia(ctx, ticket, data, kind+"/"+ext)
	if err != nil {
		log.Fatalf("upload: %v", err)
	}
	fmt.Printf("UPLOAD_REPLY %s\n", trim(string(upBody), 400))
	fmt.Printf("UPLOADED %d bytes in %d chunk(s)\n", len(data), (len(data)-1)/max(1, int(ticket.ChunkSize))+1)

	var f loco.Frame
	switch kind {
	case "image":
		w, h := probeDimensions(data)
		f, err = sess.WriteMedia(id, loco.ClientMsgID(), ticket.Resource, ticket.ResourceType, ticket.UploadKey, int32(w), int32(h))
	case "video":
		// The plain body is the one the shipped client sends. If the gateway is still busy with the
		// bytes, asking again after a pause is the difference between a race and a rejection, and if the
		// server wants the dimensions, offering them shows that too. Every attempt prints its status, so
		// the probe says which body is accepted rather than only the last failure.
		o := loco.VideoCommitOptions{}
		if *probeDims != "" {
			var w, h int
			if _, err := fmt.Sscanf(*probeDims, "%dx%d", &w, &h); err != nil {
				log.Fatalf("-dims wants WxH: %v", err)
			}
			o.Width, o.Height = int32(w), int32(h)
		}
		for i, wait := range []time.Duration{0, 4 * time.Second, 8 * time.Second} {
			if wait > 0 {
				time.Sleep(wait)
			}
			f, err = sess.WriteVideo(id, loco.ClientMsgID(), ticket.Resource, ticket.ResourceType, ticket.UploadKey, o)
			if err == nil && loco.InnerStatus(f.Body) == 0 {
				fmt.Printf("VIDEO ok on attempt %d (dims=%v)\n", i, *probeDims)
				break
			}
			fmt.Printf("VIDEO attempt=%d waited=%s dims=%q inner=%d\n", i, wait, *probeDims, loco.InnerStatus(f.Body))
		}
	case "file":
		sum := sha1.Sum(data)
		f, err = sess.WriteFile(id, loco.ClientMsgID(), ticket.Resource, ticket.ResourceType, ticket.UploadKey,
			hex.EncodeToString(sum[:]), filepath.Base(path))
	}
	if err != nil {
		log.Fatalf("WRITEMEDIA(%s): %v", kind, err)
	}
	wr, err := loco.ParseWriteResponse(f.Body)
	if err != nil {
		fmt.Printf("WRITEMEDIA kind=%s status=%d inner=%d hex=%s\n", kind, f.Status, loco.InnerStatus(f.Body), hex.EncodeToString(f.Body))
		log.Fatalf("decoding the response: %v", err)
	}
	fmt.Printf("WRITEMEDIA ok kind=%s chatId=%d logId=%d\n", kind, wr.ChatID, wr.LogID)
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
