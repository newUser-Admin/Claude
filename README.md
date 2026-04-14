# Claude Tools

A collection of AI interaction utilities: an iOS Safari response filter, a binary interaction engine, and a WebSocket-based browser terminal.

## Components

### AI Response Filter (`ai-response-filter.js`)

Hides user messages and UI chrome on major AI chat platforms, leaving only the assistant's responses visible.

**Supported platforms:** ChatGPT, Claude, Gemini, Perplexity, Copilot, DeepSeek, Meta AI, and a generic fallback.

**Usage:**
- **iOS Shortcut** — Add a "Run JavaScript on Web Page" action and paste the script.
- **Bookmarklet** — Minify the script and prefix with `javascript:`.

---

### Interaction Engine (`interaction-engine.js`)

PRO-SPEC binary interaction engine with four components:

| Component | Description |
|---|---|
| Binary Protocol | `ArrayBuffer`-based serializer/deserializer for a compact 13-byte wire format |
| Jitter Buffer | Priority-queue smoother that delays playback by a configurable window |
| Memory Vault | In-process history-less event log (no `localStorage` / `IndexedDB`) |
| Network Relay | Binary WebSocket transport with auto-reconnect awareness |

---

### WebSocket Shell (`shell-server.js` + `shell-client.html`)

Spawns a real PTY per connection and bridges it over a WebSocket, providing a full browser-based terminal.

#### Server (`shell-server.js`)

**Dependencies:**
```
npm install
```

**Run:**
```bash
npm start
# or
node shell-server.js
```

**Configuration (environment variables):**

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Listening port |
| `HOST` | `127.0.0.1` | Bind address |
| `SHELL_TOKEN` | `changeme` | Shared-secret auth token — **change this** |
| `SHELL` | `$SHELL` or `/bin/bash` | Shell binary to spawn |

> **Security:** This server grants full shell access to anyone holding the token. Never expose it on a public interface without proper authentication.

#### Client (`shell-client.html`)

Open `shell-client.html` in a browser. Enter the server URL (e.g. `ws://127.0.0.1:8080`) and the token, then connect.

## Requirements

- Node.js >= 18

## License

MIT
