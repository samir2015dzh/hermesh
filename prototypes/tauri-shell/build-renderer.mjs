// 原型，不合并：builds apps/desktop/src/renderer unchanged for the Tauri shell, into ./dist.
// Run from anywhere: node prototypes/tauri-shell/build-renderer.mjs (after pnpm install at the root).
// The only difference from electron-vite's renderer build: api.ts is replaced by shim/api.ts, and
// pdf.js's data files are left out (no PDF is opened in the prototype).
import { createRequire } from "node:module";
import path from "node:path";
import { pathToFileURL } from "node:url";

const here = import.meta.dirname;
const desktop = path.resolve(here, "../../apps/desktop");
const renderer = path.join(desktop, "src/renderer");
// Vite and its plugins are the desktop app's own, resolved from there.
const req = createRequire(path.join(desktop, "package.json"));
const load = async (name) => import(pathToFileURL(req.resolve(name)).href);
const { build } = await load("vite");
const react = (await load("@vitejs/plugin-react")).default;
const tailwindcss = (await load("@tailwindcss/vite")).default;

const realApi = path.join(renderer, "src/api.ts");
const shimApi = path.join(here, "shim/api.ts");
// Vite/rolldown ids always use forward slashes; path.join on Windows does not, so compare normalized.
const same = (a, b) => path.resolve(a).replace(/\\/g, "/") === path.resolve(b).replace(/\\/g, "/");
const swapApi = {
  name: "aide-tauri-api",
  enforce: "pre",
  async resolveId(source, importer, options) {
    // The shim's own imports resolve as the real api.ts's would (the desktop app's dependencies).
    if (importer && same(importer, shimApi)) return this.resolve(source, realApi, { ...options, skipSelf: true });
    if (!importer || !/(^|\/)api(\.ts)?$/.test(source)) return null;
    const r = await this.resolve(source, importer, { ...options, skipSelf: true });
    return r && same(r.id, realApi) ? shimApi : null;
  },
};

await build({
  configFile: false,
  root: renderer,
  base: "./",
  plugins: [swapApi, react(), tailwindcss()],
  worker: { format: "es" },
  resolve: { dedupe: ["react", "react-dom"] },
  build: { outDir: path.join(here, "dist"), emptyOutDir: true },
  logLevel: "warn",
});
console.log("renderer built:", path.join(here, "dist"));
