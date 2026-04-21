# chroncal-web e2e

Tiny Playwright harness that drives the web terminal.

## One-shot

```bash
# Terminal 1
make build build-web
./chroncal-web -addr 127.0.0.1:3131 -isolated

# Terminal 2
cd cmd/chroncal-web/e2e
npm i playwright        # first run only
node smoke.mjs http://127.0.0.1:3131/
```

## What it does

- Launches headless Chromium, opens the page.
- Waits for `window.__ws` (exposed by `static/index.html`) to reach `OPEN`.
- Hooks `onmessage` to buffer all server → client bytes.
- Waits for the first ≥500 bytes of TUI output.
- Sends a keystroke (`j`) by calling `window.__ws.send(...)`.
- Saves `smoke.png` and prints a 400-char ANSI-stripped snippet.

## Why `__ws.send` instead of `page.keyboard.type`?

`page.keyboard.type` would go through wterm's DOM input layer and translate keys
into the same bytes. For e2e it's simpler and more deterministic to skip that
layer and send the exact bytes the server expects (matches what a fixture file
would contain). Use `page.keyboard` when you're testing wterm itself, not the
TUI it's framing.

## Isolation

`chroncal-web -isolated` sets `CHRONCAL_DB=<tmpdir>/chroncal.db` per WebSocket
session and removes the tmpdir when the session closes. Each test gets a fresh
DB.
