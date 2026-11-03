// SPDX-License-Identifier: AGPL-3.0-or-later

package kknet

// The bridge's own branding: the KakaoTalk mark, for the rooms the bridge creates for itself.
//
// Matrix wants an mxc URI anywhere an image is shown, so the logo is uploaded once per run and the
// network descriptor reports that URI: bridgev2 puts it in the avatar of a new personal-filtering
// space (bridgev2/space.go) and in the bridge info it publishes. A space that already exists predates
// the icon, so it is corrected when a session comes up.
//
// The asset is the KakaoTalk "TALK" bubble, rendered from the SVG kept next to it:
//
//	rsvg-convert -w 256 -h 256 assets/kakaotalk-logo.svg -o assets/kakaotalk-logo.png
//
// PNG rather than SVG because room avatars are rasterised by every client, and SVG support there is
// uneven enough that the icon would silently not show on some of them.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"

	_ "embed"
)

//go:embed assets/kakaotalk-logo.png
var kakaoLogoPNG []byte

const kakaoLogoMime = "image/png"

// networkLogo returns the mxc URI of the logo, uploading it the first time it is asked for. The bytes
// never change, so a restart that uploads them again costs one request and nothing else: the URI is not
// stored anywhere, and the only use of it is "is this room's avatar already right", which is answered by
// reading the room.
func (c *Connector) networkLogo(ctx context.Context) (id.ContentURIString, error) {
	if cached := c.logoMXC(); cached != "" {
		return cached, nil
	}
	if c.bridge == nil || c.bridge.Bot == nil {
		return "", errors.New("kknet: the bridge is not up yet, so the KakaoTalk logo cannot be uploaded")
	}
	mxc, _, err := c.bridge.Bot.UploadMedia(ctx, "", kakaoLogoPNG, "kakaotalk-logo.png", kakaoLogoMime)
	if err != nil {
		return "", fmt.Errorf("kknet: uploading the KakaoTalk logo: %w", err)
	}
	c.setLogoMXC(mxc)
	return mxc, nil
}

// ensureBranding runs when a session is up. It makes the logo available for anything bridgev2 creates
// afterwards, and gives the space room that already exists the avatar it was created without.
func (c *Client) ensureBranding(ctx context.Context) {
	mxc, err := c.conn.networkLogo(ctx)
	if err != nil {
		// Cosmetic, and worth one warning rather than a failed connection.
		c.login.Log.Warn().Err(err).Msg("KakaoTalk icon unavailable; the KakaoTalk space will have no avatar")
		return
	}
	space := c.login.SpaceRoom
	if space == "" {
		// Not created yet. bridgev2 takes the icon from the network descriptor when it creates it, and
		// networkLogo has now put it there.
		return
	}
	// Reading arbitrary room state is not part of the plain MatrixAPI surface; the connector-side
	// variant is the one meant for the bridge's own rooms (bridgev2/matrixinterface.go:74).
	mc, ok := c.conn.bridge.Matrix.(bridgev2.MatrixConnectorWithArbitraryRoomState)
	if !ok {
		c.login.Log.Debug().Msg("Matrix implementation cannot read room state; leaving the space avatar as it is")
		return
	}
	avatar, err := mc.GetStateEvent(ctx, space, event.StateRoomAvatar, "")
	if err != nil && !errors.Is(err, mautrix.MNotFound) {
		c.login.Log.Warn().Err(err).Str("space_room", string(space)).
			Msg("Could not read the KakaoTalk space avatar")
		return
	}
	if currentRoomAvatar(avatar) == mxc {
		return
	}
	_, err = c.conn.bridge.Bot.SendState(ctx, space, event.StateRoomAvatar, "",
		&event.Content{Parsed: &event.RoomAvatarEventContent{URL: mxc}}, time.Now())
	if err != nil {
		c.login.Log.Warn().Err(err).Str("space_room", string(space)).
			Msg("Could not set the KakaoTalk space avatar")
		return
	}
	c.login.Log.Info().Str("space_room", string(space)).Str("avatar", string(mxc)).
		Msg("KakaoTalk space avatar set")
}

// currentRoomAvatar reads the picture a room currently claims, from either form of a state event:
// the parsed content struct, or the decoded JSON map the event was received as.
func currentRoomAvatar(evt *event.Event) id.ContentURIString {
	if evt == nil {
		return ""
	}
	if parsed, ok := evt.Content.Parsed.(*event.RoomAvatarEventContent); ok && parsed != nil {
		return parsed.URL
	}
	if url, ok := evt.Content.Raw["url"].(string); ok {
		return id.ContentURIString(url)
	}
	return ""
}

// These two exist so the cached URI is only read and written under the connector's lock; the network
// descriptor is read from several places (space creation, management room, matrix metadata) while a
// session may be uploading it.
func (c *Connector) logoMXC() id.ContentURIString {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.logo
}

func (c *Connector) setLogoMXC(mxc id.ContentURIString) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logo = mxc
}
