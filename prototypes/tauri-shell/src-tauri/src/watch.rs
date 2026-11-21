//! What reaches the owner with no window open: the shell follows aided itself (LocalService.Watch,
//! as notify.ts does), posts notifications and keeps counts for the tray. Only the fields it needs are
//! decoded (prost skips the rest), so no generated code from proto/ is needed for the prototype.

use crate::upstream;
use bytes::{Buf, Bytes, BytesMut};
use http_body_util::{BodyExt, Full};
use hyper::Request;
use std::collections::HashMap;
use std::time::Duration;

#[derive(Clone, PartialEq, prost::Message)]
struct WatchResponse {
    #[prost(bool, tag = "1")]
    reset: bool,
    #[prost(message, repeated, tag = "13")]
    sessions: Vec<Session>,
    #[prost(string, repeated, tag = "14")]
    deleted_session_ids: Vec<String>,
}

#[derive(Clone, PartialEq, prost::Message)]
struct Session {
    #[prost(string, tag = "1")]
    session_id: String,
    #[prost(int32, tag = "6")]
    status: i32,
    #[prost(string, tag = "16")]
    title: String,
}

// aide.session.v1.SessionStatus
const QUEUED: i32 = 1;
const PREPARING: i32 = 2;
const RUNNING: i32 = 3;
const CRASHED: i32 = 8;
const WAITING_INPUT: i32 = 9;

pub struct Notice {
    pub title: String,
    pub body: String,
}

pub struct Counts {
    pub running: usize,
    pub waiting: usize,
    pub reachable: bool,
}

/// Follows aided for as long as the app runs; reconnects a second after it goes away.
pub async fn run(notify: impl Fn(Notice) + Send + 'static, counts: impl Fn(Counts) + Send + 'static) {
    let mut known: HashMap<String, (i32, String)> = HashMap::new();
    let mut first = true;
    loop {
        if let Err(e) = follow(&mut known, &mut first, &notify, &counts).await {
            eprintln!("aided watch: {e}");
        }
        counts(Counts { running: 0, waiting: 0, reachable: false });
        tokio::time::sleep(Duration::from_secs(1)).await;
    }
}

async fn follow(known: &mut HashMap<String, (i32, String)>, first: &mut bool, notify: &impl Fn(Notice), counts: &impl Fn(Counts)) -> Result<(), upstream::Error> {
    // An empty WatchRequest in Connect's envelope: flags 0, length 0.
    let body = Full::new(Bytes::from_static(&[0, 0, 0, 0, 0])).map_err(|e| match e {}).boxed();
    let req = Request::post("/aide.local.v1.LocalService/Watch")
        .header("host", "aided")
        .header("content-type", "application/connect+proto")
        .header("connect-protocol-version", "1")
        .header("aide-app-pid", std::process::id().to_string())
        .body(body)?;
    let mut res = upstream::send(req).await?;
    if !res.status().is_success() {
        return Err(format!("status {}", res.status()).into());
    }
    let mut buf = BytesMut::new();
    while let Some(frame) = res.body_mut().frame().await {
        if let Ok(data) = frame?.into_data() {
            buf.extend_from_slice(&data);
        }
        while buf.len() >= 5 {
            let flags = buf[0];
            let len = u32::from_be_bytes([buf[1], buf[2], buf[3], buf[4]]) as usize;
            if buf.len() < 5 + len {
                break;
            }
            buf.advance(5);
            let msg = buf.split_to(len);
            if flags & 0x02 != 0 {
                return Err(format!("stream ended: {}", String::from_utf8_lossy(&msg)).into());
            }
            let w: WatchResponse = prost::Message::decode(msg.freeze())?;
            apply(known, *first, w, notify);
            *first = false;
            let running = known.values().filter(|(s, _)| matches!(*s, QUEUED | PREPARING | RUNNING)).count();
            let waiting = known.values().filter(|(s, _)| *s == WAITING_INPUT).count();
            counts(Counts { running, waiting, reachable: true });
        }
    }
    Ok(())
}

/// Sessions seen before only tell of a change; on the first look, only of a question still waiting.
fn apply(known: &mut HashMap<String, (i32, String)>, first: bool, w: WatchResponse, notify: &impl Fn(Notice)) {
    for id in w.deleted_session_ids {
        known.remove(&id);
    }
    for s in w.sessions {
        let title = if s.title.is_empty() { s.session_id.clone() } else { s.title.clone() };
        let before = known.get(&s.session_id).map(|(st, _)| *st);
        if before != Some(s.status) && (!first || s.status == WAITING_INPUT) {
            match s.status {
                WAITING_INPUT => notify(Notice { title: "需要你".into(), body: title.clone() }),
                CRASHED => notify(Notice { title: "任务失败".into(), body: title.clone() }),
                RUNNING if before.is_none() => notify(Notice { title: "任务已开始".into(), body: title.clone() }),
                _ => {}
            }
        }
        known.insert(s.session_id, (s.status, title));
    }
}
