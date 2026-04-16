# leartech-go-common

This project is wired into the Leartech hub at `~/leartech/hub/`.

## Hub status (loaded automatically)

@~/leartech/hub/status/leartech-go-common.md

This is a **library repo** — released by tagging, no Docker image, no JX promotion. Imported by `leartech-auth-service`, `leartech-soc-collector`, and any future leartech Go service. Breaking API changes need coordinated bumps across consumers.

## Updating the hub

When significant state changes during this session — new package added, breaking change made, blocker hit — update `~/leartech/hub/status/leartech-go-common.md` directly. Keep it concise (it loads into every future session here). Commit and push so other sessions/machines see it.

Routine in-conversation progress belongs in auto-memory or PR descriptions, not the hub.
