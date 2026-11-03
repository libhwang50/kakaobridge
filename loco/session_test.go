// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

// The commit bodies below are the difference between a photo, a video and a file reaching KakaoTalk, and
// none of their field names are the bridge's invention: they are what the shipped Android client sends
// (docs/protocol-notes.md §9-§10). A test that decodes what the session actually puts on the
// wire is what keeps a later refactor from quietly renaming `hfac` or adding a plausible-looking field.

import (
	"bufio"
	"io"
	"log"
	"net"
	"testing"
)

// pipeSession is a Session with a fake server: enough of the real one (connection, pending map) for a
// request to be sent and answered, so the bytes it produces can be read back.
func pipeSession(t *testing.T) (*Session, *bufio.Reader) {
	t.Helper()
	client, server := net.Pipe()
	s := &Session{conn: client, pending: map[uint32]chan Frame{}, Events: make(chan Frame, 8), log: log.New(io.Discard, "", 0)}
	t.Cleanup(func() { s.Close(); server.Close() })
	return s, bufio.NewReader(server)
}

// ask sends one request in the background and returns the body it put on the wire, after answering it so
// the caller does not block.
func ask(t *testing.T, s *Session, r *bufio.Reader, do func() (Frame, error)) []Elem {
	t.Helper()
	type res struct {
		f   Frame
		err error
	}
	done := make(chan res, 1)
	go func() { f, err := do(); done <- res{f, err} }()

	f, err := readFrame(r)
	if err != nil {
		t.Fatalf("reading the request the session sent: %v", err)
	}
	elems, _, err := DecodeDoc(f.Body, 0)
	if err != nil {
		t.Fatalf("decoding the %s body: %v", f.Cmd, err)
	}
	s.mu.Lock()
	ch, ok := s.pending[f.PID]
	s.mu.Unlock()
	if ok {
		ch <- Frame{PID: f.PID, Cmd: f.Cmd, Body: EncodeDoc([]Elem{{Key: "status", Type: tInt32, Val: int32(0)}})}
	}
	if f.Cmd != "WRITEMEDIA" {
		t.Fatalf("expected WRITEMEDIA, got %q", f.Cmd)
	}
	return elems
}

func docField(t *testing.T, elems []Elem, key string) []Elem {
	t.Helper()
	for _, e := range elems {
		if e.Key == key {
			doc, ok := e.Val.([]Elem)
			if !ok {
				t.Fatalf("%s is not a document: %T", key, e.Val)
			}
			return doc
		}
	}
	t.Fatalf("body has no %q document: keys %v", key, keysOf(elems))
	return nil
}

func keysOf(elems []Elem) []string {
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		out = append(out, e.Key)
	}
	return out
}

func wantField(t *testing.T, doc []Elem, key string, want any) {
	t.Helper()
	for _, e := range doc {
		if e.Key == key {
			if e.Val != want {
				t.Errorf("%s = %v (%T), want %v (%T)", key, e.Val, e.Val, want, want)
			}
			return
		}
	}
	t.Errorf("document is missing %s; it has %v", key, keysOf(doc))
}

func rejectField(t *testing.T, doc []Elem, key string) {
	t.Helper()
	for _, e := range doc {
		if e.Key == key {
			t.Errorf("document carries %s = %v; the client does not send it", key, e.Val)
			return
		}
	}
}

func TestPhotoCommitBody(t *testing.T) {
	s, r := pipeSession(t)
	elems := ask(t, s, r, func() (Frame, error) {
		return s.WriteMedia(42, 7, "res/vk/photo.png", "vk", "1:deadbeef", 1200, 800)
	})
	wantField(t, elems, "chatId", int64(42))
	wantField(t, elems, "msgId", int64(7))
	wantField(t, elems, "type", int32(2))
	wantField(t, elems, "scope", int32(1))
	wantField(t, elems, "extra", `{"cmt":""}`)
	photo := docField(t, elems, "photo")
	wantField(t, photo, "resource", "res/vk/photo.png")
	wantField(t, photo, "resourceType", "vk")
	wantField(t, photo, "hfac", "1:deadbeef") // the ticket's uploadKey, under the name the wire uses
	wantField(t, photo, "width", int32(1200))
	wantField(t, photo, "height", int32(800))
}

func TestVideoCommitBodyNamesOnlyTheObject(t *testing.T) {
	s, r := pipeSession(t)
	elems := ask(t, s, r, func() (Frame, error) {
		return s.WriteVideo(42, 7, "res/vk/clip.mp4", "vk", "1:cafe", VideoCommitOptions{})
	})
	wantField(t, elems, "type", int32(3))
	video := docField(t, elems, "video")
	wantField(t, video, "resource", "res/vk/clip.mp4")
	wantField(t, video, "resourceType", "vk")
	wantField(t, video, "hfac", "1:cafe")
	// The shipped client sends nothing else: size, duration and the transcoded copy come back computed.
	rejectField(t, video, "width")
	rejectField(t, video, "height")
	rejectField(t, video, "duration")
}

func TestFileCommitBodyCarriesChecksumAndName(t *testing.T) {
	s, r := pipeSession(t)
	elems := ask(t, s, r, func() (Frame, error) {
		return s.WriteFile(42, 7, "res/vk/x.zip", "vk", "1:beef", "a74ab2742db93b4ec9a7cd3838f1d6c0a25b09ac", "window_dump.xml")
	})
	wantField(t, elems, "type", int32(18))
	file := docField(t, elems, "file")
	wantField(t, file, "resource", "res/vk/x.zip")
	wantField(t, file, "resourceType", "vk")
	wantField(t, file, "hfac", "1:beef")
	// sha1 of the served bytes, as the client sends it (verified against the file Kakao later served).
	wantField(t, file, "checksum", "a74ab2742db93b4ec9a7cd3838f1d6c0a25b09ac")
	wantField(t, file, "name", "window_dump.xml") // shown in the chat, and repeated in the message text
	// A file commit has no extra document; a photo's comment field has nothing to attach to.
	for _, e := range elems {
		if e.Key == "extra" {
			t.Errorf("file commit carries extra=%v; the client omits it", e.Val)
		}
	}
}
