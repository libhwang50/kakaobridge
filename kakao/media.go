// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

// Outgoing media: ask the gateway for a signed upload slot, put the bytes there, then commit the
// object with LOCO WRITEMEDIA. The request/response shapes and the multipart layout are measured from
// a real mobile send and re-verified with this bridge's own credential (docs/protocol-notes.md §10);
// the fields below are the response of that call, copied verbatim.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
)

// pilsnerBase is the modern gateway the shipped clients use for the messaging APIs. Note that this
// path carries no /a1-talk/ prefix, unlike the account APIs: measured on the wire, not assumed. It is a
// var only so a test can point it at a local server.
var pilsnerBase = "https://talk-pilsner.kakao.com/"

// MediaTicket is the upload slot. UploadKey is what LOCO calls the photo's `hfac`, and Resource is the
// path that goes into `photo.resource`; the client never chooses either itself.
type MediaTicket struct {
	URL          string `json:"url"`
	Resource     string `json:"resource"`
	UploadKey    string `json:"uploadKey"`
	ResourceType string `json:"resourceType"`
	ChunkSize    int64  `json:"chunkSize"`
	NeedReady    bool   `json:"needReady"`
}

// PrepareMedia reserves a slot for `size` bytes of `mediaType` ("image" is the only value observed so
// far) in a chat. `ext` is the bare extension without a dot, which is how the client sends it (jpeg is
// sent as "jpg").
func (c *Client) PrepareMedia(ctx context.Context, chatID int64, mediaType string, size int64, ext string) (*MediaTicket, error) {
	body, err := json.Marshal(map[string]any{
		"chatId":    chatID,
		"mediaType": mediaType,
		"size":      size,
		"extension": ext,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, pilsnerBase+"messaging/files/v1/prepare", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("talk-agent", c.A.Agent+"/"+c.A.Version)
	req.Header.Set("talk-language", c.A.Lang)
	c.SetAuth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kakao: asking for a media upload slot: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, describePrepareError(mediaType, ext, resp.StatusCode, data)
	}
	var t MediaTicket
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("kakao: decoding the media upload slot: %w", err)
	}
	if t.URL == "" || t.Resource == "" {
		return nil, fmt.Errorf("kakao: media upload slot came back empty: %s", trim(string(data), 200))
	}
	return &t, nil
}

// UploadMedia puts the bytes in the slot. The shipped client posts one multipart form per chunk: the
// file part (named file_1, with an extension-only content type) plus an `offset` part naming the byte
// position, and marks only the last chunk finalize=true. ChunkSize from the ticket is the chunk
// boundary; the gateway signs the path, so the query is reused as-is apart from that flag.
// The returned body is the last chunk's answer. Callers normally ignore it, but for the kinds whose
// ticket says needReady the gateway uses that answer to say what it did with the bytes, and a probe has
// to be able to see it.
func (c *Client) UploadMedia(ctx context.Context, t *MediaTicket, data []byte, mimeType string) ([]byte, error) {
	chunk := t.ChunkSize
	if chunk <= 0 {
		chunk = int64(len(data))
	}
	var last []byte
	for off := int64(0); off < int64(len(data)); off += chunk {
		end := off + chunk
		if end > int64(len(data)) {
			end = int64(len(data))
		}
		body, err := c.uploadChunk(ctx, t, data[off:end], off, end == int64(len(data)), mimeType)
		if err != nil {
			return nil, err
		}
		last = body
	}
	return last, nil
}

func (c *Client) uploadChunk(ctx context.Context, t *MediaTicket, part []byte, offset int64, last bool, mimeType string) ([]byte, error) {
	u, err := url.Parse(t.URL)
	if err != nil {
		return nil, fmt.Errorf("kakao: upload url: %w", err)
	}
	q := u.Query()
	q.Set("finalize", strconv.FormatBool(last))
	u.RawQuery = q.Encode()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	name := path.Base(t.Resource)
	if name == "" || name == "." || name == "/" {
		name = "upload"
	}
	// The shipped client sends `image/*` for every image, which the gateway accepts; a concrete type is
	// no worse and is what we have, so use it and fall back to the wildcard.
	ctype := mimeType
	if ctype == "" {
		ctype = "image/*"
	}
	hdr := textproto.MIMEHeader{}
	hdr.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file_1"; filename=%q`, name))
	hdr.Set("Content-Type", ctype)
	fw, err := mw.CreatePart(hdr)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(part); err != nil {
		return nil, err
	}
	if err := mw.WriteField("offset", strconv.FormatInt(offset, 10)); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "multipart/form-data; boundary="+mw.Boundary())
	req.Header.Set("talk-agent", c.A.Agent+"/"+c.A.Version)
	req.Header.Set("talk-language", c.A.Lang)
	c.SetAuth(req)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kakao: uploading media: %w", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("kakao: media upload refused (HTTP %d): %s", resp.StatusCode, trim(string(data), 200))
	}
	return data, nil
}

// describePrepareError turns the gateway's error JSON into something a user can act on. Its reason codes
// are its own words (measured: INVALID_MEDIA_TYPE for a mediaType it does not know,
// EXTENSION_NOT_ALLOWED for e.g. ".apk" files), so they are quoted rather than paraphrased.
func describePrepareError(mediaType, ext string, status int, body []byte) error {
	var e struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(body, &e) == nil && e.Reason != "" {
		return fmt.Errorf("kakao: the gateway will not take a %s with the extension .%s (%s)", mediaType, ext, e.Reason)
	}
	return fmt.Errorf("kakao: media upload slot refused (HTTP %d): %s", status, trim(string(body), 200))
}

// MediaExt maps a Matrix mime type to the extension form Kakao expects. Only the types the bridge can
// actually send are listed; anything else returns "" and the caller refuses the event.
func MediaExt(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/webp":
		return "webp"
	default:
		return ""
	}
}

// VideoExt does the same for videos. The gateway hands out a ticket for mp4, webm, mkv and mov; which of
// them survive Kakao's own transcode is a separate question, so each one is verified before it is claimed
// in a capability.
func VideoExt(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "video/mp4":
		return "mp4"
	case "video/webm":
		return "webm"
	case "video/x-matroska":
		return "mkv"
	case "video/quicktime":
		return "mov"
	default:
		return ""
	}
}

// UploadExtFor returns the extension to ask a ticket for, or "" when the media type has no agreed
// extension. For a plain file the file's own name decides it (that is what the client sends, and the
// gateway is the one that says no to a type it does not take).
func UploadExtFor(mediaType, mimeType, fileName string) string {
	switch mediaType {
	case "image":
		return MediaExt(mimeType)
	case "video":
		return VideoExt(mimeType)
	default:
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(fileName), "."))
		if ext != "" && len(ext) <= 8 {
			return ext
		}
		// A client that names its type but not its file still says what the file is; the stdlib mapping
		// is only a translation of that, and the gateway still has the final say.
		if exts, err := mime.ExtensionsByType(strings.ToLower(strings.TrimSpace(mimeType))); err == nil && len(exts) > 0 {
			return strings.TrimPrefix(exts[0], ".")
		}
		return ""
	}
}
