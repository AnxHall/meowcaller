# Datasheet: `voip/initial_group_call`

This module starts one preselected multi-participant audio call, seeds its
call-scoped signaling state, and converts the first usable group roster, relay,
and key epoch into the existing media-readiness boundary.

**Validation vector:** `initial_group_call_corpus.json` — sanitized initial
offer, roster progression, relay selection, and readiness cases derived from
the captures below.

**Reference pinned at:** capture SHA-256
`1851cf76118bc8ef116df4ea51db73968cef3d415996cdf34013bdee9ac27fc7`
(ad-hoc preselected group call), corroborated by capture SHA-256
`47e4966e1847b686b3a31c4983df8025617d200ec27a71c5884598488af65b90`
(group-chat-bound initial group call).

## Reference source (verbatim — authoritative)

Primary immutable raw file:

`diag/captures/group-call-preselected-participants-v2-20260723-140338.jsonl`

- Byte length: 10,313,200.
- Event count: 8,661 JSONL records.
- SHA-256:
  `1851cf76118bc8ef116df4ea51db73968cef3d415996cdf34013bdee9ac27fc7`.
- Line 182 is the verbatim outgoing stanza log for the initial offer.
- Line 186, signaling sequence 24380, is the decoded network-out authority for
  the same stanza.
- Line 341, signaling sequence 24492, is the decoded server ACK carrying the
  transaction-11 initial roster.
- Line 893, signaling sequence 24707, is the first decoded `group_update` with
  a connected remote PID and group relay allocation, at roster transaction 21.

Supporting immutable raw file:

`diag/captures/group-call-outgoing-v2-20260723-100703.jsonl`

- Byte length: 21,836,404.
- Event count: 18,629 JSONL records.
- SHA-256:
  `47e4966e1847b686b3a31c4983df8025617d200ec27a71c5884598488af65b90`.
- Line 2170, signaling sequence 296, is the decoded network-out initial group
  offer whose `offer.attrs["group-jid"]` is
  `120363411251996986@g.us`.

The raw JSONL records at those boundaries are the verbatim authority. The KAT
uses these JSON paths:

- line 186:
  `node.tag`, `node.attrs.to`, `node.content[0].tag`,
  `node.content[0].attrs`, and the complete ordered
  `node.content[0].content` offer tree;
- supporting line 2170:
  `node.content[0].attrs["group-jid"]`;
- line 341:
  the `group_info` attributes and complete ordered user/device roster;
- line 893:
  the `group_info` attributes and complete ordered user/device roster, plus the
  `relay` attributes, key/token lengths, and ordered `te2` metadata/address
  lengths.

The captured initial ad-hoc offer is exactly:

```xml
<call to="00DD63A26643DC3496FCBD161E6E2AB1@call" id="20350.27209-809">
  <offer call-id="00DD63A26643DC3496FCBD161E6E2AB1" call-creator="156535032389744:14@lid">
    <audio enc="opus" rate="8000" />
    <audio enc="opus" rate="16000" />
    <net medium="3" />
    <group_info>
      <user jid="156535032389744@lid">
        <device jid="156535032389744:14@lid">
          <capability ver="1">0105f709e0bb53</capability>
        </device>
      </user>
      <user jid="242653052539031@lid">
        <device jid="242653052539031@lid" />
        <device jid="242653052539031:1@lid" />
      </user>
      <user jid="74170125783269@lid">
        <device jid="74170125783269@lid" />
        <device jid="74170125783269:44@lid" />
        <device jid="74170125783269:45@lid" />
        <device jid="74170125783269:43@lid" />
      </user>
      <user jid="9908623781998@lid">
        <device jid="9908623781998@lid" />
        <device jid="9908623781998:64@lid" />
        <device jid="9908623781998:63@lid" />
      </user>
    </group_info>
  </offer>
</call>
```

