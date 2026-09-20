//! Helpers around opening the AT-SPI accessibility bus connection and
//! constructing proxies for individual object references.

use anyhow::{anyhow, Context, Result};
use atspi::object_ref::ObjectRefOwned;
use atspi::proxy::accessible::AccessibleProxy;
use atspi::proxy::action::ActionProxy;
use atspi::proxy::application::ApplicationProxy;
use atspi::proxy::component::ComponentProxy;
use atspi::proxy::editable_text::EditableTextProxy;
use atspi::proxy::text::TextProxy;
use atspi::proxy::value::ValueProxy;
use atspi::zbus::names::{BusName, UniqueName};
use atspi::zbus::zvariant::ObjectPath;
use atspi::zbus::Address;
use atspi::AccessibilityConnection;
use std::env;
use std::fs;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

static DISCOVERY_LOG: Mutex<Vec<String>> = Mutex::new(Vec::new());

fn log_attempt(msg: impl Into<String>) {
    if let Ok(mut guard) = DISCOVERY_LOG.lock() {
        guard.push(msg.into());
    }
}

/// Recent discovery attempts, useful for error reporting. Cleared on each
/// `ensure_bus_address` invocation.
pub fn discovery_log() -> Vec<String> {
    DISCOVERY_LOG.lock().map(|g| g.clone()).unwrap_or_default()
}

/// Open the accessibility bus on the current session.
///
/// `AccessibilityConnection::new()` always tries the session bus first and
/// queries `org.a11y.Bus.GetAddress`. In the Memoh workspace container the
/// helper is exec'd from the Go bridge in a fresh shell with no session bus
/// running, so we discover the a11y bus address ourselves (via /proc and
/// cache-path probing) and feed it straight into `from_address`, bypassing
/// the session-bus indirection.
pub async fn open() -> Result<AccessibilityConnection> {
    ensure_bus_address();
    let attempted = current_bus_address();

    let raw = attempted.clone().ok_or_else(|| {
        let mut msg = String::from(
            "no AT-SPI accessibility bus address could be resolved (no live --address= in /proc and no socket at cache paths)",
        );
        let log = discovery_log();
        if !log.is_empty() {
            msg.push_str("; discovery: ");
            msg.push_str(&log.join(" | "));
        }
        anyhow!(msg)
    })?;

    let parsed: Address = raw
        .parse()
        .with_context(|| format!("invalid AT-SPI bus address {raw}"))?;

    AccessibilityConnection::from_address(parsed)
        .await
        .with_context(|| {
            let mut detail =
                format!("failed to connect to the AT-SPI accessibility bus (tried {raw})");
            let log = discovery_log();
            if !log.is_empty() {
                detail.push_str("; discovery: ");
                detail.push_str(&log.join(" | "));
            }
            detail
        })
}

fn ensure_bus_address() {
    if let Ok(mut guard) = DISCOVERY_LOG.lock() {
        guard.clear();
    }
    // A pre-set env var is trusted only if its backing socket still exists. A
    // stale value (left over from a previous shell after the a11y daemon got
    // restarted) would otherwise lock us into a dead address.
    if let Ok(existing) = env::var("AT_SPI_BUS_ADDRESS") {
        if address_socket_alive(&existing) {
            log_attempt(format!("{existing}: using (env)"));
            return;
        }
        log_attempt(format!("{existing}: stale (env), rediscovering"));
        // SAFETY: single-threaded at startup, before async executor work starts.
        unsafe { env::remove_var("AT_SPI_BUS_ADDRESS") };
    }
    if let Some(addr) = discover_bus_address() {
        // SAFETY: same single-threaded startup window.
        unsafe { env::set_var("AT_SPI_BUS_ADDRESS", addr) };
    }
}

/// Address that was passed (or discovered) for the accessibility bus, useful
/// for debugging output. Returns `None` if neither env nor discovery yielded
/// an address — atspi will then try the session-bus query path.
pub fn current_bus_address() -> Option<String> {
    env::var("AT_SPI_BUS_ADDRESS").ok()
}

