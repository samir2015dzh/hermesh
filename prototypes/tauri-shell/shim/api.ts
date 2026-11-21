// 原型，不合并：apps/desktop/src/renderer/src/api.ts 在 Tauri 外壳里的替身（build-renderer.mjs 换上）。
// Electron's page reaches aided at app://aide/api through the main process. Here the transport is
// given a fetch that goes through the shell instead (src-tauri/src/ipc.rs): the request by invoke,
// the response's body back over a Channel, streamed, so Watch and every server stream work as before.
import { createClient } from "@connectrpc/connect";
import { createConnectTransport } from "@connectrpc/connect-web";
import { LocalService } from "@aide/proto/local";

type Tauri = {
  core: {
    invoke<T>(cmd: string, args: Record<string, unknown>): Promise<T>;
    Channel: new () => { onmessage: (m: ArrayBuffer) => void };
  };
};
const tauri = (window as unknown as { __TAURI__?: Tauri }).__TAURI__;
// Without the shell's global the transport below falls back to app://aide/api, which nothing answers.
if (!tauri) console.error("[aide] window.__TAURI__ is missing: this renderer is not running in the Tauri shell.");
let nextId = 1;

async function shellFetch(input: RequestInfo | URL, init?: RequestInit): Promise<Response> {
  const req = new Request(input, init);
  const path = new URL(req.url).pathname.replace(/^\/api/, "");
  const body = Array.from(new Uint8Array(await req.arrayBuffer()));
  const id = nextId++;
  const cancel = () => void tauri!.core.invoke("aided_cancel", { id });
  let ctrl!: ReadableStreamDefaultController<Uint8Array>;
  let finished = false;
  const stream = new ReadableStream<Uint8Array>({ start: (c) => void (ctrl = c), cancel });
  const chunks = new tauri!.core.Channel();
  chunks.onmessage = (m) => {
    if (finished) return;
    const b = new Uint8Array(m);
    if (b.length > 0) return ctrl.enqueue(b);
    finished = true;
    ctrl.close();
  };
  init?.signal?.addEventListener("abort", () => {
    cancel();
    if (!finished) {
      finished = true;
      ctrl.error(new DOMException("aborted", "AbortError"));
    }
  });
  const head = await tauri!.core.invoke<{ status: number; headers: [string, string][] }>("aided_fetch", { id, path, headers: [...req.headers.entries()], body, chunks });
  return new Response(head.status === 204 ? null : stream, { status: head.status, headers: head.headers });
}

const transport = tauri
  ? createConnectTransport({ baseUrl: "http://aided/api", useBinaryFormat: true, fetch: shellFetch })
  : createConnectTransport({ baseUrl: "app://aide/api", useBinaryFormat: true });
export const local = createClient(LocalService, transport);