Only the local device carries `capability ver="1"` and the seven-byte
capability blob. Every selected remote bare LID and all discovered candidate
devices are present, in selected-user order. The group-chat-bound capture adds
only `group-jid="120363411251996986@g.us"` to the `offer`; the ad-hoc offer
omits it.

At transaction 11, self is `connected` without a PID and all three remotes are
`outgoing` without PIDs. At transaction 21, self is `connected` with PID 0,
`242653052539031@lid` is `connected` through device
`242653052539031@lid` with PID 1, and the other selected users are `receipt`.
The attached group relay has transaction 1, self PID 0, a 24-byte key, three
193-byte tokens, one 70-byte auth token, and ordered zrh/mxp/fra IPv4+IPv6
endpoint pairs. The first usable non-FNA IPv4 endpoint is zrh:
`9df011850d96` (`157.240.17.133:3478`), relay ID 0, token ID 0, auth-token ID
0, RTT 18.

## Go envelope (signatures only)

```go
package voip

type InitialGroupOfferParams struct {
	CallID       string
	CallCreator  types.JID
	GroupJID     types.JID
	Participants []types.GroupCallParticipant
}

func BuildInitialGroupOffer(params InitialGroupOfferParams) (waBinary.Node, error)
func ParseInitialGroupCallAck(node *waBinary.Node) (*types.GroupCallUpdate, bool, error)
```

```go
package whatsmeow

type GroupCallOfferOptions struct {
	GroupJID types.JID
}

func (cli *Client) OfferGroupCall(
	ctx context.Context,
	targets []types.JID,
	options ...GroupCallOfferOptions,
) (callID string, err error)

func newOutgoingGroupCallState(
	callID string,
	self types.JID,
	targets []types.JID,
	groupJID types.JID,
) *callState

func groupMediaReadyFields(
	self types.JID,
	update types.GroupCallUpdate,
) (peer types.JID, relay types.RelayEndpoint, ok bool)

type CallOffer struct {
	types.BasicCallMeta
	types.CallRemoteMeta
	Data  *waBinary.Node
	Video bool
	Group *types.GroupCallUpdate
}
```

## Implementation suggestions (guidance, not authoritative)

- Validate the entire target list before discovery: require at least two
  distinct non-empty remote users after bare-LID resolution; reject self and
  duplicates without silently dropping them.
- Preserve selected-user order in both the roster and device discovery input.
  Keep each discovered device under its owning bare LID.
- Build the local roster entry first with the exact active device JID,
  capability version 1, and an owned copy of the local client's capability.
  The Web capture advertises `0105f709e0bb53`; the current Go client advertises
  its existing `voip.CapabilityOffer` ending in `13`, and existing active-add
  captures show that this value legitimately varies by client/device. Keep the
  low-level builder generic over the participant capability so the capture KAT
  can reproduce `...53` exactly, while `OfferGroupCall` uses the Go client's
  current `voip.CapabilityOffer`. Remote devices carry no capability child.
- Send the single capture-shaped offer to `CALLID@call`, then install the
  already-group-scoped keyless call state. Discovery and send failures must not
  install state.
- Keep the selected first bare LID only as the legacy direct peer fallback.
  Group acceptance must not overwrite it.
- Accept only transaction-increasing group snapshots. Readiness requires an
  installed 32-byte group key epoch, a connected remote PID-bearing device, and
  a non-FNA six-byte relay endpoint whose token/auth-token indices exist.
- Resolve the first usable endpoint in capture order. Translate its four IPv4
  bytes and big-endian port into `types.RelayEndpoint`, cloning the relay key
  and matching token/auth token. Emit once through the existing
  `CallMediaReady`.
- Clone the parsed optional invite snapshot onto `events.CallOffer`; direct
  offers leave the field nil.
- `TODO(human)`: live WhatsApp E2E must validate the complete offer/ACK/rekey/
  media transition and clear the provisional `// NOT VALIDATED:` markers.
