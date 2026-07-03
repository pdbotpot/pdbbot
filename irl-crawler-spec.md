# IRL Crawler Bot

Separate account bot that crawls the PDB IRL (swiping) feed and sends invite comments pointing users to a hub group chat. Keeps the main Akane account clean in case of flagging.

## Purpose

Revive PDB group chat activity by reaching out to active users on the IRL feed with a link to a hub GC — a curated, read-only listing of active group chats.

## Hub GC

- Public, English
- Posting locked / automod deletes all messages (browse-only)
- Pinned content or description lists active GCs with links
- Invite link is stable and shared in every crawler comment

## Crawler loop

1. `GET /irl/feeds?attractiveUser=false[&nextCursor=...]` — paginate through feed
2. For each feed item, pick one IRL post UUID from `irls[]`
3. `POST /irl/{irl-uuid}/comment` with varied message + hub GC link
4. `POST /irl/feeds/read` with the user ID
5. Record user ID as contacted — skip on future runs
6. Sleep human-like delay between comments
7. When feed exhausted, back off and retry after cooldown

## Message variation

Pool of short templates, randomly selected and lightly varied per send. Identical repeated comments are the primary spam signal.

Example templates:
- "looking for active pdb groups? we made a hub for that [link]"
- "hey, there's a gc directory if you're looking for active groups [link]"
- "active pdb groups are hard to find, made a hub [link]"

## Rate limits

- Max ~20–30 comments/hour
- Per-session cap to avoid burst detection
- Randomised delay between actions (3–10s base + jitter)

## Implementation

Standalone `cmd/irl/main.go` binary, separate from Akane. Shares `internal/pdbapi` and `internal/token` packages. Own state file tracking contacted user IDs and last cursor position.

## Account setup

- Separate PDB account (second Google login on Waydroid)
- Own `state.json` and `config.json`
- Run independently of Akane instance
