// SPDX-License-Identifier: AGPL-3.0-or-later

package loco

import "fmt"

// Open Chat rooms are not described by the chat itself. CHATINFO for one answers with type "OM", no
// title, no picture, no member ids - and two ids that belong to another service: `li` (the Open Link
// id) and `otk` (that link's token). The room's name, intro and picture are kept on the link, and the
// client reads them over the same LOCO socket:
//
//	SYNCLINK     no arguments: every link this account has. The answer is `ols`, a list of link objects.
//	UPDATELINK   writes a room's intro and category: {li, desc, adid, categoryId}.
//	SYNCLINKUP   push carrying {ol: <the link object as it now is>}, which is how a rename or a new
//	             intro reaches every client without asking.
//
// (INFOLINK, GETLINK, SYNCLINKPF and SYNCLINKDL were each asked the same `li` and refused it: inner
// -203 or -300. SYNCLINK is the one that answers.)
//
// The link object's abbreviations, read from a room created for the purpose:
//
//	li     the link id - the same number CHATINFO puts in `li`
//	ln     the room name the members see
//	desc   the room intro (multi-line; the client allows 200 characters)
//	liu    the room picture, full size
//	lu     the public join link (open.kakao.com/o/…)
//	lt     what the link is: 8 is a chatroom, 1 is a person's own open profile link
//	otk    the link's token; it changes when the profile is written
//	olu    the *open profile* of the link's owner: the separate identity Open Chat rooms show instead
//	       of the account's real profile (nn, pi/fpi/opi, and pli for its own link)
//	ml     member limit; omt.safeBotEnabled and omt.tags; category{id, name{ko,en,ja}, icon}
//
// An account's own profile link is in the same answer as its rooms, so an entry is only used when its
// `li` is the one the chat named.

// OpenLink is the part of a link that a Matrix room can use: what it is called, what it says about
// itself, and its picture.
type OpenLink struct {
	LinkID      int64
	Name        string
	Description string
	ImageURL    string
	JoinURL     string
	Type        int
	OpenToken   int64
}

// ParseSyncLinkResponse picks one link out of a SYNCLINK answer by id. It is an error not to find it:
// the chat named the link, so an answer without it means the list is not what we asked for.
func ParseSyncLinkResponse(body []byte, linkID int64) (*OpenLink, error) {
	if inner := InnerStatus(body); inner != 0 {
		return nil, fmt.Errorf("loco: SYNCLINK inner status %d", inner)
	}
	doc, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding SYNCLINK: %w", err)
	}
	for _, e := range arrayElems(Get(doc, "ols")) {
		link, ok := e.Val.([]Elem)
		if !ok {
			continue
		}
		l := openLinkOf(link)
		if l != nil && l.LinkID == linkID {
			return l, nil
		}
	}
	return nil, fmt.Errorf("loco: SYNCLINK did not name link %d", linkID)
}

// ParseLinkUpdate decodes the link a SYNCLINKUP push carries.
func ParseLinkUpdate(body []byte) (*OpenLink, error) {
	doc, _, err := DecodeDoc(body, 0)
	if err != nil {
		return nil, fmt.Errorf("loco: decoding a link update: %w", err)
	}
	link, ok := Get(doc, "ol").([]Elem)
	if !ok {
		return nil, fmt.Errorf("loco: link update carried no link")
	}
	l := openLinkOf(link)
	if l == nil || l.LinkID == 0 {
		return nil, fmt.Errorf("loco: link update named no link")
	}
	return l, nil
}

func openLinkOf(doc []Elem) *OpenLink {
	return &OpenLink{
		LinkID:      int64Val(findVal(doc, "li")),
		Name:        stringVal(findVal(doc, "ln")),
		Description: stringVal(findVal(doc, "desc")),
		ImageURL:    stringVal(findVal(doc, "liu")),
		JoinURL:     stringVal(findVal(doc, "lu")),
		Type:        int(int32Val(findVal(doc, "lt"))),
		OpenToken:   int64Val(findVal(doc, "otk")),
	}
}
