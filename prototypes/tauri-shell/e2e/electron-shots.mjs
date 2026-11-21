// 原型，不合并：the same pages in today's Electron app, for the side-by-side screenshots of
// TAURI-EVAL.md §6 (smoke.mjs takes the Tauri ones). Run after `pnpm --dir apps/desktop build`:
//
//   AIDE_SOCKET=<the same daemon's socket> node electron-shots.mjs [shots dir]
import { createRequire } from "node:module";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const desktop = path.resolve(import.meta.dirname, "../../../apps/desktop");
const { _electron: electron } = createRequire(path.join(desktop, "package.json"))("@playwright/test");
const shots = process.argv[2] ?? path.join(import.meta.dirname, "..", "shots");
fs.mkdirSync(shots, { recursive: true });

const app = await electron.launch({
  args: [path.join(desktop, "out", "main", "index.js"), ...(process.getuid?.() === 0 ? ["--no-sandbox"] : [])],
  env: { ...process.env, AIDE_TEST_LANGUAGES: "zh-Hans", AIDE_USER_DATA: fs.mkdtempSync(path.join(os.tmpdir(), "aide-shots-")) },
});
const page = await app.firstWindow();
await page.locator(".side-nav, .rail-nav").first().waitFor({ timeout: 60_000 });
await page.locator(".side-nav button", { hasText: "概览" }).click();
await page.waitForTimeout(1500);
await page.screenshot({ path: path.join(shots, "electron-overview.png") });
await page.locator('[role=list][aria-label="任务"] [role=listitem]').last().click();
await page.waitForTimeout(1200);
await page.screenshot({ path: path.join(shots, "electron-session.png") });
await app.close();
console.log("saved to", shots);
