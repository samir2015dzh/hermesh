# Tauri 外壳原型（原型，不合并）

这是工单「桌面外壳 Electron → Tauri 原型评估（只评估，不迁移）」的原型。评估报告在交接仓的 `TAURI-EVAL.md`。

- **不进主线**：本目录不在 pnpm 工作区，也不在任何 Cargo 工作区里；现有的 CI 工作流不构建它，`apps/desktop` 的代码一行没改。
- **本分支的 PR 只作评审载体，不合并**。所有者裁定之前，不许把任何内容带进主线。

## 做了什么

用 Tauri 2 包住现有的渲染层 `apps/desktop/src/renderer`，原样构建，只替换一个文件 `api.ts`。原生层只做到最小：

| 能力 | 实现 | 对应的 Electron 代码 |
|---|---|---|
| 窗口 | `src-tauri/src/main.rs` 的 `show_main`。关窗时销毁窗口和 WebView，托盘态不留页面进程 | `main/windows.ts` |
| 托盘 | 图标、左键开窗、右键菜单（打开 / 退出），提示文字显示运行中和需要你的会话数 | `main/tray.ts` |
| 系统通知 | Rust 侧自己连 aided 的 `LocalService.Watch`，会话新开始、需要回答、失败时发通知 | `main/notify.ts` |
| 连本机 aided | 页面的 Connect 传输层换成走 Tauri IPC 的 fetch：请求经 `invoke("aided_fetch")` 到 Rust，Rust 经 Unix socket / 命名管道转给 aided，响应体通过 Channel 逐块流式回传 | `app://aide/api` 协议加 `main/gateway.ts` |
| 数据根 | 照抄 `paths.ts`/`legacy.ts` 的规则，包括改名前 `Hermesh` 目录的回退，以及 Windows 上管道名按文件现读 | `main/paths.ts`、`main/legacy.ts` |

**为什么不用 `app://` 的等价物（自定义协议）**：Tauri 的自定义协议处理器只能一次性返回整个响应体，Connect 的服务端流（Watch、StreamSessionEvents）没法走。

**为什么也不用本机回环端口**：最初的版本用过本机端口加随机令牌，代理本身是实时的。但页面一侧要经过 WebView 的网络栈，在 Linux 的 WebKitGTK 上，每条消息的尾巴要等下一条消息到了才交给页面，表现为「实时刷新慢一拍」。改成 IPC 加 Channel 以后，冒烟测试里改名 19–271 ms 内就出现在窗口里，而且不再开任何端口。

`window.aide` 桥只给了渲染层读的几个值（平台、版本、语言）。其他方法都是空操作，所以设置、Her 控制台、更新窗口等页面在原型里**不可用**。这是有意的，缺口清单见报告 §3。

## 构建与运行（Linux）

```sh
# 系统依赖（Debian/Ubuntu）：libwebkit2gtk-4.1-dev libgtk-3-dev libayatana-appindicator3-dev librsvg2-dev libsoup-3.0-dev
pnpm install                                   # 在仓库根目录
node prototypes/tauri-shell/build-renderer.mjs # 渲染层 → prototypes/tauri-shell/dist
cd prototypes/tauri-shell/src-tauri && cargo build --release
AIDE_SOCKET=<aided 的 socket> ./target/release/aide-tauri-proto
```

不设 `AIDE_SOCKET` 时，按数据根规则找本机 aided。加 `--aide-at-login` 参数启动时只进托盘，不开窗口。

## Windows 包

`.github/workflows/tauri-prototype.yml` 只能在本分支上手动触发（workflow_dispatch）。它产出 `aide-tauri-proto.exe` 和 NSIS 安装包（按用户安装，标识 `io.aide.tauri-prototype`，与已装的 Aide 互不干扰）。原型不启动也不安装 aided，只连已装的 Aide 的 aided。

## 端到端测试

- `e2e/smoke.mjs`：用 W3C WebDriver 驱动原型（经 tauri-driver；Linux 上是 WebKitWebDriver，Windows 上是 msedgedriver）。依次打开总览、会话列表和每个会话；然后直接在 aided 里给一个会话改名，等窗口里不刷新就出现新名字；每页截图。
- `e2e/electron-shots.mjs`：对同一个 aided，用现有 Electron 版截同样的页面，做渲染对比。
- `measure/linux-footprint.mjs`：容器里两种外壳的内存（USS/PSS）。**不能代表 Windows**，Windows 的同页对比用交接仓的 `measure/tauri-compare.ps1`。

```sh
AIDE_DESKTOP_PREVIEW=3600 go test ./e2e -run '^TestDesktop$' -v   # 起一个带会话的 aided，日志里打印 socket
tauri-driver --native-driver /usr/bin/WebKitWebDriver &
AIDE_SOCKET=<socket> node prototypes/tauri-shell/e2e/smoke.mjs prototypes/tauri-shell/src-tauri/target/release/aide-tauri-proto
```
