// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

import (
	"testing"

	"github.com/libhwang50/kakaobridge/kakao"
)

// The built-in reaction items are pictures, not characters, so showing one is a choice. The names here
// are the ones this bridge has watched Kakao's own client draw; anything else keeps the name Kakao
// gives it rather than inventing a face for it.
func TestAReactionIsShownByNameWhenItsFaceIsUnknown(t *testing.T) {
	heart := kakao.ReactionItem{Kind: 2, Item: "1200509_029", Alt: map[string]string{"ko": "사랑"}}
	if key := reactionKey(heart); key != "❤️" {
		t.Errorf("the first reaction in the picker was shown as %q, wanted a heart", key)
	}
	thumb := kakao.ReactionItem{Kind: 2, Item: "1200509_021", Alt: map[string]string{"ko": "엄지척"}}
	if key := reactionKey(thumb); key != "👍" {
		t.Errorf("the second was shown as %q, wanted thumbs up", key)
	}
	unknown := kakao.ReactionItem{Kind: 2, Item: "1200509_099", Alt: map[string]string{"ko": "응원봉"}}
	if key := reactionKey(unknown); key != "응원봉" {
		t.Errorf("an unseen reaction was shown as %q, wanted the name Kakao gives it", key)
	}
	nameless := kakao.ReactionItem{Kind: 2, Item: "1200509_999"}
	if key := reactionKey(nameless); key != "" {
		t.Errorf("a reaction with no name and no known face was shown as %q, wanted nothing", key)
	}
}

// Kakao repeats the whole reaction state of a message every time it is read, and the revision moves
// whenever it changes. Applying the same state twice would send Matrix the same annotation over and
// over, and treating a removal as "nothing here" would leave the annotation behind.
func TestAReactionStateIsAppliedOnceAndItsRemovalIsSeen(t *testing.T) {
	c := &Client{}
	if !c.reactionChanged(100, 7, 2) {
		t.Fatal("the first reaction state was not applied at all")
	}
	if c.reactionChanged(100, 7, 2) {
		t.Error("the same reaction state was applied twice")
	}
	if !c.reactionChanged(100, 9, 2) {
		t.Error("a reaction added on top of the first was not applied")
	}
	if !c.reactionChanged(100, 11, 0) {
		t.Error("the reactions being taken back was not applied, so Matrix would keep them")
	}
	if c.reactionChanged(100, 11, 0) {
		t.Error("an empty state was applied twice")
	}
	// A message that never had reactions is not a change either.
	if c.reactionChanged(200, 3, 0) {
		t.Error("a message that has never had reactions was treated as changed")
	}
	// Forgetting a message after a failed send lets a later read try again.
	c.forgetReaction(100)
	if !c.reactionChanged(100, 11, 3) {
		t.Error("forgetting a message did not let its new reactions be applied")
	}
}

// A reaction belongs to a message the bridge put in Matrix. On a message it skipped, there is no event
// to react to.
func TestAReactionIsOnlyBridgedForAMessageThatIsInMatrix(t *testing.T) {
	c := &Client{bridged: map[int64][]int64{5: {10, 20, 30}}}
	for _, id := range []int64{10, 20, 30} {
		if !c.wasBridged(5, id) {
			t.Errorf("message %d was bridged but is not found", id)
		}
	}
	if c.wasBridged(5, 15) || c.wasBridged(5, 40) || c.wasBridged(6, 10) {
		t.Error("a message that was never bridged was treated as bridged")
	}
	if _, _, ok := c.bridgedRange(5); !ok {
		t.Error("a chat with bridged messages has no range")
	}
	if min, max, ok := c.bridgedRange(5); !ok || min != 10 || max != 30 {
		t.Errorf("range %d..%d, wanted 10..30", min, max)
	}
	if _, _, ok := c.bridgedRange(7); ok {
		t.Error("an unknown chat claimed a range")
	}
}
