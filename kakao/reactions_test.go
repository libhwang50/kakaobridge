// SPDX-License-Identifier: AGPL-3.0-or-later

package kakao

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The two reaction reads are copied from measured answers of the gateway (docs/protocol-notes.md §12):
// the counts of a range of messages in one call, and the people behind one message in another. The ids
// are spelled as strings there, which is the part a decoder gets to get wrong silently.
const measuredLogMetas = `{
  "logmetas": [
    {"logId": "3965989416160335874", "revision": "3967312924227117320",
     "content": "{\"rx\":[{\"k\":2,\"o\":\"1200509_029\",\"c\":1,\"a\":{\"ko\":\"사랑\"}},{\"k\":2,\"o\":\"1200509_021\",\"c\":1,\"a\":{\"ko\":\"엄지척\"}}]}",
     "extra": "{\"myRx\":[{\"k\":2,\"o\":\"1200509_029\"},{\"k\":2,\"o\":\"1200509_021\"}]}"},
    {"logId": "3965687847341733889", "revision": "3965688641380620637",
     "content": "{\"rx\":[]}", "extra": "{\"myRx\":[]}"},
    {"logId": "3965512600604626945", "revision": "3965512816139460388",
     "content": "{\"rx\":[{\"k\":2,\"o\":\"1200509_029\",\"c\":3,\"a\":{\"ko\":\"사랑\"}}]}"}
  ],
  "hasNext": false, "order": "ASC", "nextMinLogId": null, "nextMaxLogId": null, "status": 0
}`

func TestReactionsOfAMessagesAreCountedAndOursAreMarked(t *testing.T) {
	var asked struct{ order, limit string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/emoticon/chat/rx/logmetas" {
			t.Errorf("called %s, wanted the range of message metas", r.URL.Path)
		}
		q := r.URL.Query()
		asked.order, asked.limit = q.Get("order"), q.Get("limit")
		for key, want := range map[string]string{
			"chatId": "482943465622166", "minLogId": "3900000000000000000", "maxLogId": "3999999999999999999",
		} {
			if q.Get(key) != want {
				t.Errorf("asked %s=%s, wanted %s", key, q.Get(key), want)
			}
		}
		if r.Header.Get("authorization") == "" {
			t.Error("asked without a credential")
		}
		w.Write([]byte(measuredLogMetas))
	}))
	defer srv.Close()
	old := pilsnerBase
	pilsnerBase = srv.URL + "/"
	defer func() { pilsnerBase = old }()

	metas, err := New(Auth{Token: "t", DeviceID: "d"}).RxLogMetas(context.Background(), RxMetaPage{
		ChatID:   482943465622166,
		MinLogID: 3900000000000000000,
		MaxLogID: 3999999999999999999,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 {
		t.Fatalf("got %d messages with metas, wanted 3", len(metas))
	}
	if asked.order != "asc" || asked.limit != "" {
		t.Errorf("asked order=%s limit=%s, wanted the whole range oldest first", asked.order, asked.limit)
	}
	if _, err := New(Auth{Token: "t", DeviceID: "d"}).RxLogMetas(context.Background(), RxMetaPage{
		ChatID: 482943465622166, MinLogID: 3900000000000000000, MaxLogID: 3999999999999999999,
		NewestFirst: true, Limit: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if asked.order != "desc" || asked.limit != "100" {
		t.Errorf("asked order=%s limit=%s, wanted a live room's newest states, capped", asked.order, asked.limit)
	}
	first := metas[0]
	if int64(first.LogID) != 3965989416160335874 || int64(first.Revision) != 3967312924227117320 {
		t.Errorf("read ids %d/%d, wanted 3965989416160335874/3967312924227117320",
			int64(first.LogID), int64(first.Revision))
	}
	if len(first.Reactions) != 2 {
		t.Fatalf("read %d reactions, wanted 2", len(first.Reactions))
	}
	heart := first.Reactions[0]
	if heart.Item != "1200509_029" || heart.Kind != 2 || heart.Count != 1 || heart.Alt["ko"] != "사랑" {
		t.Errorf("read the reaction as %+v", heart)
	}
	if len(first.Mine) != 2 || first.Mine[1].Item != "1200509_021" {
		t.Errorf("our own reactions read as %+v", first.Mine)
	}
	// A message whose reactions were all removed is still named: that is how a removal is seen.
	if len(metas[1].Reactions) != 0 {
		t.Errorf("message %d claims reactions %v, wanted none", int64(metas[1].LogID), metas[1].Reactions)
	}
	if len(metas[2].Reactions) != 1 || metas[2].Reactions[0].Count != 3 {
		t.Errorf("three people reacting read as %+v", metas[2].Reactions)
	}
	if metas[2].Mine != nil {
		t.Errorf("a message without an extra field claims our reactions as %v", metas[2].Mine)
	}
}

// Who reacted is a separate call, and an Open Chat room needs its link named there while an ordinary
// chat must not have one (measured: an empty or zero linkId is refused).
func TestWhoReactedNamesThePeople(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.RawQuery
		if r.URL.Path != "/emoticon/chat/rx/log-details" {
			t.Errorf("called %s, wanted the people behind one message", r.URL.Path)
		}
		w.Write([]byte(`{"details":[{"k":2,"o":"1200509_029","a":{"ko":"사랑"},"u":["447137810","7485505140902207768"]}],"status":0}`))
	}))
	defer srv.Close()
	old := pilsnerBase
	pilsnerBase = srv.URL + "/"
	defer func() { pilsnerBase = old }()

	c := New(Auth{Token: "t", DeviceID: "d"})
	people, err := c.RxLogDetails(context.Background(), 482943465622166, 0, 3965989416160335874)
	if err != nil {
		t.Fatal(err)
	}
	if asked != "chatId=482943465622166&logId=3965989416160335874" {
		t.Errorf("asked %s, with no link id wanted", asked)
	}
	if len(people) != 1 || people[0].Item != "1200509_029" {
		t.Fatalf("read %+v", people)
	}
	if len(people[0].Users) != 2 || int64(people[0].Users[1]) != 7485505140902207768 {
		t.Errorf("the people behind it read as %v", people[0].Users)
	}
	if _, err := c.RxLogDetails(context.Background(), 18181616243038287, 15572582, 3967023939974430723); err != nil {
		t.Fatal(err)
	}
	if asked != "chatId=18181616243038287&linkId=15572582&logId=3967023939974430723" {
		t.Errorf("asked %s, wanted the open chat room's link named", asked)
	}
}

// A refusal arrives with an HTTP 400 and a Kakao status; reading the status rather than the HTTP code
// is what turns it into an error the log can explain.
func TestAGatewayRefusalIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"status":-400,"message":""}`))
	}))
	defer srv.Close()
	old := pilsnerBase
	pilsnerBase = srv.URL + "/"
	defer func() { pilsnerBase = old }()

	_, err := New(Auth{Token: "t", DeviceID: "d"}).RxLogDetails(context.Background(), 1, 0, 2)
	if err == nil {
		t.Fatal("a refused call reported no error")
	}
}
