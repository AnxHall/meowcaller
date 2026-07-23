# MODULES.md — group-call build registry

This registry covers the capture-driven WhatsApp group-call work layered on the
verified 1:1 rebuild. Each module is built under `AGENTS.md`, one at a time.

Status: `planned` → `scaffolded` → `implemented` → `verified`.

| # | Module | Package | Deps | Datasheet | Authority | KAT | Status |
|---|---|---|---|---|---|---|---|
| 01 | group_update_ingest | `go.mau.fi/whatsmeow` | existing `voip.ParseGroupUpdate` | [voip-group-update-ingest.md](datasheets/voip-group-update-ingest.md) | immutable capture corpus | `group_update_corpus.json` | planned |
