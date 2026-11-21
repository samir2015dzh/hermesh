//! One HTTP/1.1 connection to aided over its socket or pipe (what gateway.ts does with node:http).

use crate::paths::{socket, Socket};
use bytes::Bytes;
use http_body_util::combinators::BoxBody;
use hyper::body::Incoming;
use hyper::client::conn::http1;
use hyper::{Request, Response};
use hyper_util::rt::TokioIo;

pub type Body = BoxBody<Bytes, hyper::Error>;
pub type Error = Box<dyn std::error::Error + Send + Sync>;

pub async fn send(req: Request<Body>) -> Result<Response<Incoming>, Error> {
    match socket() {
        #[cfg(unix)]
        Socket::Unix(p) => {
            let io = tokio::net::UnixStream::connect(p).await?;
            round_trip(io, req).await
        }
        #[cfg(windows)]
        Socket::Pipe(name) => {
            let io = tokio::net::windows::named_pipe::ClientOptions::new().open(name)?;
            round_trip(io, req).await
        }
        #[allow(unreachable_patterns)]
        _ => Err("no local API on this system".into()),
    }
}

async fn round_trip<IO>(io: IO, req: Request<Body>) -> Result<Response<Incoming>, Error>
where
    IO: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send + 'static,
{
    let (mut sender, conn) = http1::handshake(TokioIo::new(io)).await?;
    tokio::spawn(async move {
        let _ = conn.await;
    });
    Ok(sender.send_request(req).await?)
}
