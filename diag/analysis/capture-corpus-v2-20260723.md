# WhatsApp call capture corpus analysis

## Capture integrity

This report covers every archived JSONL capture currently in `diag/captures`.
The active `whatsapp-20260723-140946.jsonl` file is intentionally excluded
because the collector is still writing it.

| Capture | Events | Bytes | SHA-256 |
|---|---:|---:|---|
| `group-call-outgoing-20260723-100408.jsonl` | 10,472 | 3,938,672 | `f126974bcdbbb7f6325e17f0acc3b8eea4119a48d17bbf9c0691c3228df81936` |
| `group-call-outgoing-v2-20260723-100703.jsonl` | 18,629 | 21,836,404 | `47e4966e1847b686b3a31c4983df8025617d200ec27a71c5884598488af65b90` |
| `group-call-add-people-v2-20260723-112208.jsonl` | 8,372 | 12,762,523 | `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6` |
| `pre-group-call-multi-add-v2-20260723-114000.jsonl` | 7,803 | 11,660,927 | `2e9c5492097cd0f6aa3d9964aabf34a0a92bf53a70850072a765133ff7d36896` |
| `group-call-multi-add-v2-20260723-135301.jsonl` | 7,192 | 9,572,198 | `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def` |
| `snapshot-v2-20260723-135600.jsonl` | 476 | 688,419 | `47146c58d637102fd07174cb5651abf985a0e628f1b28dcdbedaac5eef3e2098` |
| `group-call-preselected-participants-v2-20260723-140338.jsonl` | 8,661 | 10,313,200 | `1851cf76118bc8ef116df4ea51db73968cef3d415996cdf34013bdee9ac27fc7` |
| `group-chat-selector-single-direct-v2-20260723-140636.jsonl` | 3,216 | 5,353,023 | `fe6a59e96f37af1459b7ffcca1fb4909253683b3e4d2cb0b3af75cd173579247` |

Total: 8 captures, 64,821 events, 76,125,366 bytes. Every JSONL line
parsed successfully. Raw captures were not modified.

## Experiment matrix

| Experiment | Stack entry point | Initial targets | Chat-bound group JID | Wire result |
|---|---|---:|---|---|
| Original group-chat call, v1/v2 | `startGroupCall` | 2 remote users | Present | Initial group call |
| Active 1:1, add one | `startCall`, then `inviteToCall` | 1 + 1 | Absent | In-place ad-hoc group upgrade |
| Active 1:1, add two in one picker action | `startCall`, then two `inviteToCall` calls | 1 + 2 | Absent | In-place ad-hoc group upgrade |
| Pre-call selector, three remote users | `startGroupCall` | 3 | Absent/empty | Initial ad-hoc group call |
| Group-chat selector, one remote user | `startCall` | 1 | Absent | Direct 1:1 call |

The `pre-group-call-multi-add` and `snapshot` files contain setup/background
activity but no call IDs or VoIP-stack call methods. They remain useful negative
controls for separating ambient WhatsApp traffic from call traffic. The
`pre-group-call-multi-add` sequence ends at 19010 and the multi-add capture resumes
at 19011, proving the archive boundary did not hide an already-running call.
The snapshot opens the new-call selector and selects contacts but ends before
confirmation, so it is a partial selector trace rather than a signaling vector.

The v2 original group-call archive begins with 111 residual v1 rows from the
previous extension session. There is still exactly one logical call in the file:
only one `startGroupCall` and one call ID occur.

## Central finding: selector origin is not call identity

The latest capture confirms the correction supplied by the operator. Opening a
call selector from a group chat does not make the resulting call group-bound.

Immediately before line 89, navigation telemetry reports a group chat with
`groupSize=3` and `typeOfGroup=1`, and the click path is the chat call-dropdown
voice control. At line 89 / sequence 31015 / `12:07:50.234Z`, the one-person
selection nevertheless invokes:

```text
startCall(one bare LID, that user's device fan-out, one call ID, ...)
```

The resulting offer is the standard direct-call shape:

- one peer user and their devices;
- privacy material;
- Opus at 8 kHz and 16 kHz;
- network medium 3;
- no `group_info`;
- no `group-jid`;
- no `CALLID@call` routing.

Its relay ACK is direct and advertises `peer_pid=0` and `self_pid=1`. The peer's
`preaccept`, `accept`, and final terminate are also direct participant-addressed
signals. `getCallInfo` remains a two-participant, non-group call.

By contrast, line 120 / sequence 24321 / `12:05:27.521Z` in the immediately
preceding pre-call selection invokes one `startGroupCall` with three remote users,
three bare LIDs, and three device lists. Its group-chat argument is the empty
string. The initial offer contains self plus all three remote users in one
`group_info`. The server ACK at transaction 11 and all later group updates also
omit `group-jid`.

### Observed

- One selected remote user produces `startCall`.
- Three selected remote users produce `startGroupCall`.
- The group-chat context does not appear in the direct call's signaling.
- The ad-hoc initial group call is a real group call despite having no group JID.

