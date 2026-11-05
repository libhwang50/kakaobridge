// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
	"time"
)

type Frame struct {
	PID    uint32
	Status uint16
	Cmd    string
	BType  byte
	Body   []byte
}

func (f Frame) Encode() []byte {
	cmd := make([]byte, 11)
	copy(cmd, f.Cmd)
	// cmd field: NUL padded; keep embedded NULs as-is
	out := make([]byte, 22+len(f.Body))
	binary.LittleEndian.PutUint32(out[0:], f.PID)
	binary.LittleEndian.PutUint16(out[4:], f.Status)
	copy(out[6:], cmd)
	out[17] = f.BType
	binary.LittleEndian.PutUint32(out[18:], uint32(len(f.Body)))
	copy(out[22:], f.Body)
	return out
}

func readFrame(r io.Reader) (Frame, error) {
	var hdr [22]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Frame{}, err
	}
	n := binary.LittleEndian.Uint32(hdr[18:])
	if n > 1<<22 {
		return Frame{}, fmt.Errorf("frame too large: %d", n)
	}
	body := make([]byte, n)
	if n > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			return Frame{}, err
		}
	}
	cmd := string(hdr[6:17])
	for len(cmd) > 0 && cmd[len(cmd)-1] == 0 {
		cmd = cmd[:len(cmd)-1]
	}
	return Frame{
		PID:    binary.LittleEndian.Uint32(hdr[0:]),
		Status: binary.LittleEndian.Uint16(hdr[4:]),
		Cmd:    cmd,
		BType:  hdr[17],
		Body:   body,
	}, nil
}

type Config struct {
	TicketHost   string
	LocoSNI      string
	CheckinHex   string // replayed captured bodies; used only when Auth is nil
	LoginlistHex string
	// Auth, when set, makes the session authenticate itself: CHECKIN/LOGINLIST are built from the
	// bridge's own userId/access_token/device_uuid instead of replaying a capture. See loco.Auth.
	Auth *Auth
}

type Session struct {
	cfg  Config
	conn net.Conn
	br   *bufio.Reader

	// LastLoginlist holds the raw LOGINLIST response body. The server answers with a bare BSON
	// document (status, userId, revision, revisionInfo, chatDatas, delChatIds, minLogId,
	// lastTokenId, eof, …); cmd/locoauth -dump writes it and cmd/bsondump decodes it.
	LastLoginlist []byte

	mu      sync.Mutex
	nextPID uint32
	pending map[uint32]chan Frame

	Events chan Frame // server-initiated pushes
	log    *log.Logger
}

