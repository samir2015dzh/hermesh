// 原型，不合并：the two shells' memory on Linux, against the same daemon (TAURI-EVAL.md §2, the
// container's numbers). They do NOT stand for Windows: WebKitGTK is not WebView2, and Linux has no
// private working set. Per process: USS (Private_Clean + Private_Dirty of /proc/<pid>/smaps_rollup,
// the closest to Windows' private working set) and PSS, summed over each app's process tree.
//
//   AIDE_SOCKET=<socket> node linux-footprint.mjs <aide-tauri-proto> [seconds per window]
// with tauri-driver on :4444 and apps/desktop built. Scenarios: window (overview, idle), browse (each
// session opened, back to the overview), tray (Electron: its windows closed, the app left in the tray;
// Tauri: started with --aide-at-login, no window opened, since WebKitWebDriver ends the app with its
// last window).
import { execFileSync, spawn } from "node:child_process";
import { createRequire } from "node:module";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const [bin, secsArg = "20"] = process.argv.slice(2);
const secs = Number(secsArg);
const desktop = path.resolve(import.meta.dirname, "../../../apps/desktop");
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const rows = `[role=list][aria-label="任务"] [role=listitem]`;

function tree(root) {
  const kids = new Map();
  for (const d of fs.readdirSync("/proc").filter((d) => /^\d+$/.test(d))) {
    try {
      const ppid = Number(fs.readFileSync(`/proc/${d}/stat`, "utf8").split(") ")[1].split(" ")[1]);
      if (!kids.has(ppid)) kids.set(ppid, []);
      kids.get(ppid).push(Number(d));
    } catch {}
  }
  const out = [];
  const q = [root];
  while (q.length) {
    const p = q.shift();
    out.push(p);
    q.push(...(kids.get(p) ?? []));
  }
  return out;
}

function label(pid) {
  const name = fs.readFileSync(`/proc/${pid}/comm`, "utf8").trim();
  // Chromium rewrites its children's argv into one string: split on spaces too.
  const cmd = fs.readFileSync(`/proc/${pid}/cmdline`, "utf8").split(/\0| /);
  const type = cmd.find((a) => a.startsWith("--type="))?.slice(7);
  const sub = cmd.find((a) => a.startsWith("--utility-sub-type="))?.split(".").pop();
  return type ? `${name}:${type}${sub ? `(${sub})` : ""}` : name;
}

function mem(pid) {
  const t = fs.readFileSync(`/proc/${pid}/smaps_rollup`, "utf8");
  const kb = (k) => Number(new RegExp(`^${k}:\\s+(\\d+)`, "m").exec(t)?.[1] ?? 0);
  return { uss: (kb("Private_Clean") + kb("Private_Dirty")) / 1024, pss: kb("Pss") / 1024 };
}

/** Averages each label's USS / PSS (and the sum) over samples every 2 s for secs. */
async function sample(root) {
  const per = new Map();
  let n = 0;
  for (const end = Date.now() + secs * 1000; Date.now() < end; await sleep(2000)) {
    n++;
    for (const pid of tree(root)) {
      try {
        const l = label(pid);
        const m = mem(pid);
        const v = per.get(l) ?? { uss: 0, pss: 0, count: 0 };
        per.set(l, { uss: v.uss + m.uss, pss: v.pss + m.pss, count: v.count + 1 });
      } catch {}
    }
  }
  const rows = [...per].map(([l, v]) => ({ label: l, procs: +(v.count / n).toFixed(1), uss: +(v.uss / n).toFixed(1), pss: +(v.pss / n).toFixed(1) }));
  return { rows, uss: +rows.reduce((a, r) => a + r.uss, 0).toFixed(1), pss: +rows.reduce((a, r) => a + r.pss, 0).toFixed(1) };
}

const results = {};

// --- Electron (Playwright), as the desktop's e2e drives it.
{
  const { _electron: electron } = createRequire(path.join(desktop, "package.json"))("@playwright/test");
  const app = await electron.launch({
    args: [path.join(desktop, "out", "main", "index.js"), ...(process.getuid?.() === 0 ? ["--no-sandbox"] : [])],
    env: { ...process.env, AIDE_TEST_LANGUAGES: "zh-Hans", AIDE_USER_DATA: fs.mkdtempSync(path.join(os.tmpdir(), "aide-mem-")) },
  });
  const pid = app.process().pid;
  const page = await app.firstWindow();
  await page.locator(".side-nav").first().waitFor({ timeout: 60_000 });
  await page.locator(".side-nav button", { hasText: "概览" }).click();
  await sleep(5000);
  results.electron = { window: await sample(pid) };
  const n = await page.locator(rows).count();
  for (let i = 0; i < n; i++) {
    await page.locator(rows).nth(i).click();
    await sleep(1500);
  }
  await page.locator(".side-nav button", { hasText: "概览" }).click();
  await sleep(5000);
  results.electron.browse = await sample(pid);
  await app.evaluate(({ BrowserWindow }) => BrowserWindow.getAllWindows().forEach((w) => w.close()));
  await sleep(10_000);
  results.electron.tray = await sample(pid);
  await app.close();
}

// --- Tauri (tauri-driver / WebKitWebDriver).
{
  const d = "http://127.0.0.1:4444";
  const wd = async (m, u, b) => (await (await fetch(d + u, { method: m, headers: { "content-type": "application/json" }, body: b && JSON.stringify(b) })).json()).value;
  const s = (await wd("POST", "/session", { capabilities: { alwaysMatch: { "tauri:options": { application: path.resolve(bin) } } } })).sessionId;
  const js = (script, ...args) => wd("POST", `/session/${s}/execute/sync`, { script, args });
  for (let i = 0; i < 120 && !(await js(`return !!document.querySelector(".side-nav")`)); i++) await sleep(500);
  const pid = Number(execFileSync("pgrep", ["-n", "-f", path.resolve(bin)], { encoding: "utf8" }).trim());
  const overview = `[...document.querySelectorAll(".side-nav button")].find((b) => /概览/.test(b.textContent))?.click()`;
  await js(overview);
  await sleep(5000);
  results.tauri = { window: await sample(pid) };
  const n = await js(`return document.querySelectorAll(arguments[0]).length`, rows);
  for (let i = 0; i < n; i++) {
    await js(`document.querySelectorAll(arguments[0])[arguments[1]]?.click()`, rows, i);
    await sleep(1500);
  }
  await js(overview);
  await sleep(5000);
  results.tauri.browse = await sample(pid);
  await wd("DELETE", `/session/${s}`);
}
{
  const app = spawn(path.resolve(bin), ["--aide-at-login"], { stdio: "ignore" });
  await sleep(15_000);
  results.tauri.tray = await sample(app.pid);
  app.kill();
}

console.log(JSON.stringify(results, null, 1));
