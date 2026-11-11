// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"errors"
	"fmt"
	"testing"

	"github.com/libhwang50/kakaobridge/kakao"
	"github.com/libhwang50/kakaobridge/loco"
)

// A dropped connection and a rejected token look the same in the log if they are not told apart, and they
// want opposite treatment: one is retried, the other is renewed. Measured with a deliberately wrong
// access token: CHECKIN still answers ok and LOGINLIST is refused with inner status -950; a wrong device
// id is answered with -1 instead (docs/protocol-notes.md §4).
func TestARefusedSessionTokenIsRecognisedAsOne(t *testing.T) {
	refused := fmt.Errorf("kknet: opening a LOCO session: %w", &loco.InnerError{Verb: "LOGINLIST", Status: -950})
	if !sessionTokenRefused(refused) {
		t.Error("-950 from LOGINLIST should read as a refused session token")
	}
	for _, other := range []error{
		&loco.InnerError{Verb: "LOGINLIST", Status: -1},  // a device id Kakao does not know
		&loco.InnerError{Verb: "CHATINFO", Status: -950}, // another verb saying it
		errors.New("write tcp 172.30.0.10:55970->203.133.185.75:995: write: broken pipe"),
	} {
		if sessionTokenRefused(other) {
			t.Errorf("%v was read as a refused session token", other)
		}
	}
}

// -100 for a device that was registered once means the identity itself is gone. Only a person at the
// phone can fix that, so the bridge must stop and say so rather than retry.
func TestADeviceKakaoRefusesIsNotRetried(t *testing.T) {
	if !credentialIsDead(&kakao.StatusError{Status: -100}) {
		t.Error("status -100 should read as a dead device")
	}
	for _, retryable := range []error{
		&kakao.StatusError{Status: -300},
		&loco.InnerError{Verb: "LOGINLIST", Status: -950}, // the session token, not the device
		errors.New("context deadline exceeded"),
	} {
		if credentialIsDead(retryable) {
			t.Errorf("%v should not stop the bridge", retryable)
		}
	}
}

// A media request that failed on a dead socket is worth asking again after a moment, because the pump may
// have rebuilt the session by then. A status Kakao refused is not: it would be answered the same.
func TestADeadSocketIsAskedAgainAndARefusalIsNot(t *testing.T) {
	for _, transport := range []error{
		errors.New("write tcp 172.30.0.10:55970->203.133.185.75:995: write: broken pipe"),
		errors.New("EOF"),
		fmt.Errorf("kknet: no LOCO session to ask about chat 482943007629834"),
	} {
		if !sessionWentAway(transport) {
			t.Errorf("%v should be asked again", transport)
		}
	}
	for _, refused := range []error{
		&loco.InnerError{Verb: "MCHATLOGS", Status: -203},
		&loco.InnerError{Verb: "LOGINLIST", Status: -950},
	} {
		if sessionWentAway(refused) {
			t.Errorf("%v should not be asked again", refused)
		}
	}
}
