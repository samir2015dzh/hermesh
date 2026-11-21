import { _electron as electron, expect, type ElectronApplication, type Locator, type Page } from "@playwright/test";
import { execFile, execFileSync } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { promisify } from "node:util";

export type Fixture = {
  socket: string;
  sessions: Record<string, string>;
  cli: string;
  cliEnv: string[];
  /**
   * Owner daemons nobody has set up yet, each with a relay of its own not set up either (first-run tests): claimCode
   * is that relay's setup code, which sets it up once.
   */
  fresh: { socket: string; relayURL: string; claimCode: string; gate?: string }[];
  /** A second Mac's daemon (its process group), a machine only with a session on it; 0 when none. */
  otherMac: number;
  /** Where git on this Mac finds github.com: owner/name is <github>/owner/name.git. */
  github: string;
  /** The dsh spec: the stand-in DSH Host running the Aide bridge plugin (0 when none). */
  dshHost?: number;
  /** Its DSH home. */
  dshHome?: string;
  dshLog?: string;
};

/** Loaded first in every launch: the app's windows open without taking focus. */
export const background = path.join(import.meta.dirname, "background.cjs");

export const fixture: Fixture = JSON.parse(fs.readFileSync(process.env.AIDE_E2E_FIXTURE ?? "", "utf8"));

/**
 * Starts the built app (pnpm build) against the fixture's daemon (or the one
 * at socket), with a new profile unless given one. The app follows the
 * system's languages, which macOS lets a single run override
 * (-AppleLanguages; elsewhere the app reads AIDE_TEST_LANGUAGES); the
 * tests read the app in Chinese unless they ask for other languages
 * (language.spec.ts).
 */
export async function launch(
  socket = fixture.socket,
  // systemColors: pages see the app's own light or dark (Playwright makes them light otherwise).
  // dark: the app in dark; args: more of Electron's (the download page's screenshots).
  // start: leave the window on the start page it picks (docs/her-team.md §14.1); otherwise the fixture's owner
  // window is put on Work, where most specs begin.
  o: { systemLanguages?: string[]; profile?: string; systemColors?: boolean; dark?: boolean; args?: string[]; env?: Record<string, string>; start?: boolean } = {},
): Promise<{ app: ElectronApplication; page: Page }> {
  const languages = o.systemLanguages ?? ["zh-Hans"];
  const app = await electron.launch({
    args: ["-r", background, path.join(process.env.AIDE_DESKTOP_OUT || path.join(import.meta.dirname, "..", "out"), "main", "index.js"), "-AppleLanguages", `(${languages.join(", ")})`, ...(o.args ?? [])],
    // Its own profile, so app instances of parallel runs don't meet.
    env: {
      ...process.env,
      AIDE_SOCKET: socket,
      ...(process.platform !== "darwin" ? { AIDE_TEST_LANGUAGES: languages.join(",") } : {}),
      AIDE_USER_DATA: o.profile ?? fs.mkdtempSync(path.join(os.tmpdir(), "hmdesk-")),
      ...o.env,
    },
    ...(o.systemColors ? { colorScheme: null } : o.dark ? { colorScheme: "dark" as const } : {}),
  });
  const page = await app.firstWindow();
  if (socket === fixture.socket && !o.start) await openWork(app, page);
  return { app, page };
}

/**
 * View → Work (⌘1), once the sidebar shows, so the window listens: a choice made before the start page is picked
 * keeps it from being picked.
 */
export async function openWork(app: ElectronApplication, page: Page): Promise<void> {
  await page.locator(".side-nav").waitFor({ timeout: 30_000 });
  await app.evaluate(({ Menu }) => Menu.getApplicationMenu()?.getMenuItemById("overview")?.click());
}

/** Runs the agent CLI, as the fixture's agent. */
export async function cli(...args: string[]): Promise<string> {
  const env = Object.fromEntries(fixture.cliEnv.map((kv) => kv.split(/=(.*)/s).slice(0, 2)));
  const { stdout } = await promisify(execFile)(fixture.cli, args, { env: { ...process.env, ...env } });
  return stdout;
}

