//! Where aided answers: the Tauri counterpart of apps/desktop/src/main/paths.ts and legacy.ts
//! (internal/paths mirrors the same rules).

use std::path::{Path, PathBuf};

/// The daemon's local API: a Unix socket, or (Windows) a named pipe whose name aided writes to a file.
pub enum Socket {
    #[cfg_attr(windows, allow(dead_code))]
    Unix(PathBuf),
    #[cfg_attr(not(windows), allow(dead_code))]
    Pipe(String),
}

const IDENTITY: [&str; 3] = ["owner/current", "owner/owner.db", "machine/keys/machine.key"];

fn holds(dir: &Path) -> bool {
    IDENTITY.iter().any(|f| dir.join(f).exists())
}

/// dir, or the data directory from before the rename (…/Hermesh for …/Aide) when that one holds the identity.
fn kept(dir: PathBuf) -> PathBuf {
    let old = match dir.file_name().and_then(|n| n.to_str()) {
        Some("Aide") => "Hermesh",
        Some("aide") => "hermesh",
        _ => return dir,
    };
    if holds(&dir) {
        return dir;
    }
    let was = dir.with_file_name(old);
    if holds(&was) || (!dir.exists() && was.exists()) {
        was
    } else {
        dir
    }
}

pub fn data_dir() -> PathBuf {
    if let Some(d) = std::env::var_os("AIDE_DATA_DIR").or_else(|| std::env::var_os("HERMESH_DATA_DIR")) {
        return PathBuf::from(d);
    }
    let home = std::env::var_os(if cfg!(windows) { "USERPROFILE" } else { "HOME" }).map(PathBuf::from).unwrap_or_default();
    if cfg!(windows) {
        let local = std::env::var_os("LOCALAPPDATA").map(PathBuf::from).unwrap_or_else(|| home.join("AppData").join("Local"));
        return kept(local.join("Aide"));
    }
    if cfg!(target_os = "macos") {
        return kept(home.join("Library/Application Support/Aide"));
    }
    match std::env::var_os("XDG_DATA_HOME") {
        Some(x) => kept(PathBuf::from(x).join("aide")),
        None => kept(home.join(".local/share/aide")),
    }
}

/// Looked up again for each connection: on Windows the pipe's name changes whenever aided starts.
pub fn socket() -> Socket {
    let set = std::env::var("AIDE_SOCKET").ok().filter(|s| !s.is_empty());
    if cfg!(windows) {
        let file = set.unwrap_or_else(|| data_dir().join("run").join("aided.pipe").to_string_lossy().into_owned());
        if file.starts_with(r"\\.\pipe\") {
            return Socket::Pipe(file);
        }
        let name = std::fs::read_to_string(&file).map(|s| s.trim().to_string()).unwrap_or_default();
        return Socket::Pipe(if name.is_empty() { file } else { name });
    }
    Socket::Unix(set.map(PathBuf::from).unwrap_or_else(|| data_dir().join("run").join("aided.sock")))
}
