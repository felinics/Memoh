//! `a11y-cli snapshot` subcommand — walk the desktop (or one application's)
//! accessibility tree iteratively, assign `eN` refs to visible nodes, persist
//! the index under a fresh snapshot id, and emit both human-readable lines and
//! structured JSON with diagnostics so the Go caller can tell whether an empty
//! list means "no UI" or "everything filtered out".

use anyhow::Result;
use atspi::object_ref::ObjectRefOwned;
use atspi::proxy::accessible::AccessibleProxy;
use atspi::{AccessibilityConnection, CoordType, Role, State, StateSet};
use serde::Serialize;

use crate::apps;
use crate::connection;
use crate::refs::{self, RefEntry};

/// Maximum number of applications we descend into before giving up. The
/// registry sits in front of every connected accessibility application; a
/// reasonable upper bound keeps the walk responsive on chatty desktops.
const MAX_APPS: usize = 32;
/// Maximum nodes inspected across the entire walk. AT-SPI trees can balloon
/// (LibreOffice Calc exposes ~2^31 cells), so we cap aggressively.
const MAX_VISITS: usize = 8000;

#[derive(Serialize)]
struct SnapshotItem {
    #[serde(rename = "ref")]
    ref_id: String,
    role: String,
    name: String,
    x: i32,
    y: i32,
    width: i32,
    height: i32,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    states: Vec<String>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    actions: Vec<String>,
    #[serde(skip_serializing_if = "is_zero")]
    app_pid: u32,
}

fn is_zero(value: &u32) -> bool {
    *value == 0
}

#[derive(Serialize, Default)]
struct Diagnostics {
    apps: usize,
    visited: usize,
    accepted: usize,
    skipped_state: usize,
    skipped_role: usize,
    skipped_geometry: usize,
    errors: usize,
    #[serde(skip_serializing_if = "Option::is_none")]
    bus_address: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    display: Option<String>,
}

/// The application a snapshot was scoped to.
#[derive(Serialize)]
struct SnapshotApp {
    app_id: String,
    pid: u32,
    name: String,
}

#[derive(Serialize)]
struct SnapshotOutput {
    ok: bool,
    /// Contract version; see `crate::PROTOCOL_VERSION`.
    protocol_version: u32,
    helper_version: &'static str,
    /// Identifier of this observation; refs are only valid together with it.
    snapshot_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    app: Option<SnapshotApp>,
    /// The `--limit` that was in effect, echoed so the caller can tell a
    /// short list from a capped one.
    limit: usize,
    truncated: bool,
    items: Vec<SnapshotItem>,
    lines: Vec<String>,
    refs_path: String,
    diagnostics: Diagnostics,
}