/// Extract the filesystem path from a `unix:path=...` style bus address.
/// Returns `None` for abstract sockets or non-unix transports.
pub(crate) fn unix_path_from_address(addr: &str) -> Option<&str> {
    addr.split(',')
        .find_map(|part| part.strip_prefix("unix:path="))
}

/// Parse the numeric part of an X11 DISPLAY string (e.g. `":99.0"` → `"99"`).
/// Defaults to `"0"` when the value is missing or unparsable.
pub(crate) fn display_number(display: Option<&str>) -> String {
    display
        .map(|d| d.trim_start_matches(':').split('.').next().unwrap_or("0"))
        .filter(|d| !d.is_empty())
        .map(str::to_string)
        .unwrap_or_else(|| "0".to_string())
}

/// Build the ordered list of socket paths to probe when `/proc` discovery
/// turns up nothing. Kept pure so unit tests can validate the order without
/// touching the real environment.
pub(crate) fn candidate_paths(
    display_id: &str,
    xdg_runtime: Option<&str>,
    home: Option<&str>,
) -> Vec<PathBuf> {
    let leaf = format!("at-spi/bus_{display_id}");
    let mut paths: Vec<PathBuf> = Vec::new();
    if let Some(runtime) = xdg_runtime {
        paths.push(PathBuf::from(runtime).join(&leaf));
        paths.push(PathBuf::from(runtime).join("at-spi/bus"));
    }
    if let Some(home) = home {
        paths.push(PathBuf::from(home).join(".cache").join(&leaf));
    }
    paths.push(PathBuf::from("/data/.cache").join(&leaf));
    paths.push(PathBuf::from("/root/.cache").join(&leaf));
    paths.push(PathBuf::from("/tmp").join(&leaf));
    paths
}

/// True when an AT-SPI bus address actually accepts a Unix socket connection.
///
/// `Path::exists()` alone is not enough: a dbus-daemon can crash leaving a
/// stale socket file (then connect fails with `ECONNREFUSED`), or the address
/// in `/proc` cmdline may have been the daemon's *requested* listen path which
/// it never bound to (then connect fails with `ENOENT`). We catch both by
/// trying to open the socket here, before handing the address to zbus.
fn address_socket_alive(addr: &str) -> bool {
    use std::os::unix::net::UnixStream;
    // Abstract sockets are not visible on the filesystem; trust them and let
    // zbus surface any error.
    let path = match unix_path_from_address(addr) {
        Some(p) => p,
        None => return true,
    };
    if !Path::new(path).exists() {
        log_attempt(format!("{addr}: path missing"));
        return false;
    }
    match UnixStream::connect(path) {
        Ok(_) => true,
        Err(err) => {
            log_attempt(format!("{addr}: connect failed: {err}"));
            false
        }
    }
}

fn discover_bus_address() -> Option<String> {
    // /proc scan first: this picks up the actual `--address=` the a11y dbus
    // daemon is listening on, which is the authoritative answer regardless of
    // how cache paths look. We still verify the socket exists, because a
    // recently-killed daemon's PID entry can linger in /proc.
    for addr in scan_proc_for_bus_addresses() {
        if address_socket_alive(&addr) {
            log_attempt(format!("{addr}: using (from /proc)"));
            return Some(addr);
        }
    }

    let display_id = display_number(env::var("DISPLAY").ok().as_deref());
    let xdg_runtime = env::var("XDG_RUNTIME_DIR").ok();
    let home = env::var("HOME").ok();
    let candidates = candidate_paths(&display_id, xdg_runtime.as_deref(), home.as_deref());

    for path in candidates {
        if path.exists() {
            let addr = format!("unix:path={}", path.display());
            log_attempt(format!("{addr}: using (cache path)"));
            return Some(addr);
        } else {
            log_attempt(format!("unix:path={}: missing", path.display()));
        }
    }
    None
}

