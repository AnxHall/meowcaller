# Datasheet: `voip/group_enc_rekey_ingest`

Typed parsing, Signal decryption, and dispatch of the participant keygen-v2 rekey
that follows a group-call roster transition.

**Validation vector:** `group_enc_rekey_corpus.json` — synthetic, non-secret
envelopes preserving the captured tag/attribute structure, encryption types,
versions, ciphertext sizes, author identity, and transaction relationships.

**Reference pinned at:**

- capture SHA-256 `d565e26f2ca48483525c5bf4dcc2c1bf5ae616299190d128f5f35f74ac50d6c6`
- capture SHA-256 `a91028746497b58d962f14fe5ed4d8036f3ca1c7f2091af5caa52f8430947def`
- whatsmeow commit `7dc1db147f07af4a7b8878a4823e516386547164`

The captures were approved by the human reviewer as authoritative. Live call
`D66652FC17BF1F8BBA898DE097B428FA` on 2026-07-24 independently corroborated the
boundary: transaction 17 activated the remaining participant, its packets reached
the expected SSRC but failed WARP authentication, and that same participant then
sent the transaction-17 `enc_rekey`.

## Reference source (verbatim — authoritative)

The immutable raw JSONL files remain the verbatim authority:

- `diag/captures/group-call-add-people-v2-20260723-112208.jsonl`
  (`8,372` events, `12,762,523` bytes)
- `diag/captures/group-call-multi-add-v2-20260723-135301.jsonl`
  (`7,192` events, `9,572,198` bytes)

The compact reports identify the relevant boundaries:

- single-add lines 4,223–4,388: roster/media update, joiner `enc_rekey`, and
  participant-key installation;
- single-add lines 5,647–6,026: remaining peer `enc_rekey` and roster reduction;
- multi-add lines 2,799–3,120: participant `enc_rekey`, media graph addition, and
  connected-key update.

Observed normalized wire shape:

```xml
<call from="<author-device>" id="<stanza-id>">
  <enc_rekey
      call-creator="<call-creator>"
      call-id="<call-id>"
      transaction-id="<uint32>">
    <encopt keygen="2"/>
    <enc type="msg|pkmsg" v="2"><opaque ciphertext></enc>
  </enc_rekey>
</call>
```

Observed facts:

```text
outer call[from] is the rekey author and matches a roster device
call-creator is separate from the author
keygen is 2 and enc version is 2
ciphertext sizes are 32, 146, or 231 bytes
Signal decryption produces exactly 32 raw key bytes
the rekey follows its matching group_update by 84–342 ms
an older transaction rekey can arrive after a newer group_update
not every group_update has a rekey
sframe_enabled, sframe_tx_enabled, and sframe_rx_enabled remain 0
```

The existing whatsmeow call-key path proves the Signal primitive:

```text
decryptIncomingCallKey locates enc, selects msg/pkmsg, and calls decryptDM
decryptDM uses the outer author JID's SignalAddress
decryptDM parses normal/pre-key Signal messages, decrypts, and removes v2 padding
```

## Go envelope (signatures only)

```go
package types

type GroupCallEncRekey struct {
	CallID            string
	CallCreator       JID
	TransactionID     uint32
	KeyGeneration     uint32
	EncryptionType    string
	EncryptionVersion uint32
	Ciphertext        []byte
}
```

```go
package voip

func ParseGroupCallEncRekey(node *waBinary.Node) (*types.GroupCallEncRekey, error)
```

```go
package events

type CallEncRekey struct {
	types.BasicCallMeta
	Rekey  types.GroupCallEncRekey
	RawKey []byte
	Data   *waBinary.Node
}
```

```go
package whatsmeow

func (cli *Client) onCallEncRekey(
	ctx context.Context,
	child *waBinary.Node,
	meta types.BasicCallMeta,
)

func newCallEncRekeyEvent(
	meta types.BasicCallMeta,
	rekey *types.GroupCallEncRekey,
	rawKey []byte,
	child *waBinary.Node,
) (*events.CallEncRekey, error)
```

## Implementation suggestions (guidance, not authoritative)

- Parse bounded decimal `transaction-id`, `keygen`, and `v` values.
- Require exactly one `encopt` and one `enc`; require byte ciphertext and clone it.
- Preserve the parsed envelope separately from the decrypted result.
- Select the existing `decryptDM` normal/pre-key path from `enc[type]`, using
  outer `call[from]` as the Signal author.
- Reject unsupported encryption types and decrypted results other than 32 bytes.
- Clone the raw key into the event; never log it, its ciphertext, or a fingerprint.
- Dispatch no typed event when parsing or decryption fails, but preserve the
  existing deferred call ACK behavior.
- Do not compare the rekey transaction only against the latest roster transaction;
  captured rekeys can be delayed behind newer snapshots.
- Do not mutate call state or install media keys in whatsmeow. Meowcaller consumes
  the typed event in the next module.
- Do not enable SFrame: the captured calls keep all frame-level SFrame flags off.
