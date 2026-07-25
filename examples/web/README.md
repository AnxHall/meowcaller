# Browser call console

This example pairs a whatsmeow client by QR code and exposes meowcaller's audio,
H.264 video, independent camera controls, orientation, and call reactions in a
localhost browser UI.

During an established audio call, enter one or more WhatsApp numbers or LIDs in
the **People to add** box, separated by commas or newlines, then choose **Add
people**. The event log reports each invite independently. A successful
`participant_invite` result means the audio-only invite was submitted. The page
then waits for an authoritative WhatsApp roster: `group_state` shows each
transaction, and `participant_join` is emitted only when the target is connected
with a selected PID-bearing device.

```sh
go run .
```

Open the printed `http://127.0.0.1:...` URL. On first run, scan the QR code from
WhatsApp under **Linked devices**. The SQLite session stays in this directory for
later runs.

Use `go run . -diagdump ./capture` to record sensitive call diagnostics for local
protocol research. Do not share those captures without reviewing their contents.