/// Walk `/proc/*/cmdline` and pull `--address=...` from every running
/// dbus-daemon pointed at an `at-spi*/accessibility.conf`. Results are sorted
/// by PID descending (most recent first) so callers prefer the latest
/// restart.
fn scan_proc_for_bus_addresses() -> Vec<String> {
    let proc = Path::new("/proc");
    let entries = match fs::read_dir(proc) {
        Ok(e) => e,
        Err(_) => return Vec::new(),
    };
    let mut found: Vec<(u64, String)> = Vec::new();
    for entry in entries.flatten() {
        let name = entry.file_name();
        let name = name.to_string_lossy();
        let pid: u64 = match name.parse() {
            Ok(n) => n,
            Err(_) => continue,
        };
        let cmdline_path = entry.path().join("cmdline");
        let bytes = match fs::read(&cmdline_path) {
            Ok(b) => b,
            Err(_) => continue,
        };
        let argv: Vec<String> = bytes
            .split(|b| *b == 0)
            .filter(|s| !s.is_empty())
            .map(|s| String::from_utf8_lossy(s).into_owned())
            .collect();
        let is_at_spi = argv
            .iter()
            .any(|a| a.contains("at-spi") && a.contains("accessibility.conf"));
        if !is_at_spi {
            continue;
        }
        for arg in &argv {
            if let Some(rest) = arg.strip_prefix("--address=") {
                log_attempt(format!("pid {pid}: --address={rest}"));
                found.push((pid, rest.to_string()));
            }
        }
    }
    found.sort_by(|a, b| b.0.cmp(&a.0));
    found.into_iter().map(|(_, addr)| addr).collect()
}

fn owned_endpoints(object: &ObjectRefOwned) -> Result<(BusName<'static>, ObjectPath<'static>)> {
    let name = object
        .name()
        .context("AT-SPI object reference is missing a bus name (null ref)")?;
    let path = object.path();
    let name =
        UniqueName::try_from(name.as_str().to_string()).context("invalid AT-SPI bus name")?;
    let path =
        ObjectPath::try_from(path.as_str().to_string()).context("invalid AT-SPI object path")?;
    Ok((BusName::from(name), path))
}

/// Build an [`AccessibleProxy`] tied to the connection's lifetime (not the
/// caller-provided `ObjectRefOwned`).
pub async fn accessible_for<'a>(
    conn: &'a AccessibilityConnection,
    object: &ObjectRefOwned,
) -> Result<AccessibleProxy<'a>> {
    let (destination, path) = owned_endpoints(object)?;
    AccessibleProxy::builder(conn.connection())
        .destination(destination)?
        .path(path)?
        .build()
        .await
        .context("failed to build accessible proxy")
}

fn proxy_endpoints(proxy: &AccessibleProxy<'_>) -> (BusName<'static>, ObjectPath<'static>) {
    (
        proxy.inner().destination().to_owned(),
        proxy.inner().path().to_owned(),
    )
}

/// Build a `ComponentProxy` for an object, used to read geometry.
pub async fn component_for<'a>(
    conn: &'a AccessibilityConnection,
    accessible: &AccessibleProxy<'_>,
) -> Result<ComponentProxy<'a>> {
    let (destination, path) = proxy_endpoints(accessible);
    ComponentProxy::builder(conn.connection())
        .destination(destination)?
        .path(path)?
        .build()
        .await
        .context("failed to build component proxy")
}

/// Build an `ActionProxy` for an object that exposes the Action interface.
pub async fn action_for<'a>(
    conn: &'a AccessibilityConnection,
    accessible: &AccessibleProxy<'_>,
) -> Result<ActionProxy<'a>> {
    let (destination, path) = proxy_endpoints(accessible);
    ActionProxy::builder(conn.connection())
        .destination(destination)?
        .path(path)?
        .build()
        .await
        .context("failed to build action proxy")
}

/// Build an `EditableTextProxy` for an object that exposes EditableText.
pub async fn editable_for<'a>(
    conn: &'a AccessibilityConnection,
    accessible: &AccessibleProxy<'_>,
) -> Result<EditableTextProxy<'a>> {
    let (destination, path) = proxy_endpoints(accessible);
    EditableTextProxy::builder(conn.connection())
        .destination(destination)?
        .path(path)?
        .build()
        .await
        .context("failed to build editable text proxy")
}

