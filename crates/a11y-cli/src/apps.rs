//! `a11y-cli apps` — enumerate the applications registered on the
//! accessibility bus. Each registry child is one application; its process id
//! is resolved through the bus daemon (`GetConnectionUnixProcessID`), which is
//! the only stable identity a running GUI application has from the outside.

use anyhow::{Context, Result};
use atspi::proxy::accessible::AccessibleProxy;
use atspi::zbus::fdo::DBusProxy;
use atspi::zbus::names::BusName;
use atspi::{AccessibilityConnection, CoordType, State};
use serde::Serialize;

use crate::connection;

/// One top-level window of an application.
#[derive(Serialize, Clone, Debug)]
pub struct AppWindow {
    pub name: String,
    pub role: String,
    pub x: i32,
    pub y: i32,
    pub width: i32,
    pub height: i32,
    pub active: bool,
}

/// One application on the accessibility bus.
#[derive(Serialize, Clone, Debug)]
pub struct AppInfo {
    /// Stable instance id used by the Go side: `app:<pid>`.
    pub app_id: String,
    pub pid: u32,
    pub bus_name: String,
    pub name: String,
    pub toolkit: String,
    pub version: String,
    pub windows: Vec<AppWindow>,
}

#[derive(Serialize)]
struct AppsOutput {
    ok: bool,
    protocol_version: u32,
    helper_version: &'static str,
    apps: Vec<AppInfo>,
    #[serde(skip_serializing_if = "Option::is_none")]
    bus_address: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    display: Option<String>,
}

pub async fn run() -> Result<()> {
    let conn = connection::open().await?;
    let apps = list(&conn).await?;
    let out = AppsOutput {
        ok: true,
        protocol_version: crate::PROTOCOL_VERSION,
        helper_version: env!("CARGO_PKG_VERSION"),
        apps: apps.into_iter().map(|(info, _)| info).collect(),
        bus_address: connection::current_bus_address(),
        display: std::env::var("DISPLAY").ok(),
    };
    println!("{}", serde_json::to_string(&out)?);
    Ok(())
}

/// Enumerate applications together with their root accessible proxy.
pub async fn list(conn: &AccessibilityConnection) -> Result<Vec<(AppInfo, AccessibleProxy<'_>)>> {
    let registry = conn.root_accessible_on_registry().await?;
    let children = registry
        .get_children()
        .await
        .context("list registry children")?;
    let dbus = DBusProxy::new(conn.connection()).await.ok();
    let mut out = Vec::new();
    for object in children {
        let proxy = match connection::accessible_for(conn, &object).await {
            Ok(proxy) => proxy,
            Err(_) => continue,
        };
        let bus_name = proxy.inner().destination().to_string();
        let pid = match &dbus {
            Some(dbus) => match BusName::try_from(bus_name.clone()) {
                Ok(name) => dbus.get_connection_unix_process_id(name).await.unwrap_or(0),
                Err(_) => 0,
            },
            None => 0,
        };
        let name = proxy.name().await.unwrap_or_default();
        let (toolkit, version) = match connection::application_for(conn, &proxy).await {
            Ok(app) => (
                app.toolkit_name().await.unwrap_or_default(),
                app.version().await.unwrap_or_default(),
            ),
            Err(_) => (String::new(), String::new()),
        };
        let windows = windows_of(conn, &proxy).await;
        out.push((
            AppInfo {
                app_id: app_id_for(pid, &bus_name),
                pid,
                bus_name,
                name,
                toolkit,
                version,
                windows,
            },
            proxy,
        ));
    }
    Ok(out)
}

/// Stable id for an application: the pid when the bus daemon could resolve
/// it, otherwise the bus name (unique for the connection's lifetime).
pub fn app_id_for(pid: u32, bus_name: &str) -> String {
    if pid > 0 {
        format!("app:{pid}")
    } else {
        format!("bus:{bus_name}")
    }
}