func Dial(cfg Config, lg *log.Logger) (*Session, error) {
	var chk, ll []byte
	var err error
	if cfg.Auth != nil {
		chk, ll = cfg.Auth.CheckinBody(), cfg.Auth.LoginlistBody()
		lg.Printf("authenticating as userId=%d agent=%s appVer=%s duuid=%s…",
			cfg.Auth.UserID, cfg.Auth.OS, cfg.Auth.AppVer, cfg.Auth.DUUID[:12])
	} else {
		if chk, err = hex.DecodeString(cfg.CheckinHex); err != nil {
			return nil, fmt.Errorf("checkin hex: %w", err)
		}
		if ll, err = hex.DecodeString(cfg.LoginlistHex); err != nil {
			return nil, fmt.Errorf("loginlist hex: %w", err)
		}
	}
	if cfg.TicketHost == "" {
		cfg.TicketHost = "ticket-loco.kakao.com"
	}
	if cfg.LocoSNI == "" {
		cfg.LocoSNI = "loco.kakao.com"
	}
	tlsCfg := &tls.Config{ServerName: cfg.TicketHost, InsecureSkipVerify: true}
	tc, err := tls.Dial("tcp", cfg.TicketHost+":995", tlsCfg)
	if err != nil {
		return nil, fmt.Errorf("ticket dial: %w", err)
	}
	defer tc.Close()
	br := bufio.NewReader(tc)
	if _, err := tc.Write(Frame{PID: 1, Cmd: "CHECKIN", Body: chk}.Encode()); err != nil {
		return nil, err
	}
	tc.SetReadDeadline(time.Now().Add(10 * time.Second))
	var resp Frame
	for {
		resp, err = readFrame(br)
		if err != nil {
			return nil, fmt.Errorf("checkin read: %w", err)
		}
		if resp.PID == 1 {
			break
		}
	}
	if s := InnerStatus(resp.Body); s != 0 {
		return nil, fmt.Errorf("CHECKIN inner status %d", s)
	}
	elems, _, err := DecodeDoc(resp.Body, 0)
	if err != nil {
		return nil, fmt.Errorf("checkin body: %w", err)
	}
	host, _ := Get(elems, "host").(string)
	port := 995
	if v, ok := Get(elems, "port").(int32); ok {
		port = int(v)
	}
	if host == "" {
		host = cfg.LocoSNI
	}
	lg.Printf("CHECKIN ok: loco=%s:%d", host, port)

	lc, err := tls.Dial("tcp", fmt.Sprintf("%s:%d", host, port),
		&tls.Config{ServerName: cfg.LocoSNI, InsecureSkipVerify: true})
	if err != nil {
		return nil, fmt.Errorf("loco dial: %w", err)
	}
	s := &Session{
		cfg: cfg, conn: lc, br: bufio.NewReader(lc),
		nextPID: 100, pending: map[uint32]chan Frame{},
		Events: make(chan Frame, 256),
		log:    lg,
	}
	// LOGINLIST
	ch := make(chan Frame, 1)
	s.pending[2] = ch
	if _, err := lc.Write(Frame{PID: 2, Cmd: "LOGINLIST", Body: ll}.Encode()); err != nil {
		lc.Close()
		return nil, err
	}
	lc.SetReadDeadline(time.Now().Add(10 * time.Second))
	lr, err := readFrame(s.br)
	if err != nil {
		lc.Close()
		return nil, fmt.Errorf("loginlist read: %w", err)
	}
	if lr.PID != 2 {
		lc.Close()
		return nil, fmt.Errorf("unexpected loginlist pid %d", lr.PID)
	}
	if st := InnerStatus(lr.Body); st != 0 {
		lc.Close()
		return nil, fmt.Errorf("LOGINLIST inner status %d", st)
	}
	lg.Printf("LOGINLIST ok bodylen=%d", len(lr.Body))
	s.LastLoginlist = lr.Body
	if cfg.Auth != nil {
		// Keep the push cursor the server hands back, so a restart can resume instead of
		// starting from 0 (which asks for nothing historic).
		if elems, _, err := DecodeDoc(lr.Body, 0); err == nil {
			for _, k := range []string{"lastTokenId", "tokenId"} {
				switch v := Get(elems, k).(type) {
				case int64:
					cfg.Auth.LastToken = v
					lg.Printf("resume cursor %s=%d", k, v)
				case int32:
					cfg.Auth.LastToken = int64(v)
					lg.Printf("resume cursor %s=%d", k, v)
				}
			}
		}
	}
	lc.SetReadDeadline(time.Time{})
	go s.readLoop()
	go s.pingLoop()
	return s, nil
}

func (s *Session) readLoop() {
	for {
		f, err := readFrame(s.br)
		if err != nil {
			s.log.Printf("readLoop exit: %v", err)
			close(s.Events)
			return
		}
		s.mu.Lock()
		ch, ok := s.pending[f.PID]
		if ok {
			delete(s.pending, f.PID)
		}
		s.mu.Unlock()
		if ok {
			ch <- f
		} else {
			select {
			case s.Events <- f:
			default:
				s.log.Printf("events overflow, dropped %s", f.Cmd)
			}
		}
	}
}

func (s *Session) pingLoop() {
	for {
		time.Sleep(25 * time.Second)
		if _, err := s.Request("PING", nil, 6*time.Second); err != nil {
			s.log.Printf("PING failed: %v", err)
			return
		}
	}
}

func (s *Session) newPID() uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.nextPID
	s.nextPID++
	if s.nextPID == 0x0FFFFFFF {
		s.nextPID = 100
	}
	return p
}

// Request sends cmd with body and waits for the matching response.
func (s *Session) Request(cmd string, body []byte, to time.Duration) (Frame, error) {
	pid := s.newPID()
	ch := make(chan Frame, 1)
	s.mu.Lock()
	s.pending[pid] = ch
	s.mu.Unlock()
	if _, err := s.conn.Write(Frame{PID: pid, Cmd: cmd, Body: body}.Encode()); err != nil {
		return Frame{}, err
	}
	select {
	case f := <-ch:
		return f, nil
	case <-time.After(to):
		s.mu.Lock()
		delete(s.pending, pid)
		s.mu.Unlock()
		return Frame{}, fmt.Errorf("%s timeout", cmd)
	}
}

func (s *Session) Close() error { return s.conn.Close() }

// high-level helpers ---------------------------------------------------------

func (s *Session) WriteMsg(chatID int64, msg string) (Frame, error) {
	return s.WriteRaw(chatID, msg, "", ClientMsgID())
}