/** Windows' words for this computer, where the app runs on a PC (src/shared/words.ts). */
export const onWindows = process.platform === "win32";
export const thisComputer = onWindows ? "这台电脑" : "这台 Mac";

/**
 * A Mac's words as the app says them on this system: on a PC "this computer" where a Mac's say "this Mac" (the
 * _win variants of src/locales), or `elsewhere` for a sentence that differs more.
 */
export function sys(mac: string, elsewhere?: string): string {
  if (!onWindows) return mac;
  return elsewhere ?? mac.replaceAll("这台 Mac ", "这台电脑").replaceAll("这台 Mac", "这台电脑").replaceAll("公司的 Mac", "公司的电脑");
}

/** With AIDE_SCREENS=<dir>, saves a screenshot of the page there (for looking at against the design). */
export async function snap(page: Page, name: string): Promise<void> {
  const dir = process.env.AIDE_SCREENS;
  if (!dir) return;
  fs.mkdirSync(dir, { recursive: true });
  await page.waitForTimeout(300);
  await page.screenshot({ path: path.join(dir, `${name}.png`) });
}

/** A git repository named name, for a project. */
export function gitRepo(name: string): string {
  // The long path, as aided reports it (Windows' temp folder may come as a short 8.3 one).
  const dir = path.join(fs.realpathSync.native(fs.mkdtempSync(path.join(os.tmpdir(), "hmrepo-"))), name);
  fs.mkdirSync(dir);
  fs.writeFileSync(path.join(dir, "README.md"), `# ${name}\n`);
  const env = { ...process.env, GIT_AUTHOR_NAME: "t", GIT_AUTHOR_EMAIL: "t@t", GIT_COMMITTER_NAME: "t", GIT_COMMITTER_EMAIL: "t@t" };
  for (const args of [["init", "-q", "-b", "main"], ["add", "-A"], ["commit", "-q", "-m", "init"]]) execFileSync("git", args, { cwd: dir, env });
  return dir;
}

/** The native folder panel can't be driven: it answers dir. */
export async function pickAnswers(app: ElectronApplication, dir: string) {
  await app.evaluate(({ dialog }, d) => {
    dialog.showOpenDialog = (async () => ({ canceled: false, filePaths: [d] })) as typeof dialog.showOpenDialog;
  }, dir);
}

/** The native menu can't be clicked: the next one shown answers id. */
export async function answerMenu(app: ElectronApplication, id: string) {
  await app.evaluate(({ ipcMain }, id) => {
    ipcMain.removeHandler("popup-menu");
    ipcMain.handle("popup-menu", () => id);
  }, id);
}


export { navTo, sessionList } from "./sessions";

/** Measure after navigation's column transition and the map's resize delivery. */
export async function mapHeight(map: Locator): Promise<number> {
  await map.locator(".lmap-box").evaluate((box) => Promise.all(box.closest(".mainwrap")!.getAnimations().map((a) => a.finished)));
  await expect.poll(() => map.locator(".lmap-box").evaluate((box) => {
    const stage = box.querySelector<HTMLElement>(".lmap-stage")!;
    const expected = document.createElement("div").style;
    expected.transform = `scale(${Math.min(1.15, box.clientWidth / stage.offsetWidth)})`;
    return stage.style.transform === expected.transform;
  })).toBe(true);
  return (await map.boundingBox())!.height;
}

/** Closing the target can interrupt the input acknowledgement itself. */
export async function clickToClose(button: Locator): Promise<void> {
  const page = button.page();
  await Promise.all([
    page.waitForEvent("close"),
    button.click({ noWaitAfter: true }).catch((error: unknown) => {
      if (!page.isClosed() || !(error instanceof Error) || !/^locator\.click: Target page, context or browser has been closed(?:\n|$)/.test(error.message)) throw error;
    }),
  ]);
}
