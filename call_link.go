package meowcaller

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
)

// CallLinkOptions selects an audio or video call link.
type CallLinkOptions struct {
	Video bool
}

// CallLink is one reusable public call-link token and URL.
type CallLink struct {
	Token string
	URL   string
	Video bool
}

// CallLinkPreview is sanitized link metadata returned before joining.
type CallLinkPreview struct {
	Token              string
	Video              bool
	ApprovalRequired   bool
	IsAdmin            bool
	Creator            types.JID
	CreatorPhoneNumber types.JID
}

// CreateCallLink creates a reusable audio or video call link.
func (c *Client) CreateCallLink(ctx context.Context, opts CallLinkOptions) (CallLink, error) {
	if c == nil || c.eng == nil || c.eng.createCallLink == nil {
		return CallLink{}, errors.New("meowcaller: call-link signaling is unavailable")
	}
	media := callLinkMedia(opts)
	link, err := c.eng.createCallLink(ctx, media)
	if err != nil {
		return CallLink{}, fmt.Errorf("meowcaller: create call link: %w", err)
	}
	if link == nil || link.Token == "" {
		return CallLink{}, errors.New("meowcaller: create call link returned no token")
	}
	return publicCallLink(link.Token, link.Media), nil
}

// PreviewCallLink queries call-link metadata without joining.
func (c *Client) PreviewCallLink(
	ctx context.Context,
	tokenOrURL string,
	opts CallLinkOptions,
) (CallLinkPreview, error) {
	if c == nil || c.eng == nil || c.eng.previewCallLink == nil {
		return CallLinkPreview{}, errors.New("meowcaller: call-link signaling is unavailable")
	}
	token, err := normalizeCallLinkToken(tokenOrURL)
	if err != nil {
		return CallLinkPreview{}, err
	}
	preview, err := c.eng.previewCallLink(ctx, token, callLinkMedia(opts))
	if err != nil {
		return CallLinkPreview{}, fmt.Errorf("meowcaller: preview call link: %w", err)
	}
	if preview == nil {
		return CallLinkPreview{}, errors.New("meowcaller: preview call link returned no result")
	}
	return CallLinkPreview{
		Token:              preview.Token,
		Video:              preview.Media == types.CallLinkMediaVideo,
		ApprovalRequired:   preview.WaitingRoomEnabled,
		IsAdmin:            preview.IsAdmin,
		Creator:            preview.Creator,
		CreatorPhoneNumber: preview.CreatorPN,
	}, nil
}

// JoinCallLink joins the active session or returns a dormant waiting-room call.
func (c *Client) JoinCallLink(
	ctx context.Context,
	tokenOrURL string,
	opts CallLinkOptions,
) (*Call, error) {
	if c == nil || c.eng == nil {
		return nil, errors.New("meowcaller: call-link signaling is unavailable")
	}
	token, err := normalizeCallLinkToken(tokenOrURL)
	if err != nil {
		return nil, err
	}
	return c.eng.joinLink(ctx, token, opts)
}

