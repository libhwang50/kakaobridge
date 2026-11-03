// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The gateway hands out one signed URL and a chunk size, and the client re-posts that URL once per
// chunk with an offset part, marking only the last one finalize=true. Both halves of that contract were
// read off a real send (docs/protocol-notes.md §10); this test pins what our uploader does with it.
func TestUploadMediaChunks(t *testing.T) {
	type chunk struct {
		offset     int64
		length     int
		finalize   bool
		partName   string
		filename   string
		ctype      string
		authorized string
	}
	var got []chunk
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "want POST", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, "bad multipart: "+err.Error(), http.StatusBadRequest)
			return
		}
		c := chunk{finalize: r.URL.Query().Get("finalize") == "true", authorized: r.Header.Get("authorization")}
		c.offset, _ = strconv.ParseInt(r.FormValue("offset"), 10, 64)
		files := r.MultipartForm.File["file_1"]
		if len(files) != 1 {
			http.Error(w, "want exactly one file_1 part", http.StatusBadRequest)
			return
		}
		c.partName = "file_1"
		c.filename = files[0].Filename
		c.ctype = files[0].Header.Get("Content-Type")
		f, err := files[0].Open()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		defer f.Close()
		buf := make([]byte, files[0].Size)
		n, _ := f.Read(buf)
		c.length = n
		got = append(got, c)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"status":"OK"}`)
	}))
	defer srv.Close()

	c := New(Auth{Token: "tok", DeviceID: "dev", Agent: "android", Version: "26.9.1"})
	data := make([]byte, 20)
	for i := range data {
		data[i] = byte(i)
	}
	ticket := &MediaTicket{
		URL:          srv.URL + "/offset-up/dir/name/?credential=x&expires=1&signature=y%3D&finalize=true",
		Resource:     "dir/name/photo.png",
		UploadKey:    "1:bc47dbe6",
		ResourceType: "vk",
		ChunkSize:    7,
	}
	if _, err := c.UploadMedia(context.Background(), ticket, data, "image/png"); err != nil {
		t.Fatalf("upload: %v", err)
	}
	want := []struct {
		offset, length int64
		finalize       bool
	}{
		{0, 7, false}, {7, 7, false}, {14, 6, true},
	}
	if int64(len(got)) != int64(len(want)) {
		t.Fatalf("chunks: got %d, want %d", len(got), len(want))
	}
	for i, w := range want {
		if got[i].offset != w.offset || int64(got[i].length) != w.length || got[i].finalize != w.finalize {
			t.Errorf("chunk %d: got offset %d len %d finalize %v, want offset %d len %d finalize %v",
				i, got[i].offset, got[i].length, got[i].finalize, w.offset, w.length, w.finalize)
		}
		if got[i].filename != "photo.png" || got[i].ctype != "image/png" {
			t.Errorf("chunk %d: file part is %q %q, want the resource name and the real mime type",
				i, got[i].filename, got[i].ctype)
		}
		if got[i].authorized != "tok-dev" {
			t.Errorf("chunk %d: authorization is %q, want the same form the gateway APIs use", i, got[i].authorized)
		}
	}
}

// The ticket fields are the gateway's own names; a decode that drops uploadKey would send a photo with
// no hfac, so the mapping is worth pinning to the captured response.
func TestPrepareMediaDecodesTheTicket(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/messaging/files/v1/prepare" {
			http.Error(w, "wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		if r.Header.Get("authorization") != "tok-dev" {
			http.Error(w, "want authorization: <token>-<device_uuid>", http.StatusUnauthorized)
			return
		}
		fmt.Fprint(w, `{"url":"https://kage.talk.kakao.com/offset-up/d1V80R/dJMcaqOoMdn/?credential=x&expires=1&signature=y%3D&finalize=true",`+
			`"resource":"d1V80R/dJMcaqOoMdn/jbfU65fG1II2.png","uploadKey":"1:bc47dbe6","resourceType":"vk","chunkSize":4194304,"needReady":false}`)
	}))
	defer srv.Close()

	c := New(Auth{Token: "tok", DeviceID: "dev", Agent: "android", Version: "26.9.1"})
	old := pilsnerBase
	pilsnerBase = srv.URL + "/"
	defer func() { pilsnerBase = old }()
	ticket, err := c.PrepareMedia(context.Background(), 482943465622166, "image", 101273, "png")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if ticket.Resource != "d1V80R/dJMcaqOoMdn/jbfU65fG1II2.png" || ticket.UploadKey != "1:bc47dbe6" {
		t.Errorf("ticket fields lost: %+v", ticket)
	}
	if ticket.ChunkSize != 4194304 || ticket.ResourceType != "vk" {
		t.Errorf("ticket fields lost: %+v", ticket)
	}
	if !strings.Contains(ticket.URL, "/offset-up/") {
		t.Errorf("upload url: %q", ticket.URL)
	}
}

func TestMediaExt(t *testing.T) {
	for mime, want := range map[string]string{
		"image/jpeg": "jpg",
		"image/png":  "png",
		"image/webp": "webp",
		"image/gif":  "gif",
		"image/heif": "",
		"text/plain": "",
	} {
		if got := MediaExt(mime); got != want {
			t.Errorf("MediaExt(%q) = %q, want %q", mime, got, want)
		}
	}
}

func TestUploadExtFor(t *testing.T) {
	// A file's own extension decides, because that is what the client sends and what the gateway checks.
	if got := UploadExtFor("file", "application/octet-stream", "report.pdf"); got != "pdf" {
		t.Errorf("file extension from name: got %q, want pdf", got)
	}
	// A client that gives a mime type and no name still says what the file is.
	if got := UploadExtFor("file", "application/pdf", ""); got != "pdf" {
		t.Errorf("file extension from mime type: got %q, want pdf", got)
	}
	if got := UploadExtFor("file", "", ""); got != "" {
		t.Errorf("nothing to name the file with must be refused, got %q", got)
	}
	// Images and videos go by mime type: those extensions are part of the mediaType, not the sender's choice.
	if got := UploadExtFor("image", "image/png", "photo.jpg"); got != "png" {
		t.Errorf("image extension must come from the mime type, got %q", got)
	}
	if got := UploadExtFor("video", "video/mp4", ""); got != "mp4" {
		t.Errorf("video extension from mime type: got %q, want mp4", got)
	}
}

// The gateway's own reason codes are what make a refusal actionable ("try another file type"), so the
// error quotes them instead of paraphrasing. Measured answers pinned here are in r53.
func TestDescribePrepareErrorQuotesTheReason(t *testing.T) {
	err := describePrepareError("file", "apk", 400, []byte(`{"status":400,"code":"90017","reason":"EXTENSION_NOT_ALLOWED"}`))
	if err == nil {
		t.Fatal("a 400 with a reason must produce an error")
	}
	msg := err.Error()
	for _, want := range []string{".apk", "EXTENSION_NOT_ALLOWED"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error must name %s, got %q", want, msg)
		}
	}
	// A body without a reason must not lose the status or the body.
	other := describePrepareError("video", "mp4", 500, []byte(`{"status":500}`))
	if !strings.Contains(other.Error(), "500") {
		t.Errorf("an unexplained refusal must still name the status: %q", other.Error())
	}
}