### Hypothesis

The menu likely branches on selected remote participant count: one uses the
direct-call path and two or more use the initial-group path. The one-versus-three
captures strongly support this, but a pre-call selection of exactly two remote
users would close the remaining gap.

## Three distinct construction paths

### 1. Direct 1:1

`startCall` creates one direct offer. The peer identity remains the one selected
user. Direct lifecycle uses peer-addressed signaling and the ordinary 1:1 relay.

### 2. Initial group call

`startGroupCall` creates one initial offer containing all selected users and
their candidate devices. This remains true in both observed variants:

- a group-chat-bound call with a non-empty group JID;
- an ad-hoc selector call with an empty group JID.

The preselected ad-hoc call begins with four roster users including self. Its
server progression is:

- transaction 11: three remote users outgoing;
- transaction 21: original peer connected, two users at receipt;
- transaction 24: one additional user connected with PID 2;
- transaction 28: all four connected, final added user PID 3;
- transaction 33: PID 2 participant leaves;
- transaction 34: original peer leaves.

The server terminate for this call is delivered before the final transaction-34
roster snapshot reaches the stack. Future lifecycle code must tolerate late
roster updates and must not infer who ended the call from this ordering.

### 3. Add people to an active call

An active 1:1 call remains on the same call ID while becoming an ad-hoc group.
Each added user gets an independent singular `inviteToCall` and offer, even when
two users were selected in one UI confirmation. The server merges those
independent invitation tracks into the canonical transaction-ordered roster.

This means the initial-group builder and active-call invite builder are different
wire operations:

- initial group: one offer containing all initial participants;
- active add: one offer per newly invited participant.

## Identity, roster, media, and keying conclusions

### Observed

- `GroupJID` is optional. It is present for a group-chat-bound initial call and
  absent for ad-hoc initial groups and 1:1-to-group upgrades.
- Call ID is stable across active-call upgrades.
- Group updates are authoritative, transaction-ordered snapshots.
- A roster may contain outgoing, receipt, connected, and invited users together.
- Only connected devices have PIDs.
- Only connected participants become active media and SFrame-key members.
- Pending invitees can have reserved SSRC families without active RTP.
- Group relay credentials rotate as connected membership changes, while the
  server can keep the elected relay stable.
- PID layout is allocation-specific. The original group call assigns self PID 0;
  a 1:1-to-group upgrade assigns self PID 1. Code must consume the advertised
  PID instead of assuming a fixed self slot.

### Hypotheses

- Transaction number gaps are server versioning, not missing client retries.
- Rekey authorship follows the participant responsible for the current membership
  transition, but the corpus is not sufficient to make that an implementation
  rule.

## Current implementation comparison

### whatsmeow

The `rajeh/group-calls` branch already has `voip.ParseGroupUpdate`, roster types,
and group relay types. The capture corpus exposes two immediate correctness gaps:

1. `parseGroupInfo` currently requires `group-jid`, so it rejects all captured
   ad-hoc group updates even though they are valid and complete.
2. `handleCallEvent` does not dispatch `group_update`; parsed snapshots therefore
   never reach call state or meowcaller.

The call state and public API are still 1:1-only. There is no initial group offer
API, active-call invite API, group-address routing mode, transaction gate, or
participant lifecycle event.

### meowcaller

`Call`, `engineCall`, media startup, and the web example all model one peer.
There is no participant roster, per-PID media/key state, group relay update path,
initial group call API, or active-call invite method. The web example cannot
truthfully add people until whatsmeow exposes the signaling operation and
authoritative group snapshots.

## Evidence-backed implementation order

1. Make `GroupJID` optional in the existing group-update parser and add a captured
   ad-hoc-group test.
2. Dispatch typed group-update events and acknowledge them through whatsmeow.
3. Store transaction-ordered group snapshots in call state and switch signaling
   destinations to `CALLID@call` only after group mode is established.
4. Add the singular active-call invite primitive; make any plural helper loop
   over that primitive and preserve per-target errors.
5. Add the separate initial-group offer path.
6. Add participant-indexed media, SFrame, and relay updates in meowcaller.
7. Expose the singular add operation and per-participant state in the web example.

Only step 1 is a self-contained correction to the already-landed parser module.
The later steps contain API and state-machine choices that should be reviewed one
module at a time.

Step 1 is implemented locally in whatsmeow commit `f6065a6`: the parser now uses
an optional group JID and has a regression test matching the captured ad-hoc
shape. The full whatsmeow test suite passes, and CodeRabbit reported no findings.
Nothing was pushed.

The next new module is blocked on repository-governance input: the current trees
do not contain the datasheet template or module registry required by `AGENTS.md`,
and the available Rust reference has no group-update/group-start implementation.
The capture corpus is sufficient evidence for behavior, but it is not yet an
approved authoritative source under that protocol.
