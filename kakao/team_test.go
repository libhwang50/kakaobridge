// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A Team Chat's name and intro come from this service rather than from LOCO, so the request shape is
// worth pinning: measured on the wire as a POST of {"chatId":…} with the usual device credential
// (docs/protocol-notes.md §7a). The answer below is the measured field set, with invented ids.
func TestTeamChatProfileIsReadByPost(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotAuth, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth, gotType = r.Header.Get("authorization"), r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, `{"chatId":1000000000001,"name":"BRIDGE TEAM","description":"what this room is for",`+
			`"hostId":"2000000002","accessiblePrevChat":false,"accessibleLogId":3965809828508467200,`+
			`"status":"ACTIVE","revision":4,"memberRescueStatus":false,"warehouseKeyUpdateStatus":false,`+
			`"warehouseBackupStatus":"OFF","createdAt":1793698573155,`+
			`"permission":{"INVITE":"STAFF","WRITE":"MEMBER","BOT":"MEMBER"},"staff":[],"restrictions":[]}`)
	}))
	defer srv.Close()
	old := teamBase
	teamBase = srv.URL + "/"
	defer func() { teamBase = old }()

	c := New(Auth{Token: "tok", DeviceID: "dev", Agent: "android", Version: "26.9.1", Lang: "en"})
	team, err := c.TeamChat(context.Background(), 1000000000001)
	if err != nil {
		t.Fatalf("TeamChat: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/alcatraz/v1/chat/info" {
		t.Errorf("asked %s %s, want POST /alcatraz/v1/chat/info", gotMethod, gotPath)
	}
	if gotBody != `{"chatId":1000000000001}` {
		t.Errorf("sent body %q, want the chat id as a number", gotBody)
	}
	if gotAuth != "tok-dev" {
		t.Errorf("sent authorization %q, want the device credential", gotAuth)
	}
	if gotType != "application/json" {
		t.Errorf("sent content-type %q, want application/json", gotType)
	}
	if team.Name != "BRIDGE TEAM" || team.Description != "what this room is for" {
		t.Errorf("profile read as %q / %q", team.Name, team.Description)
	}
	if team.HostID != "2000000002" || team.Status != "ACTIVE" {
		t.Errorf("host/status read as %q / %q", team.HostID, team.Status)
	}
	if team.Permission["WRITE"] != "MEMBER" {
		t.Errorf("roles read as %v", team.Permission)
	}
}

// A chat that is not a Team Chat is refused, and the bridge needs to tell that apart from a failure:
// the gateway answers 404 with its own "invalid request" code (measured for a plain group chat and for
// an Open Chat room).
func TestTeamChatKnowsAChatWithoutATeam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"status":404,"code":"90001","message":"유효하지 않은 요청입니다."}`)
	}))
	defer srv.Close()
	old := teamBase
	teamBase = srv.URL + "/"
	defer func() { teamBase = old }()

	c := New(Auth{Token: "tok", DeviceID: "dev"})
	_, err := c.TeamChat(context.Background(), 1000000000002)
	if !errors.Is(err, ErrNotTeamChat) {
		t.Fatalf("got %v, want ErrNotTeamChat", err)
	}
}