async fn windows_of(conn: &AccessibilityConnection, app: &AccessibleProxy<'_>) -> Vec<AppWindow> {
    let mut out = Vec::new();
    let children = match app.get_children().await {
        Ok(children) => children,
        Err(_) => return out,
    };
    for child in children {
        let node = match connection::accessible_for(conn, &child).await {
            Ok(node) => node,
            Err(_) => continue,
        };
        let role = node
            .get_role_name()
            .await
            .unwrap_or_else(|_| "window".to_string());
        let name = node.name().await.unwrap_or_default();
        let states = node.get_state().await.ok();
        let active = states
            .as_ref()
            .map(|s| s.contains(State::Active))
            .unwrap_or(false);
        let showing = states
            .as_ref()
            .map(|s| s.contains(State::Showing) || s.contains(State::Visible))
            .unwrap_or(true);
        if !showing {
            continue;
        }
        let (x, y, width, height) = match connection::component_for(conn, &node).await {
            Ok(component) => component
                .get_extents(CoordType::Screen)
                .await
                .unwrap_or((0, 0, 0, 0)),
            Err(_) => (0, 0, 0, 0),
        };
        out.push(AppWindow {
            name,
            role,
            x,
            y,
            width,
            height,
            active,
        });
    }
    out
}

/// Match an application selector (`app:<pid>`, bare pid, bus name, or a
/// case-insensitive name) against the listed applications. Name matches must
/// be unique; ambiguity is an error that lists the candidates.
pub fn select<'a, T>(apps: &'a [(AppInfo, T)], selector: &str) -> Result<&'a (AppInfo, T)> {
    let wanted = selector.trim();
    if wanted.is_empty() {
        anyhow::bail!("application selector is empty");
    }
    let pid = wanted
        .strip_prefix("app:")
        .unwrap_or(wanted)
        .parse::<u32>()
        .ok();
    if let Some(pid) = pid {
        return apps
            .iter()
            .find(|(info, _)| info.pid == pid)
            .with_context(|| format!("no accessible application with pid {pid}"));
    }
    if wanted.starts_with(':') || wanted.starts_with("bus:") {
        let bus = wanted.strip_prefix("bus:").unwrap_or(wanted);
        return apps
            .iter()
            .find(|(info, _)| info.bus_name == bus)
            .with_context(|| format!("no accessible application on bus name {bus}"));
    }
    let lowered = wanted.to_lowercase();
    let matches: Vec<&(AppInfo, T)> = apps
        .iter()
        .filter(|(info, _)| info.name.to_lowercase() == lowered)
        .collect();
    match matches.len() {
        0 => anyhow::bail!("no accessible application named {wanted:?}"),
        1 => Ok(matches[0]),
        n => {
            let ids: Vec<String> = matches
                .iter()
                .map(|(info, _)| info.app_id.clone())
                .collect();
            anyhow::bail!(
                "{n} applications are named {wanted:?}: {}; pass one of their ids",
                ids.join(", ")
            )
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn info(pid: u32, bus: &str, name: &str) -> (AppInfo, ()) {
        (
            AppInfo {
                app_id: app_id_for(pid, bus),
                pid,
                bus_name: bus.to_string(),
                name: name.to_string(),
                toolkit: "gtk".to_string(),
                version: String::new(),
                windows: Vec::new(),
            },
            (),
        )
    }

    #[test]
    fn app_id_prefers_pid_and_falls_back_to_bus_name() {
        assert_eq!(app_id_for(42, ":1.9"), "app:42");
        assert_eq!(app_id_for(0, ":1.9"), "bus::1.9");
    }

    #[test]
    fn select_matches_pid_bus_and_unique_name() {
        let apps = vec![
            info(10, ":1.1", "xfce4-appfinder"),
            info(11, ":1.2", "Terminal"),
            info(12, ":1.3", "Terminal"),
        ];
        assert_eq!(select(&apps, "app:10").unwrap().0.pid, 10);
        assert_eq!(select(&apps, "11").unwrap().0.pid, 11);
        assert_eq!(select(&apps, ":1.3").unwrap().0.pid, 12);
        assert_eq!(select(&apps, "XFCE4-APPFINDER").unwrap().0.pid, 10);
    }

    #[test]
    fn select_reports_ambiguous_names_with_candidates() {
        let apps = vec![info(11, ":1.2", "Terminal"), info(12, ":1.3", "Terminal")];
        let err = select(&apps, "terminal").unwrap_err().to_string();
        assert!(err.contains("2 applications"), "{err}");
        assert!(err.contains("app:11") && err.contains("app:12"), "{err}");
        assert!(select(&apps, "nope").is_err());
        assert!(select(&apps, "app:99").is_err());
    }
}