pub async fn run(limit: usize, app: Option<&str>) -> Result<()> {
    let conn = connection::open().await?;
    let all_apps = apps::list(&conn).await?;
    let (scope, roots): (
        Option<SnapshotApp>,
        Vec<&(apps::AppInfo, AccessibleProxy<'_>)>,
    ) = match app {
        Some(selector) => {
            let chosen = apps::select(&all_apps, selector)?;
            (
                Some(SnapshotApp {
                    app_id: chosen.0.app_id.clone(),
                    pid: chosen.0.pid,
                    name: chosen.0.name.clone(),
                }),
                vec![chosen],
            )
        }
        None => (None, all_apps.iter().collect()),
    };
    let (entries, truncated, diagnostics) = collect(&conn, &roots, limit).await?;
    let snapshot_id = refs::new_snapshot_id();
    let app_pid = scope.as_ref().map(|s| s.pid).unwrap_or(0);
    let refs_path = refs::write(&snapshot_id, app_pid, &entries)?;

    let lines: Vec<String> = entries.iter().map(format_line).collect();
    let items: Vec<SnapshotItem> = entries
        .iter()
        .map(|entry| SnapshotItem {
            ref_id: entry.ref_id.clone(),
            role: entry.role.clone(),
            name: entry.name.clone(),
            x: entry.x,
            y: entry.y,
            width: entry.width,
            height: entry.height,
            states: entry.states.clone(),
            actions: entry.actions.clone(),
            app_pid: entry.app_pid,
        })
        .collect();

    let out = SnapshotOutput {
        ok: true,
        protocol_version: crate::PROTOCOL_VERSION,
        helper_version: env!("CARGO_PKG_VERSION"),
        snapshot_id,
        app: scope,
        limit,
        truncated,
        items,
        lines,
        refs_path: refs_path.display().to_string(),
        diagnostics,
    };
    println!("{}", serde_json::to_string(&out)?);
    Ok(())
}

fn format_line(entry: &RefEntry) -> String {
    let mut line = format!("- {}", entry.role);
    let name = entry.name.trim();
    if !name.is_empty() {
        line.push(' ');
        line.push_str(&json_quote(name));
    }
    line.push_str(&format!(" [ref={}]", entry.ref_id));
    if entry.has_geometry() {
        line.push_str(&format!(
            " @{},{} {}x{}",
            entry.x, entry.y, entry.width, entry.height
        ));
    }
    if !entry.states.is_empty() {
        line.push_str(&format!(" ({})", entry.states.join(", ")));
    }
    if !entry.actions.is_empty() {
        line.push_str(&format!(" actions={}", entry.actions.join(",")));
    }
    line
}

fn json_quote(value: &str) -> String {
    serde_json::to_string(value).unwrap_or_else(|_| format!("\"{value}\""))
}

/// States worth surfacing to the model: they change what an action can do
/// (editable, focused) or describe a toggle's current value (checked,
/// selected, expanded). Everything else stays out to keep lines short.
fn interesting_states(states: &StateSet) -> Vec<String> {
    let mut out = Vec::new();
    if states.contains(State::Focused) {
        out.push("focused".to_string());
    }
    if states.contains(State::Editable) {
        out.push("editable".to_string());
    }
    if states.contains(State::Checked) {
        out.push("checked".to_string());
    }
    if states.contains(State::Selected) {
        out.push("selected".to_string());
    }
    if states.contains(State::Expanded) {
        out.push("expanded".to_string());
    }
    if states.contains(State::Pressed) {
        out.push("pressed".to_string());
    }
    if !states.contains(State::Sensitive) || !states.contains(State::Enabled) {
        out.push("disabled".to_string());
    }
    out
}

async fn collect(
    conn: &AccessibilityConnection,
    roots: &[&(apps::AppInfo, AccessibleProxy<'_>)],
    limit: usize,
) -> Result<(Vec<RefEntry>, bool, Diagnostics)> {
    let mut entries: Vec<RefEntry> = Vec::new();
    let mut diag = Diagnostics {
        apps: roots.len(),
        bus_address: connection::current_bus_address(),
        display: std::env::var("DISPLAY").ok(),
        ..Diagnostics::default()
    };
    let mut truncated = false;

    for (info, app_proxy) in roots.iter().take(MAX_APPS) {
        if entries.len() >= limit {
            truncated = true;
            break;
        }

        // Iterative depth-first walk inside this application.
        let mut stack: Vec<AccessibleProxy<'_>> = vec![(*app_proxy).clone()];
        while let Some(node) = stack.pop() {
            if entries.len() >= limit {
                truncated = true;
                break;
            }
            if diag.visited >= MAX_VISITS {
                truncated = true;
                break;
            }
            diag.visited += 1;

            match describe(conn, &node, entries.len() + 1, info.pid).await {
                Outcome::Keep(entry) => {
                    diag.accepted += 1;
                    entries.push(entry);
                }
                Outcome::SkipState => diag.skipped_state += 1,
                Outcome::SkipRole => diag.skipped_role += 1,
                Outcome::SkipGeometry => diag.skipped_geometry += 1,
                Outcome::Error => diag.errors += 1,
            }

            // Expand children even when the node itself was filtered, so
            // descendants still get a chance to surface.
            let child_objs = match node.get_children().await {
                Ok(values) => values,
                Err(_) => {
                    diag.errors += 1;
                    continue;
                }
            };
            for child_obj in child_objs.into_iter().rev() {
                if let Ok(child) = into_accessible(conn, child_obj).await {
                    stack.push(child);
                }
            }
        }
    }
    Ok((entries, truncated, diag))
}

async fn into_accessible<'a>(
    conn: &'a AccessibilityConnection,
    object: ObjectRefOwned,
) -> Result<AccessibleProxy<'a>> {
    connection::accessible_for(conn, &object).await
}

enum Outcome {
    Keep(RefEntry),
    SkipState,
    SkipRole,
    SkipGeometry,
    Error,
}

async fn describe(
    conn: &AccessibilityConnection,
    node: &AccessibleProxy<'_>,
    next_index: usize,
    app_pid: u32,
) -> Outcome {
    let states = match node.get_state().await {
        Ok(s) => s,
        Err(_) => return Outcome::Error,
    };
    if !is_on_screen(&states) {
        return Outcome::SkipState;
    }
    let role = match node.get_role().await {
        Ok(r) => r,
        Err(_) => return Outcome::Error,
    };
    if role_is_structural(role) {
        return Outcome::SkipRole;
    }

    let role_name = node
        .get_role_name()
        .await
        .unwrap_or_else(|_| format!("{role:?}").to_lowercase());
    let name = node.name().await.unwrap_or_default();

    // Geometry is best-effort: nodes with missing or zero extents (popups,
    // virtual children, lazily-laid-out widgets) are still useful to surface
    // when they expose a name. The Go side only needs geometry for the RFB
    // fallback path, which gracefully degrades to the AT-SPI Action route.
    let (x, y, width, height) = match connection::component_for(conn, node).await {
        Ok(component) => component
            .get_extents(CoordType::Screen)
            .await
            .unwrap_or((0, 0, 0, 0)),
        Err(_) => (0, 0, 0, 0),
    };

    // If both geometry and name are empty, the node carries no information the
    // model can act on. Skip it to keep the snapshot focused.
    if width <= 0 && height <= 0 && name.trim().is_empty() {
        return Outcome::SkipGeometry;
    }

    let actions = action_names(conn, node).await;

    let inner = node.inner();
    Outcome::Keep(RefEntry {
        ref_id: format!("e{next_index}"),
        bus_name: inner.destination().to_string(),
        object_path: inner.path().to_string(),
        role: role_name,
        name,
        x,
        y,
        width,
        height,
        states: interesting_states(&states),
        actions,
        app_pid,
    })
}

/// Names of the AT-SPI actions a node exposes, empty when it has none or the
/// interface is missing. Best effort: a failure here must not drop the node.
async fn action_names(conn: &AccessibilityConnection, node: &AccessibleProxy<'_>) -> Vec<String> {
    let Ok(actions) = connection::action_for(conn, node).await else {
        return Vec::new();
    };
    let Ok(descriptors) = actions.get_actions().await else {
        return Vec::new();
    };
    descriptors
        .into_iter()
        .map(|a| a.name.trim().to_string())
        .filter(|n| !n.is_empty())
        .collect()
}

fn is_on_screen(states: &StateSet) -> bool {
    // AT-SPI exposes both Visible (will be drawn when its parent is) and
    // Showing (is actually on screen right now). Chromium tends to set only
    // Showing on many of its accessible nodes, while GTK apps set both — we
    // accept either to avoid false negatives.
    states.contains(State::Showing) || states.contains(State::Visible)
}

fn role_is_structural(role: Role) -> bool {
    // Blacklist: things that are pure structural noise, never actionable, and
    // whose subtrees are still walked (we filter the node itself, not its
    // descendants).
    matches!(
        role,
        Role::Filler
            | Role::Separator
            | Role::Invalid
            | Role::Unknown
            | Role::DesktopFrame
            | Role::Application
            | Role::DesktopIcon
    )
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(role: &str, name: &str, x: i32, y: i32, w: i32, h: i32) -> RefEntry {
        RefEntry {
            ref_id: "e3".to_string(),
            bus_name: ":1.42".to_string(),
            object_path: "/org/a11y/atspi/accessible/3".to_string(),
            role: role.to_string(),
            name: name.to_string(),
            x,
            y,
            width: w,
            height: h,
            states: Vec::new(),
            actions: Vec::new(),
            app_pid: 0,
        }
    }

    #[test]
    fn format_line_appends_states_and_actions_when_present() {
        let mut e = entry("text", "Search", 10, 20, 200, 24);
        e.states = vec!["focused".to_string(), "editable".to_string()];
        e.actions = vec!["click".to_string(), "menu".to_string()];
        let line = format_line(&e);
        assert_eq!(
            line,
            "- text \"Search\" [ref=e3] @10,20 200x24 (focused, editable) actions=click,menu"
        );
    }

    #[test]
    fn interesting_states_reports_disabled_when_insensitive() {
        let mut set = StateSet::empty();
        set.insert(State::Showing);
        set.insert(State::Visible);
        assert_eq!(interesting_states(&set), vec!["disabled".to_string()]);
        set.insert(State::Sensitive);
        set.insert(State::Enabled);
        set.insert(State::Editable);
        set.insert(State::Focused);
        assert_eq!(
            interesting_states(&set),
            vec!["focused".to_string(), "editable".to_string()]
        );
    }

    #[test]
    fn format_line_emits_role_name_ref_and_geometry() {
        let line = format_line(&entry("push button", "Reload", 120, 80, 28, 28));
        assert_eq!(line, "- push button \"Reload\" [ref=e3] @120,80 28x28");
    }

    #[test]
    fn format_line_omits_name_when_blank() {
        let line = format_line(&entry("scroll bar", "   ", 10, 20, 4, 100));
        assert_eq!(line, "- scroll bar [ref=e3] @10,20 4x100");
    }

    #[test]
    fn format_line_omits_geometry_when_unknown() {
        let line = format_line(&entry("link", "Help", 0, 0, 0, 0));
        assert_eq!(line, "- link \"Help\" [ref=e3]");
    }

    #[test]
    fn format_line_escapes_quotes_in_name() {
        let line = format_line(&entry("button", "Say \"hi\"", 1, 2, 3, 4));
        // serde_json escapes inner quotes, keeping the outer quoting valid.
        assert_eq!(line, "- button \"Say \\\"hi\\\"\" [ref=e3] @1,2 3x4");
    }

    #[test]
    fn json_quote_escapes_special_chars() {
        assert_eq!(json_quote("hello"), "\"hello\"");
        assert_eq!(json_quote("with \"quote\""), "\"with \\\"quote\\\"\"");
        assert_eq!(json_quote("line\nbreak"), "\"line\\nbreak\"");
    }

    #[test]
    fn structural_roles_are_filtered() {
        assert!(role_is_structural(Role::Filler));
        assert!(role_is_structural(Role::Separator));
        assert!(role_is_structural(Role::Application));
        assert!(role_is_structural(Role::DesktopFrame));
    }

    #[test]
    fn actionable_roles_pass_through() {
        assert!(!role_is_structural(Role::Button));
        assert!(!role_is_structural(Role::Link));
        assert!(!role_is_structural(Role::Entry));
        assert!(!role_is_structural(Role::CheckBox));
    }
}