// WriteRaw sends WRITE with an explicit client msgId. extra carries the message's styles, or "" for a
// plain message; see WriteExtra for what it looks like and why it is called that.
func (s *Session) WriteRaw(chatID int64, msg, extra string, msgID int64) (Frame, error) {
	elems := []Elem{
		{Key: "chatId", Type: tInt64, Val: chatID},
		{Key: "msg", Type: tString, Val: msg},
		{Key: "msgId", Type: tInt64, Val: msgID},
		{Key: "type", Type: tInt32, Val: int32(1)},
	}
	if extra != "" {
		elems = append(elems, Elem{Key: "extra", Type: tString, Val: extra})
	}
	elems = append(elems,
		Elem{Key: "silence", Type: tBool, Val: false},
		Elem{Key: "scope", Type: tInt32, Val: int32(1)},
	)
	body := EncodeDoc(elems)
	return s.Request("WRITE", body, 8*time.Second)
}

// Ship asks Kakao for a media upload ticket before a WRITEMEDIA commit. Field names, order and types
// are copied from a real mobile send (docs/protocol-notes.md §9): the key for the chat is "c", the
// size is int64, the media kind is int32 (2 = photo), and cs is the lowercase SHA-1 hex of the bytes.
// The answer carries where to upload (see GETTRAILER for the same endpoint shape).
func (s *Session) Ship(chatID int64, kind int32, size int64, sha1hex, ext string) (Frame, error) {
	body := EncodeDoc([]Elem{
		{Key: "c", Type: tInt64, Val: chatID},
		{Key: "s", Type: tInt64, Val: size},
		{Key: "t", Type: tInt32, Val: kind},
		{Key: "cs", Type: tString, Val: sha1hex},
		{Key: "e", Type: tString, Val: ext},
		{Key: "ex", Type: tString, Val: "{}"},
	})
	return s.Request("SHIP", body, 10*time.Second)
}

// StatusMediaNotReady is what a media commit answers while the gateway is still working on the uploaded
// bytes. Measured with a video (r53b): the first WRITEMEDIA after a complete upload got this, and the
// same body sent 4 seconds later was accepted. It is the meaning of the `needReady` flag a media ticket
// carries - photos and plain files answer 0 immediately, videos do not.
const StatusMediaNotReady = -306

// ClientMsgID is the message id the client proposes for a send; Kakao's answer assigns the logId, but
// every shipped client sends a msgId of its own (seen in a mobile WRITE and WRITEMEDIA), so both of our
// send paths do the same: a ms timestamp kept inside 31 bits.
func ClientMsgID() int64 { return time.Now().UnixMilli() % 2_000_000_000 }

// WriteMedia commits a photo that has already been uploaded to Kakao's storage (see kakao.PrepareMedia
// and kakao.UploadMedia). The message shape is the one a real mobile client sends (r52/r52d): a fixed
// preamble, then a document naming the object the gateway handed out - its resource path, its type
// ("vk"), and the upload key as `hfac`. Photo adds width/height, video adds nothing (Kakao reads size
// and duration off the file itself and serves a transcoded `talkv_high.mp4` back), and file adds the
// SHA-1 `checksum` and the `name` the sender chose. `extra.cmt` is sent empty because that is what the
// client sends: Kakao's composer has no caption, and a non-empty cmt was ignored by the server (r52e).
func (s *Session) WriteMedia(chatID, msgID int64, resource, resourceType, hfac string, width, height int32) (Frame, error) {
	return s.WriteMediaKind(chatID, msgID, 2, "photo", []Elem{
		{Key: "width", Type: tInt32, Val: width},
		{Key: "height", Type: tInt32, Val: height},
	}, resource, resourceType, hfac)
}

// VideoCommitOptions are the fields a client may add to a video commit beyond the object reference. The
// shipped Android client sends none of them (measured, r53), so the zero value is the shape the bridge
// uses; a probe sets them to find out whether the server ever requires one.
type VideoCommitOptions struct {
	Width, Height int32
	DurationMS    int64
}

// WriteVideo commits an uploaded video (Kakao message type 3). Only the object reference is named: the
// chat log that comes back has size, duration and a transcoded high-definition URL that the server
// produced on its own, so the bridge does not guess at them.
func (s *Session) WriteVideo(chatID, msgID int64, resource, resourceType, hfac string, o VideoCommitOptions) (Frame, error) {
	var doc []Elem
	if o.Width != 0 && o.Height != 0 {
		doc = append(doc, Elem{Key: "width", Type: tInt32, Val: o.Width}, Elem{Key: "height", Type: tInt32, Val: o.Height})
	}
	if o.DurationMS != 0 {
		doc = append(doc, Elem{Key: "duration", Type: tInt64, Val: o.DurationMS})
	}
	return s.WriteMediaKind(chatID, msgID, 3, "video", doc, resource, resourceType, hfac)
}

