#!/usr/bin/env node
// Smoke test: open the web terminal, wait for first frame, send a keystroke,
// screenshot, and dump a slice of the scrollback.
//
// Run against a server started with:
//   ./chroncal-web -addr 127.0.0.1:3131 -isolated
//
// Usage:  node smoke.mjs [url]
import { chromium } from "playwright";
import { writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const url = process.argv[2] || "http://127.0.0.1:3131/";
const outDir = dirname(fileURLToPath(import.meta.url));

// Prefer a system Chromium if available (avoids the ~150MB Playwright download).
const executablePath = process.env.PLAYWRIGHT_CHROMIUM || "/usr/bin/chromium";
const browser = await chromium.launch({ executablePath });
const ctx = await browser.newContext({ viewport: { width: 1280, height: 720 } });
const page = await ctx.newPage();
page.on("pageerror", (e) => console.error("pageerror:", e));

await page.goto(url);
await page.waitForFunction(() => window.__ws && window.__ws.readyState === 1, { timeout: 5000 });

// Collect server → client bytes for assertions.
await page.evaluate(() => {
  window.__buf = "";
  const prev = window.__ws.onmessage;
  window.__ws.onmessage = (ev) => {
    window.__buf += ev.data;
    prev?.(ev);
  };
});

// First paint settles.
await page.waitForFunction(() => window.__buf.length > 500, { timeout: 5000 });

// Navigate: j (next day), then q-ish — for now just dump what we saw.
await page.evaluate(() => window.__ws.send("j"));
await page.waitForTimeout(200);

const shotPath = join(outDir, "smoke.png");
await page.screenshot({ path: shotPath, fullPage: true });

const buf = await page.evaluate(() => window.__buf);
const textSnippet = buf.replace(/\x1b\[[0-9;?]*[a-zA-Z]/g, "").slice(0, 400);

console.log(`screenshot: ${shotPath}`);
console.log(`bytes received: ${buf.length}`);
console.log(`snippet (ansi stripped):\n${textSnippet}`);

const ok = /April 2026|Tuesday|chroncal/i.test(textSnippet) || buf.length > 1000;
await browser.close();

if (!ok) {
  console.error("FAIL: did not see expected TUI output");
  process.exit(1);
}
console.log("OK");