func (e *engine) joinLink(ctx context.Context, token string, opts CallLinkOptions) (*Call, error) {
	if e.joinCallLink == nil {
		return nil, errors.New("meowcaller: call-link signaling is unavailable")
	}
	join, err := e.joinCallLink(ctx, token, callLinkMedia(opts))
	if err != nil {
		return nil, fmt.Errorf("meowcaller: join call link: %w", err)
	}
	if join == nil || join.CallID == "" {
		return nil, errors.New("meowcaller: join call link returned no active call")
	}
	phase := CallPhaseConnecting
	if join.InWaitingRoom {
		phase = CallPhaseWaitingRoom
	}
	peer := join.CallCreator.ToNonAD()
	call := &Call{eng: e, id: join.CallID, peer: peer, phase: phase}
	waiting := waitingRoomStateFromJoin(join)
	call.waitingRoomState = &waiting
	if join.Group != nil {
		state := groupCallStateFromUpdate(*join.Group)
		call.groupState = &state
	}

	e.mu.Lock()
	m := e.calls[join.CallID]
	if m == nil {
		m = e.entry(join.CallID)
	}
	wasPlaceholder := m.placeholder
	cancelPlaceholder := e.attachGroupPlaceholder(m)
	if m.call == nil || wasPlaceholder {
		m.call = call
	} else {
		call = m.call
		call.setWaitingRoomState(waiting)
	}
	m.direction = CallDirectionOutgoing
	m.group = true
	m.from = join.CallCreator
	m.localVideo = opts.Video
	m.remoteVideo = opts.Video
	if join.Group != nil && (m.groupUpdate == nil ||
		join.Group.TransactionID > m.groupUpdate.TransactionID) {
		update := cloneGroupCallUpdate(*join.Group)
		m.groupUpdate = &update
		pending := cloneGroupCallUpdate(update)
		m.pendingGroupUpdate = &pending
	}
	authoritative := m.groupUpdate
	authoritativeWaitingRoom := m.waitingRoom
	e.mu.Unlock()
	if cancelPlaceholder != nil {
		cancelPlaceholder()
	}
	if authoritative != nil {
		call.setGroupState(groupCallStateFromUpdate(*authoritative))
	}
	if authoritativeWaitingRoom != nil {
		call.setWaitingRoomState(*authoritativeWaitingRoom)
	}
	e.maybeStartMedia(join.CallID)
	return call, nil
}

func (e *engine) onWaitingRoomUpdate(event *events.CallWaitingRoomUpdate) {
	if event == nil || event.CallID == "" {
		return
	}
	e.mu.Lock()
	m := e.calls[event.CallID]
	if m == nil {
		m = e.groupEntry(event.CallID)
	}
	inWaitingRoom := !event.WaitingRoom.IsAdmin
	if m.call != nil {
		inWaitingRoom = m.call.State() == CallPhaseWaitingRoom
	} else if m.waitingRoom != nil {
		inWaitingRoom = m.waitingRoom.InWaitingRoom
	}
	state := waitingRoomStateFromTypes(event.WaitingRoom, inWaitingRoom)
	m.waitingRoom = &state
	call := m.call
	e.mu.Unlock()
	if call != nil {
		call.setWaitingRoomState(state)
	}
}

func (e *engine) setApprovalRequired(ctx context.Context, callID string, enabled bool) error {
	call, state, err := e.adminWaitingRoomCall(callID)
	if err != nil {
		return err
	}
	if e.setCallLinkApproval == nil {
		return errors.New("meowcaller: call-link signaling is unavailable")
	}
	if err = e.setCallLinkApproval(ctx, callID, enabled); err != nil {
		return fmt.Errorf("meowcaller: set approval required: %w", err)
	}
	state.Enabled = enabled
	call.setWaitingRoomState(state)
	return nil
}

func (e *engine) controlWaitingParticipant(
	ctx context.Context,
	callID, rawUser string,
	admit bool,
) error {
	_, _, err := e.adminWaitingRoomCall(callID)
	if err != nil {
		return err
	}
	user, err := parseCallTarget(rawUser)
	if err != nil {
		return err
	}
	if admit {
		if e.admitCallLinkParticipant == nil {
			return errors.New("meowcaller: call-link signaling is unavailable")
		}
		err = e.admitCallLinkParticipant(ctx, callID, user.ToNonAD())
	} else {
		if e.denyCallLinkParticipant == nil {
			return errors.New("meowcaller: call-link signaling is unavailable")
		}
		err = e.denyCallLinkParticipant(ctx, callID, user.ToNonAD())
	}
	if err != nil {
		return fmt.Errorf("meowcaller: waiting-room participant control: %w", err)
	}
	return nil
}

func (e *engine) adminWaitingRoomCall(callID string) (*Call, WaitingRoomState, error) {
	e.mu.Lock()
	m := e.calls[callID]
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call == nil || call.State() == CallPhaseEnded {
		return nil, WaitingRoomState{}, errors.New("meowcaller: call is not active")
	}
	state, ok := call.WaitingRoomState()
	if !ok || !state.IsAdmin {
		return nil, WaitingRoomState{}, errors.New("meowcaller: call-link control requires an administrator")
	}
	return call, state, nil
}