/// Build an `ApplicationProxy` for an application root node.
pub async fn application_for<'a>(
    conn: &'a AccessibilityConnection,
    accessible: &AccessibleProxy<'_>,
) -> Result<ApplicationProxy<'a>> {
    let (destination, path) = proxy_endpoints(accessible);
    ApplicationProxy::builder(conn.connection())
        .destination(destination)?
        .path(path)?
        .build()
        .await
        .context("failed to build application proxy")
}

/// Build a `ValueProxy` for an object that exposes the Value interface
/// (sliders, spin buttons, progress bars).
pub async fn value_for<'a>(
    conn: &'a AccessibilityConnection,
    accessible: &AccessibleProxy<'_>,
) -> Result<ValueProxy<'a>> {
    let (destination, path) = proxy_endpoints(accessible);
    ValueProxy::builder(conn.connection())
        .destination(destination)?
        .path(path)?
        .build()
        .await
        .context("failed to build value proxy")
}

/// Build a `TextProxy` for an object that exposes the Text interface.
pub async fn text_for<'a>(
    conn: &'a AccessibilityConnection,
    accessible: &AccessibleProxy<'_>,
) -> Result<TextProxy<'a>> {
    let (destination, path) = proxy_endpoints(accessible);
    TextProxy::builder(conn.connection())
        .destination(destination)?
        .path(path)?
        .build()
        .await
        .context("failed to build text proxy")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn unix_path_extracts_plain_address() {
        assert_eq!(
            unix_path_from_address("unix:path=/tmp/at-spi/bus_99"),
            Some("/tmp/at-spi/bus_99")
        );
    }

    #[test]
    fn unix_path_handles_keyed_address() {
        // dbus addresses may carry extra key=value pairs separated by commas.
        assert_eq!(
            unix_path_from_address("unix:path=/run/foo,guid=abcd"),
            Some("/run/foo")
        );
        assert_eq!(
            unix_path_from_address("guid=abcd,unix:path=/run/foo"),
            Some("/run/foo")
        );
    }

    #[test]
    fn unix_path_returns_none_for_abstract_or_tcp() {
        assert_eq!(unix_path_from_address("unix:abstract=/tmp/foo"), None);
        assert_eq!(unix_path_from_address("tcp:host=127.0.0.1,port=4242"), None);
    }

    #[test]
    fn display_number_strips_colon_and_screen() {
        assert_eq!(display_number(Some(":99")), "99");
        assert_eq!(display_number(Some(":99.0")), "99");
        assert_eq!(display_number(Some(":0")), "0");
    }

    #[test]
    fn display_number_falls_back_to_zero() {
        assert_eq!(display_number(None), "0");
        assert_eq!(display_number(Some("")), "0");
    }

    #[test]
    fn candidate_paths_orders_runtime_then_home_then_globals() {
        let paths = candidate_paths("99", Some("/run/user/1000"), Some("/home/me"));
        let as_strs: Vec<String> = paths.iter().map(|p| p.display().to_string()).collect();
        assert_eq!(
            as_strs,
            vec![
                "/run/user/1000/at-spi/bus_99",
                "/run/user/1000/at-spi/bus",
                "/home/me/.cache/at-spi/bus_99",
                "/data/.cache/at-spi/bus_99",
                "/root/.cache/at-spi/bus_99",
                "/tmp/at-spi/bus_99",
            ]
        );
    }

    #[test]
    fn candidate_paths_skips_unset_runtime_and_home() {
        let paths = candidate_paths("7", None, None);
        let as_strs: Vec<String> = paths.iter().map(|p| p.display().to_string()).collect();
        assert_eq!(
            as_strs,
            vec![
                "/data/.cache/at-spi/bus_7",
                "/root/.cache/at-spi/bus_7",
                "/tmp/at-spi/bus_7",
            ]
        );
    }
}
