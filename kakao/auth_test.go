// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Every call here goes through do(), and the shape of the reply is a JSON object. Handing the target
// to do() as a value instead of a pointer produced real bugs twice (login.json "returned no
// access_token", passcodeLogin/generate "returned no passcode") while the gateway had answered
// status 0 - so all three calls are exercised against a stub gateway.
func TestDeviceAuthDecodesReplies(t *testing.T) {
	seen := map[string]map[string]any{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]any
		_ = json.NewDecoder(r.Body).Decode(&reqBody)
		seen[r.URL.Path] = reqBody
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/android/account/passcodeLogin/generate":
			_, _ = io.WriteString(w, `{"status":0,"passcode":"4321","remainingSeconds":57}`)
		case "/android/account/passcodeLogin/registerDevice":
			_, _ = io.WriteString(w, `{"status":-100,"nextRequestIntervalInSeconds":5,"remainingSeconds":40}`)
		case "/android/account/login.json":
			_, _ = io.WriteString(w, `{"status":0,"userId":447137810,"access_token":"tok","refresh_token":"ref","recipe":"rec","displayAccountId":"+82 10-1234-5678"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			_, _ = io.WriteString(w, `{"status":-1}`)
		}
	}))
	defer srv.Close()

	a := &DeviceAuth{BaseURL: srv.URL, Agent: "android", Account: "01012345678", Password: "pw",
		AppVer: "26.9.1", Lang: "ko", XVC: "81dff230d8731c63",
		Device: Device{UUID: "b9fa", Name: "MATRIX-BRIDGE", Model: "bridge-linux", OSVersion: "16"}}
	ctx := context.Background()

	code, window, err := a.GeneratePasscode(ctx)
	if err != nil {
		t.Fatalf("GeneratePasscode: %v", err)
	}
	if code != "4321" || window != 57*time.Second {
		t.Fatalf("GeneratePasscode decoded %q / %v", code, window)
	}
	gen := seen["/android/account/passcodeLogin/generate"]
	if gen["permanent"] != true {
		t.Errorf("android generate must send permanent:true, got %v", gen["permanent"])
	}
	if dev, _ := gen["device"].(map[string]any); dev["uuid"] != "b9fa" {
		t.Errorf("generate must send the full device object, got %v", gen["device"])
	}
	if gen["email"] != "01012345678" {
		t.Errorf("generate sent email=%v", gen["email"])
	}

	res, err := a.PollRegisterDevice(ctx)
	if err != nil || res.Approved {
		t.Fatalf("PollRegisterDevice should report still-waiting for status -100, got approved=%v err=%v", res.Approved, err)
	}
	if res.Interval != 5*time.Second || res.Remaining != 40*time.Second {
		t.Fatalf("PollRegisterDevice decoded interval=%v remaining=%v", res.Interval, res.Remaining)
	}

	tok, err := a.Login(ctx)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if tok.AccessToken != "tok" || tok.RefreshToken != "ref" || tok.UserID != 447137810 {
		t.Fatalf("Login decoded %+v", tok)
	}
}

// X-VC is required by login.json as well as the passcode family (-500 without it), and the header
// helper is shared: check it lands on both request kinds.
func TestRequiredHeadersPresent(t *testing.T) {
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		headers = append(headers, r.Header.Clone())
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":0,"passcode":"1","remainingSeconds":1}`)
	}))
	defer srv.Close()
	a := &DeviceAuth{BaseURL: srv.URL, Agent: "android", Account: "010", Password: "p",
		AppVer: "26.9.1", Lang: "ko", XVC: "vc-value", Device: Device{UUID: "u", OSVersion: "16"}}
	if _, _, err := a.GeneratePasscode(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(headers) != 1 {
		t.Fatalf("expected 1 request, got %d", len(headers))
	}
	h := headers[0]
	for key, want := range map[string]string{
		"X-Vc":         "vc-value",
		"Content-Type": "application/json",
		"User-Agent":   "KT/26.9.1 An/16 ko",
	} {
		if got := h.Get(key); got != want {
			t.Errorf("header %s = %q, want %q", key, got, want)
		}
	}
}
