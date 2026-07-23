# MODULES.md — group-call build registry

This registry covers the capture-driven WhatsApp group-call work layered on the
verified 1:1 rebuild. Each module is built under `AGENTS.md`, one at a time.

Status: `planned` → `scaffolded` → `implemented` → `verified`.

| # | Module | Package | Deps | Datasheet | Authority | KAT | Status |
|---|---|---|---|---|---|---|---|
| 01 | group_update_ingest | `go.mau.fi/whatsmeow` | existing `voip.ParseGroupUpdate`, group_call_state | [voip-group-update-ingest.md](datasheets/voip-group-update-ingest.md) | immutable capture corpus | `group_update_corpus.json` | scaffolded (`285aa8b`; KAT skipped on handler stub) |
| 02 | group_call_state | `go.mau.fi/whatsmeow` | existing `types.GroupCallUpdate` | [voip-group-call-state.md](datasheets/voip-group-call-state.md) | immutable capture corpus | `group_call_state_corpus.json` | verified (`5a6350b`; apply and routing KATs pass) |