func (e *engine) setHandRaised(callID string, raised bool) error {
	if e.setCallHandRaised == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	if err := e.setCallHandRaised(context.Background(), callID, raised); err != nil {
		return fmt.Errorf("meowcaller: set hand raised: %w", err)
	}
	return nil
}

func (e *engine) setScreenShare(
	callID string,
	state types.CallScreenShareState,
	screenShareID *uint32,
) error {
	if e.setCallScreenShare == nil {
		return errors.New("meowcaller: call signaling is unavailable")
	}
	if err := e.setCallScreenShare(context.Background(), callID, state, screenShareID); err != nil {
		return fmt.Errorf("meowcaller: set screen share: %w", err)
	}
	e.mu.Lock()
	m := e.calls[callID]
	var sender *videoSender
	var call *Call
	if m != nil {
		sender = m.videoTx
		call = m.call
	}
	e.mu.Unlock()
	sender.switchSource()
	if call != nil {
		call.requestVideoKeyframe()
	}
	return nil
}

func (e *engine) onHandRaise(event *events.CallHandRaise) {
	if event == nil {
		return
	}
	e.mu.Lock()
	m := e.calls[event.CallID]
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		call.dispatchHandRaise(HandRaiseState{
			Participant: event.Participant,
			Raised:      event.Raised,
		})
	}
}

func (e *engine) onScreenShare(event *events.CallScreenShare) {
	if event == nil {
		return
	}
	e.mu.Lock()
	m := e.calls[event.CallID]
	var call *Call
	if m != nil {
		call = m.call
	}
	e.mu.Unlock()
	if call != nil {
		call.dispatchScreenShare(ScreenShareState{
			Participant:      event.Participant,
			Active:           event.State == types.CallScreenShareStateStarted,
			Version:          event.Version,
			ScreenShareID:    event.ScreenShareID,
			HasScreenShareID: event.HasScreenShareID,
			Synthetic:        event.Synthetic,
		})
	}
}

func waitingRoomStateFromJoin(join *types.CallLinkJoin) WaitingRoomState {
	return WaitingRoomState{
		Enabled:       join.WaitingRoomEnabled,
		IsAdmin:       join.IsAdmin,
		InWaitingRoom: join.InWaitingRoom,
	}
}

func waitingRoomStateFromTypes(room types.CallLinkWaitingRoom, inWaitingRoom bool) WaitingRoomState {
	state := WaitingRoomState{
		Enabled:       room.Enabled,
		IsAdmin:       room.IsAdmin,
		InWaitingRoom: inWaitingRoom,
		TransactionID: room.TransactionID,
		Users:         make([]WaitingRoomUser, len(room.Users)),
	}
	for i, user := range room.Users {
		state.Users[i] = WaitingRoomUser{JID: user.JID, PN: user.PN, State: user.State}
	}
	return state
}

func callLinkMedia(opts CallLinkOptions) types.CallLinkMedia {
	if opts.Video {
		return types.CallLinkMediaVideo
	}
	return types.CallLinkMediaAudio
}

func publicCallLink(token string, media types.CallLinkMedia) CallLink {
	return CallLink{
		Token: token,
		URL:   fmt.Sprintf("https://call.whatsapp.com/%s/%s", media, token),
		Video: media == types.CallLinkMediaVideo,
	}
}

func normalizeCallLinkToken(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("meowcaller: call-link token is required")
	}
	if !strings.Contains(raw, "://") {
		if strings.ContainsRune(raw, '/') {
			return "", errors.New("meowcaller: invalid call-link token")
		}
		return raw, nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "call.whatsapp.com" {
		return "", errors.New("meowcaller: invalid WhatsApp call-link URL")
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 || (parts[0] != "audio" && parts[0] != "video") || parts[1] == "" {
		return "", errors.New("meowcaller: invalid WhatsApp call-link URL")
	}
	return parts[1], nil
}
