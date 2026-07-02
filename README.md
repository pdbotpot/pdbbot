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

3. Set your PDB user ID in `config.json` (`self_user_id`).

## Run

```sh
# Groq (default)
go run ./cmd/akane/ -state state.json -config config.json -bot-state akane_state.json

# OpenAI
go run ./cmd/akane/ -state state.json -config config.json -bot-state akane_state.json -provider openai
```

Keys are loaded from `keys/<provider>.key`. To use a different keys directory: `-keys-dir /path/to/keys`.

## Chat commands

| Command | Effect |
|---------|--------|
| `!akanestop` / `!stopakane` | Silence Akane in this chat |
| `!akane` / `!akanestart` / `!startakane` | Wake Akane in this chat |
| `!akane <message>` | Wake and reply to message |
