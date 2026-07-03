# akane

PDB group chat bot. Watches group chats and responds as Akane.

## Setup

1. Get a valid `state.json` (PDB auth tokens) by intercepting the PDB Android app with Frida/mitmproxy
   and capturing the login response. Seed the file with:
   ```json
   {
     "access_token": "eyJ...",
     "refresh_token": "eyJ...",
     "expire_at": 0,
     "device_id": "your-device-id"
   }
   ```
   The bot auto-refreshes the access token; only `refresh_token` and `device_id` need to be accurate.

2. Create API key files:
   ```sh
   mkdir keys
   echo "gsk_..." > keys/groq.key      # Groq API key
   echo "sk-proj-..." > keys/openai.key # OpenAI API key
   ```
   Do not save keys with a BOM (UTF-8 BOM will corrupt the key).

3. Set your PDB user ID in `config.json` (`self_user_id`).

## Run

```sh
# Groq (default)
go run ./cmd/akane/ -state state.json -config config.json -bot-state akane_state.json

# OpenAI
go run ./cmd/akane/ -state state.json -config config.json -bot-state akane_state.json -provider openai

# Override model
go run ./cmd/akane/ -state state.json -config config.json -bot-state akane_state.json -model llama-3.3-70b-versatile
```

Keys are loaded from `keys/<provider>.key`. To use a different keys directory: `-keys-dir /path/to/keys`.

## Files to deploy

Copy these to the server:

| File | Purpose |
|------|---------|
| `akane` | Compiled binary |
| `state.json` | PDB auth tokens |
| `akane_state.json` | Bot state (channel flags, flood memory) |
| `config.json` | Bot config |
| `keys/groq.key` | Groq API key |
| `keys/openai.key` | OpenAI API key |

## Config

`config.json` key options (all optional, defaults shown):

```json
{
  "poll_interval_sec": 15,
  "idle_after_sec": 300,
  "idle_poll_interval_sec": 30,
  "deep_idle_after_sec": 900,
  "deep_idle_poll_interval_sec": 90,
  "ambient_prob": 0.10,
  "per_channel_cooldown_sec": 2,
  "max_replies_per_channel_per_hour": 200,
  "max_replies_global_per_hour": 500,
  "history_len": 12,
  "convo_mode_minutes": 4,
  "convo_mode_ambient_minutes": 1,
  "active_hours": { "tz": "Europe/Tallinn", "start": "10:00", "end": "00:30" },
  "self_user_id": "4885554",
  "dry_run": false,
  "providers": {
    "groq": {
      "base_url": "https://api.groq.com/openai/v1",
      "model": "openai/gpt-oss-20b",
      "fallback_models": ["llama-3.3-70b-versatile"]
    },
    "openai": {
      "base_url": "https://api.openai.com/v1",
      "model": "gpt-4o-mini"
    }
  }
}
```

`fallback_models` are tried left to right when the primary model returns 429 (quota exhausted).

## Chat commands

All commands work regardless of whether Akane is enabled or disabled.

### Bot control (mod/admin only)

| Command | Effect |
|---------|--------|
| `!akane-disable` | Fully silence Akane; only a mod can re-enable |
| `!akane-enable` | Re-enable Akane (lifts mod-lock or disabled state) |

### Bot control (anyone)

| Command | Effect |
|---------|--------|
| `!akanestop` / `!stopakane` / `!akane stop` | Silence Akane in this chat |
| `!akane` / `!akanestart` / `!startakane` | Wake Akane in this chat |
| `!akane <message>` | Wake and reply to message |
| `!truth` / `!dare` | Generate a truth question or dare |
| `!create-gc <name>` | Create a new public group chat; bot DMs the invite link and transfers ownership when you send your first message |
| `!server-conf` | Print current channel config |

### Automod (mod/admin only)

| Command | Effect |
|---------|--------|
| `!automod-gc-invites` | Toggle: delete messages containing GC invite links |
| `!auto-delete-events` | Toggle: delete system event messages (join/leave/role changes) |
| `!purge-events` | One-shot: delete all existing event messages in the chat |
| `!mods-only-chat-mode` | Toggle: delete all non-mod/admin messages |
| `!gc-links-only` | Toggle: delete all messages that don't contain a GC invite link (mods exempt) |
| `!no-duplicates` | Toggle: delete messages whose text was already seen in recent history (mods exempt) |
| `!anti-flood <N>` | Toggle flood detection; `<N>` sets max identical messages per poll cycle (default 3) |

## Tools

```sh
# Delete all event messages from a named group chat
go run ./cmd/purgeevents/ state.json "GC NAME"
```
