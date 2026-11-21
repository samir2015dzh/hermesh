//! The page's way to aided: the Tauri counterpart of app://aide/api (index.ts serve() + gateway.ts).
//!
//! Electron's page fetches app://aide/api/…, which the main process forwards to aided's socket. A
//! Tauri custom protocol cannot do the same for Connect's server streams (Watch, StreamSessionEvents):
//! its handler answers with a whole body. A loopback port (tried first) streams, but the page then
//! goes through the WebView's network stack, and WebKitGTK held back the end of each message until
//! the next arrived. So the page's Connect transport is given a fetch of its own (shim/api.ts) that
//! calls aided_fetch: the shell sends the request to aided and passes the body back chunk by chunk
//! over a Channel, in order, as aided writes it. No port is opened; only this app's page can call it.

use crate::upstream;
use bytes::Bytes;
use http_body_util::{BodyExt, Full};
use hyper::Request;
use serde::Serialize;
use std::collections::HashMap;
use std::sync::Mutex;
use tauri::ipc::{Channel, Response};
use tauri::async_runtime::JoinHandle;

const SERVICE: &str = "aide.local.v1.LocalService";
const FORWARDED: [&str; 5] = ["content-type", "connect-protocol-version", "connect-timeout-ms", "connect-content-encoding", "connect-accept-encoding"];

/// Bodies still being passed to the page, by the page's id for its request: cancelled when it aborts.
#[derive(Default)]
pub struct Open(Mutex<HashMap<u32, JoinHandle<()>>>);

#[derive(Serialize)]
pub struct Head {
    status: u16,
    headers: Vec<(String, String)>,
}

fn allowed(path: &str) -> bool {
    path.strip_prefix('/')
        .and_then(|p| p.split_once('/'))
        .is_some_and(|(svc, m)| svc == SERVICE && !m.is_empty() && m.chars().all(|c| c.is_ascii_alphanumeric() || c == '_'))
}

/// Sends the page's request to aided; answers with the status and headers, then sends the body on
/// `chunks`, each piece as aided wrote it, and an empty piece at its end.
#[tauri::command]
pub async fn aided_fetch(id: u32, path: String, headers: Vec<(String, String)>, body: Vec<u8>, chunks: Channel<Response>, open: tauri::State<'_, Open>) -> Result<Head, String> {
    if !allowed(&path) {
        let _ = chunks.send(Response::new(Vec::new()));
        return Ok(Head { status: 403, headers: vec![] });
    }
    let mut req = Request::post(path.as_str()).header("host", "aided").header("aide-viewer", "window");
    for (k, v) in headers.iter().filter(|(k, _)| FORWARDED.contains(&k.to_ascii_lowercase().as_str())) {
        req = req.header(k.as_str(), v.as_str());
    }
    let req = req.body(Full::new(Bytes::from(body)).map_err(|e| match e {}).boxed()).map_err(|e| e.to_string())?;
    let mut res = match upstream::send(req).await {
        Ok(r) => r,
        Err(e) => {
            // As gateway.ts answers when aided cannot be reached: Connect reads it as unavailable.
            let msg = format!("{{\"code\":\"unavailable\",\"message\":\"aided: {}\"}}", e.to_string().replace('"', "'"));
            let _ = chunks.send(Response::new(msg.into_bytes()));
            let _ = chunks.send(Response::new(Vec::new()));
            return Ok(Head { status: 503, headers: vec![("content-type".into(), "application/json".into())] });
        }
    };
    let head = Head {
        status: res.status().as_u16(),
        headers: res
            .headers()
            .iter()
            .filter(|(k, _)| k.as_str().starts_with("connect-") || k.as_str() == "content-type" || k.as_str().starts_with("grpc-"))
            .filter_map(|(k, v)| Some((k.to_string(), v.to_str().ok()?.to_string())))
            .collect(),
    };
    let task = tauri::async_runtime::spawn(async move {
        while let Some(Ok(frame)) = res.body_mut().frame().await {
            if let Ok(data) = frame.into_data() {
                if !data.is_empty() && chunks.send(Response::new(data.to_vec())).is_err() {
                    return;
                }
            }
        }
        let _ = chunks.send(Response::new(Vec::new()));
    });
    open.0.lock().unwrap().insert(id, task);
    Ok(head)
}

/// The page aborted a request (a Watch it no longer needs): its body stops, and aided's connection closes.
#[tauri::command]
pub fn aided_cancel(id: u32, open: tauri::State<'_, Open>) {
    if let Some(t) = open.0.lock().unwrap().remove(&id) {
        t.abort();
    }
}