// WriteFile commits an uploaded file (Kakao message type 18). `checksum` is the SHA-1 of the bytes in
// hex - measured, not guessed: the client's value equals the SHA-1 of the bytes the CDN serves - and
// `name` is the file name the sender chose, which is what Kakao shows and what the chat log repeats in
// the message text. This commit carries no `extra` field at all, unlike photo and video.
func (s *Session) WriteFile(chatID, msgID int64, resource, resourceType, hfac, checksum, name string) (Frame, error) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: chatID},
		{Key: "msgId", Type: tInt64, Val: msgID},
		{Key: "type", Type: tInt32, Val: int32(18)},
		{Key: "scope", Type: tInt32, Val: int32(1)},
		{Key: "silence", Type: tBool, Val: false},
		{Key: "featureStat", Type: tString, Val: ""},
		{Key: "noSeen", Type: tBool, Val: false},
		{Key: "file", Type: tDoc, Val: []Elem{
			{Key: "resource", Type: tString, Val: resource},
			{Key: "resourceType", Type: tString, Val: resourceType},
			{Key: "hfac", Type: tString, Val: hfac},
			{Key: "checksum", Type: tString, Val: checksum},
			{Key: "name", Type: tString, Val: name},
		}},
	})
	return s.Request("WRITEMEDIA", body, 30*time.Second)
}

// WriteMediaKind writes a WRITEMEDIA body for an arbitrary kind: the preamble every kind shares, plus
// the document that names the uploaded object. The named methods above are the measured kinds; the generic
// form exists so a probe can try a body shape that has not been measured yet (and see the status code it
// gets back, which is how -306 for a bare video commit was found).
func (s *Session) WriteMediaKind(chatID, msgID int64, kind int32, docKey string, extraFields []Elem, resource, resourceType, hfac string) (Frame, error) {
	doc := []Elem{
		{Key: "resource", Type: tString, Val: resource},
		{Key: "resourceType", Type: tString, Val: resourceType},
		{Key: "hfac", Type: tString, Val: hfac},
	}
	doc = append(doc, extraFields...)
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: chatID},
		{Key: "msgId", Type: tInt64, Val: msgID},
		{Key: "type", Type: tInt32, Val: kind},
		{Key: "scope", Type: tInt32, Val: int32(1)},
		{Key: "silence", Type: tBool, Val: false},
		{Key: "featureStat", Type: tString, Val: ""},
		{Key: "noSeen", Type: tBool, Val: false},
		{Key: docKey, Type: tDoc, Val: doc},
		{Key: "extra", Type: tString, Val: `{"cmt":""}`},
	})
	return s.Request("WRITEMEDIA", body, 30*time.Second)
}

func (s *Session) MChatLogs(chatIDs []int64, sinces []int64) (Frame, error) {
	body := EncodeDoc([]Elem{
		{Key: "chatIds", Type: tArr, Val: Array(toI64(chatIDs)...)},
		{Key: "sinces", Type: tArr, Val: Array(toI64(sinces)...)},
	})
	return s.Request("MCHATLOGS", body, 10*time.Second)
}

func (s *Session) ChatOnRoom(chatID int64) (Frame, error) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: chatID},
		{Key: "token", Type: tInt32, Val: int32(time.Now().Unix())},
	})
	return s.Request("CHATONROOM", body, 8*time.Second)
}

// ChatInfo asks the server for one room: type, member counts, the last message, and display
// members with nicknames and profile image URLs. Unlike ChatOnRoom (a sync view: cursors and member
// ids) this is the verb that carries what a Matrix room needs to be created with.
func (s *Session) ChatInfo(chatID int64) (Frame, error) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: chatID},
	})
	return s.Request("CHATINFO", body, 8*time.Second)
}

// GetMem asks for a room's member list with profiles: nicknames, avatar URLs, status messages, and
// suspended/memorial flags. This is the source for ghost display names.
func (s *Session) GetMem(chatID int64) (Frame, error) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: chatID},
	})
	return s.Request("GETMEM", body, 8*time.Second)
}

// SyncLink asks for every Open Link this account has: the Open Chat rooms it joined or created, and
// its own open profile. The server does not filter by link id, so the caller picks the entry it wants
// (see ParseSyncLinkResponse).
func (s *Session) SyncLink() (Frame, error) {
	return s.Request("SYNCLINK", EncodeDoc(nil), 8*time.Second)
}

func (s *Session) NotiRead(chatID, watermark int64) (Frame, error) {
	body := EncodeDoc([]Elem{
		{Key: "chatId", Type: tInt64, Val: chatID},
		{Key: "watermark", Type: tInt64, Val: watermark},
		{Key: "notiRead", Type: tBool, Val: true},
	})
	return s.Request("NOTIREAD", body, 8*time.Second)
}

func toI64(xs []int64) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}
