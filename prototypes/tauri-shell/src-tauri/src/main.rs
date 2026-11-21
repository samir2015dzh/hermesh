//! Aide 桌面外壳的 Tauri 原型（只评估，不合并；交接仓 TAURI-EVAL.md）。
//!
//! What it does, and nothing more: the main window (apps/desktop/src/renderer, unchanged, built by
//! ../build-renderer.mjs), a tray icon with a short menu, notifications from aided's Watch, and the
//! page's way to aided (ipc.rs). Closing the window destroys it, so in the tray no web content
//! process is left (as the Electron app does, docs/resident.md).

#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod paths;
mod ipc;
mod upstream;
mod watch;

use std::sync::Mutex;
use tauri::menu::{Menu, MenuItem};
use tauri::tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent};
use tauri::{AppHandle, Manager, RunEvent, WebviewUrl, WebviewWindowBuilder};
use tauri_plugin_notification::NotificationExt;

/// The tray's tooltip, last set (counts from Watch).
struct Tip(Mutex<String>);

fn bridge() -> String {
    // The renderer calls window.aide?.x(...): with no bridge it runs as a page outside the app. The few
    // values it reads are given; any other method is a no-op that is also a thenable, so neither
    // `await window.aide.x()` nor a useEffect returning `window.aide.onX(cb)` breaks.
    let platform = if cfg!(windows) { "win32" } else if cfg!(target_os = "macos") { "darwin" } else { "linux" };
    format!(
        r#"window.__AIDE_ERRORS__ = [];
addEventListener("error", (e) => window.__AIDE_ERRORS__.push(String(e.message)));
addEventListener("unhandledrejection", (e) => window.__AIDE_ERRORS__.push(String(e.reason && (e.reason.stack || e.reason))));
(() => {{
  const nop = () => {{ const f = () => {{}}; f.then = (ok) => Promise.resolve(undefined).then(ok); return f; }};
  const base = {{ platform: {platform:?}, material: false, userName: "", version: {version:?}, language: "zh-CN", macos: 0 }};
  window.aide = new Proxy(base, {{ get: (t, k) => (k in t ? t[k] : typeof k === "string" ? nop : undefined) }});
}})();"#,
        version = env!("CARGO_PKG_VERSION"),
    )
}

fn show_main(app: &AppHandle) {
    if let Some(w) = app.get_webview_window("main") {
        let _ = w.show();
        let _ = w.unminimize();
        let _ = w.set_focus();
        return;
    }
    let mut w = WebviewWindowBuilder::new(app, "main", WebviewUrl::App("index.html".into()))
        .title("Aide")
        .inner_size(1240.0, 800.0)
        .min_inner_size(480.0, 620.0)
        .initialization_script(&bridge());
    // Eval only (TAURI-EVAL.md §2.1): AIDE_WEBVIEW_ARGS reaches WebView2's own switches, e.g.
    // --remote-debugging-port=9333 to read console and network over CDP. Unset: the defaults.
    if let Ok(args) = std::env::var("AIDE_WEBVIEW_ARGS") {
        w = w.additional_browser_args(&args);
    }
    let built = w.build();
    if let Err(e) = built {
        eprintln!("main window: {e}");
    }
}

fn main() {
    let at_login = std::env::args().any(|a| a == "--aide-at-login");
    let app = tauri::Builder::default()
        .plugin(tauri_plugin_single_instance::init(|app, _argv, _cwd| show_main(app)))
        .plugin(tauri_plugin_notification::init())
        .manage(ipc::Open::default())
        .invoke_handler(tauri::generate_handler![ipc::aided_fetch, ipc::aided_cancel])
        .setup(move |app| {
            app.manage(Tip(Mutex::new("Aide".into())));

            let open = MenuItem::with_id(app, "open", "打开 Aide", true, None::<&str>)?;
            let quit = MenuItem::with_id(app, "quit", "退出 Aide", true, None::<&str>)?;
            let menu = Menu::with_items(app, &[&open, &quit])?;
            let tray = TrayIconBuilder::with_id("aide")
                .icon(app.default_window_icon().cloned().expect("icon"))
                .tooltip("Aide")
                .menu(&menu)
                .show_menu_on_left_click(false)
                .on_menu_event(|app, e| match e.id.as_ref() {
                    "open" => show_main(app),
                    "quit" => app.exit(0),
                    _ => {}
                })
                .on_tray_icon_event(|tray, e| {
                    if let TrayIconEvent::Click { button: MouseButton::Left, button_state: MouseButtonState::Up, .. } = e {
                        show_main(tray.app_handle());
                    }
                })
                .build(app)?;

            let (h1, h2) = (app.handle().clone(), app.handle().clone());
            tauri::async_runtime::spawn(watch::run(
                move |n| {
                    let _ = h1.notification().builder().title(n.title).body(n.body).show();
                },
                move |c| {
                    let tip = if !c.reachable {
                        "Aide — 连不上 aided".to_string()
                    } else {
                        format!("Aide — {} 个运行中，{} 个需要你", c.running, c.waiting)
                    };
                    let state = h2.state::<Tip>();
                    let mut last = state.0.lock().unwrap();
                    if *last != tip {
                        let _ = tray.set_tooltip(Some(&tip));
                        *last = tip;
                    }
                },
            ));

            if !at_login {
                show_main(app.handle());
            }
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("tauri app");

    app.run(|_app, e| {
        // The last window closed (destroyed, its WebView with it): the app stays in the tray.
        if let RunEvent::ExitRequested { code: None, api, .. } = e {
            api.prevent_exit();
        }
    });
}
