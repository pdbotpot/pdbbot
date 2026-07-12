# Bot Roadmap

## Immediate fixes

- [ ] `automod-gc-invites` should exempt mods/admins (currently doesn't — runs before adminIDs fetch, needs to fetch or reorder)
- [ ] Config flag `enable_ai: bool` — when false, bot skips all LLM logic (no replies, no truth/dare). Automod still runs. Default true for akane, false for moderation/invite bots.

## Multi-account / multi-bot

### Login flow (need to capture endpoints)
- Intercept PDB email login on Waydroid:
  - `POST /auth/...` → request code sent to email
  - `POST /auth/...` → submit code, get tokens
- Build web UI: enter email → enter code → tokens saved, bot spawned

### Architecture
- One process, multiple `Bot` goroutines — one per account
- Each account has its own: `token.Manager`, `BotState`, `config.json`, `prompt.txt`
- Shared: LLM client (one API key), poll scheduler
- On startup: load all account dirs, spin up a bot per account
- Persist accounts to a registry file (e.g. `accounts.json`)

### Bot roles (via config per account)
- **AI bot** (akane account): `enable_ai: true`, responds to messages, truth/dare, !create-gc
- **Mod bot**: `enable_ai: false`, runs automod only (gc-links-only, mods-only, flood, etc.)
- **Invite bot**: `enable_ai: false`, separate account for IRL feed crawling (see irl-crawler-spec.md)

### Web UI (per-user self-service, future)
- User enters PDB email → bot requests code → user enters code → account created
- User sees their bot's status and can toggle automod settings
- Each user's bot is isolated (own account, own state)

## IRL crawler (separate bot account)
- Spec already written: irl-crawler-spec.md
- Needs its own account (minor-safe, separate from akane)
- Crawls IRL feed, sends hub GC invite link comments
- Rate limited, randomised messages, tracks contacted users
