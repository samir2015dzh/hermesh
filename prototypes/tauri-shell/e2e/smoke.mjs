// 原型，不合并：the Tauri prototype end to end, driven as Playwright drives the Electron app, but over
// W3C WebDriver (tauri-driver, which starts WebKitWebDriver on Linux and msedgedriver on Windows). No
// test runner, no dependencies: it shows how much of a spec carries over (TAURI-EVAL.md §5).
//
//   AIDE_SOCKET=<a daemon's socket> node smoke.mjs <path to aide-tauri-proto> [shots dir]
//
// tauri-driver must be running (port 4444). It opens the overview, the session list and each session,
// renames a session through aided directly and waits for the window to show it without a reload (the
// shell's Watch streaming through its proxy), and saves a screenshot of each page.
import fs from "node:fs";
import http from "node:http";
import path from "node:path";

const [bin, shots = path.join(import.meta.dirname, "..", "shots")] = process.argv.slice(2);
const socket = process.env.AIDE_SOCKET;
if (!bin || !socket) throw new Error("usage: AIDE_SOCKET=… node smoke.mjs <app> [shots]");
fs.mkdirSync(shots, { recursive: true });
const driver = process.env.TAURI_DRIVER ?? "http://127.0.0.1:4444";

async function wd(method, url, body) {
  const r = await fetch(driver + url, { method, headers: { "content-type": "application/json" }, body: body && JSON.stringify(body) });
  const j = await r.json();
  if (!r.ok) throw new Error(`${method} ${url}: ${JSON.stringify(j.value)}`);
  return j.value;
}

/** aided's LocalService over its socket, in Connect's JSON (what the page does in binary). */
function aided(method, body) {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const req = http.request({ socketPath: socket, path: `/aide.local.v1.LocalService/${method}`, method: "POST", headers: { "content-type": "application/json", "content-length": Buffer.byteLength(data) } }, (res) => {
      let out = "";
      res.on("data", (c) => (out += c));
      res.on("end", () => (res.statusCode === 200 ? resolve(JSON.parse(out)) : reject(new Error(`${method}: ${res.statusCode} ${out}`))));
    });
    req.on("error", reject);
    req.end(data);
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, f, timeout = 30_000) {
  const end = Date.now() + timeout;
  let last;
  for (;;) {
    const v = await f().catch((e) => void (last = e));
    if (v) return v;
    if (Date.now() > end) throw new Error(`timed out: ${what}${last ? ` (${last.message})` : ""}`);
    await sleep(250);
  }
}

const started = Date.now();
const session = await wd("POST", "/session", { capabilities: { alwaysMatch: { "tauri:options": { application: path.resolve(bin) } } } });
const s = `/session/${session.sessionId}`;
const js = (script, ...args) => wd("POST", `${s}/execute/sync`, { script, args });
const shot = async (name) => fs.writeFileSync(path.join(shots, `${name}.png`), Buffer.from(await wd("GET", `${s}/screenshot`), "base64"));
let failed = false;
try {
  // The sidebar: the page is up and aided answered through the proxy (Watch's first message).
  await until("the sidebar", () => js(`return !!document.querySelector(".side-nav, .rail-nav")`), 60_000);
  console.log(`window with its sidebar: ${Date.now() - started} ms after the session was asked for`);

  // The overview, from the sidebar.
  await js(`[...document.querySelectorAll(".side-nav button")].find((b) => /概览/.test(b.textContent))?.click()`);
  await sleep(1500);
  await shot("tauri-overview");

  // The session list, and each session in turn.
  const rows = `[role=list][aria-label="任务"] [role=listitem]`;
  const titles = await until("the session list", () => js(`const r = [...document.querySelectorAll(arguments[0])]; return r.length ? r.map((x) => x.textContent) : null`, rows));
  console.log(`session list: ${titles.length} sessions`);
  for (let i = 0; i < titles.length; i++) {
    await js(`document.querySelectorAll(arguments[0])[arguments[1]]?.click()`, rows, i);
    await sleep(1200);
    if (i === titles.length - 1) await shot("tauri-session");
  }

  // Live: a session renamed in aided shows in the window as it is (Watch, streamed through the proxy).
  const { sessions } = await aided("ListSessions", { limit: 50 });
  const target = sessions.find((x) => x.status !== "SESSION_STATUS_ARCHIVED") ?? sessions[0];
  const title = `实时刷新 ${Date.now() % 100000}`;
  await aided("RenameSession", { sessionId: target.sessionId, title });
  const t0 = Date.now();
  await until("the renamed session in the window", () => js(`return document.body.innerText.includes(arguments[0])`, title), 15_000);
  console.log(`rename shown in the window ${Date.now() - t0} ms after aided took it`);
  await shot("tauri-live");
  await aided("RenameSession", { sessionId: target.sessionId, title: target.title });
  console.log("PASS");
} catch (e) {
  failed = true;
  console.error("FAIL:", e.message, `after ${Date.now() - started} ms`);
  console.error(await js(`return location.href + " " + document.getElementById("root")?.innerHTML.slice(0, 200)`).catch((x) => x.message));
  await shot("tauri-failure").catch(() => {});
} finally {
  await wd("DELETE", s).catch(() => {});
}
process.exit(failed ? 1 : 0);
