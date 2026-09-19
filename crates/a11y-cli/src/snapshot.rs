//! `a11y-cli snapshot` subcommand — walk the desktop, one application, or the
//! subtree below a ref iteratively, assign `eN` refs to visible nodes (reusing
//! the previous ids for elements that are still there), persist the index
//! under a fresh snapshot id, and emit both human-readable lines and
//! structured JSON with diagnostics so the Go caller can tell whether an empty
//! list means "no UI" or "everything filtered out".

use std::collections::HashMap;

use anyhow::Result;
use atspi::object_ref::ObjectRefOwned;
use atspi::proxy::accessible::AccessibleProxy;
use atspi::{AccessibilityConnection, CoordType, Interface, InterfaceSet, Role, State, StateSet};
use serde::Serialize;

use crate::apps;
use crate::connection;
use crate::refs::{self, RefEntry, RefIndex};

/// Maximum number of applications we descend into before giving up. The
/// registry sits in front of every connected accessibility application; a
/// reasonable upper bound keeps the walk responsive on chatty desktops.
const MAX_APPS: usize = 32;
/// Maximum nodes inspected across the entire walk. AT-SPI trees can balloon
/// (LibreOffice Calc exposes ~2^31 cells), so we cap aggressively.
const MAX_VISITS: usize = 8000;
/// Longest value text read from an editable widget.
const MAX_VALUE_CHARS: usize = 200;

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
    depth: u32,
    #[serde(skip_serializing_if = "Option::is_none")]
    value: Option<String>,
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
    /// The ref whose subtree was walked, when `--scope` was given.
    #[serde(skip_serializing_if = "Option::is_none")]
    scope: Option<String>,
    /// The `--limit` that was in effect, echoed so the caller can tell a
    /// short list from a capped one.
    limit: usize,
    truncated: bool,
    /// How many refs kept the id they had in the previous index.
    reused_refs: usize,
    /// How many entries of the previous index were carried over unobserved
    /// because this snapshot was scoped to a subtree (see `RefEntry::carried`).
    carried_refs: usize,
    items: Vec<SnapshotItem>,
    lines: Vec<String>,
    refs_path: String,
    diagnostics: Diagnostics,
}

/// One starting point of the walk: an accessible node and the pid of the
/// application it belongs to.
struct WalkRoot<'a> {
    proxy: AccessibleProxy<'a>,
    app_pid: u32,
}

pub async fn run(limit: usize, app: Option<&str>, scope: Option<&str>, reuse: bool) -> Result<()> {
    let conn = connection::open().await?;
    let all_apps = apps::list(&conn).await?;
    let previous: Option<RefIndex> = refs::load().ok();

    let mut scope_ref: Option<String> = None;
    let (scope_app, roots): (Option<SnapshotApp>, Vec<WalkRoot<'_>>) = match (scope, app) {
        (Some(scope_id), _) => {
            let entry = refs::lookup(scope_id, None)?;
            let object = entry.to_object_ref()?;
            let proxy = connection::accessible_for(&conn, &object).await?;
            scope_ref = Some(entry.ref_id.clone());
            let app_info = all_apps
                .iter()
                .find(|(info, _)| info.pid == entry.app_pid && entry.app_pid > 0)
                .map(|(info, _)| SnapshotApp {
                    app_id: info.app_id.clone(),
                    pid: info.pid,
                    name: info.name.clone(),
                });
            (
                app_info,
                vec![WalkRoot {
                    proxy,
                    app_pid: entry.app_pid,
                }],
            )
        }
        (None, Some(selector)) => {
            let chosen = apps::select(&all_apps, selector)?;
            (
                Some(SnapshotApp {
                    app_id: chosen.0.app_id.clone(),
                    pid: chosen.0.pid,
                    name: chosen.0.name.clone(),
                }),
                vec![WalkRoot {
                    proxy: chosen.1.clone(),
                    app_pid: chosen.0.pid,
                }],
            )
        }
        (None, None) => (
            None,
            all_apps
                .iter()
                .map(|(info, proxy)| WalkRoot {
                    proxy: proxy.clone(),
                    app_pid: info.pid,
                })
                .collect(),
        ),
    };

    let (mut entries, truncated, diagnostics) = collect(&conn, &roots, limit).await?;
    let reused_refs = assign_refs(&mut entries, previous.as_ref().filter(|_| reuse));
    let snapshot_id = refs::new_snapshot_id();
    let app_pid = scope_app.as_ref().map(|s| s.pid).unwrap_or(0);
    // A subtree observation leaves everything outside the subtree
    // unobserved; with ref reuse those entries are carried over so their
    // ids stay reserved for the next whole-target observation.
    let mut index_entries = entries.clone();
    let mut carried_refs = 0;
    if scope.is_some() && reuse {
        if let Some(index) = previous.as_ref() {
            let carried = carry_over(index, &entries);
            carried_refs = carried.len();
            index_entries.extend(carried);
        }
    }
    let refs_path = refs::write(&snapshot_id, app_pid, &index_entries)?;

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
            depth: entry.depth,
            value: entry.value.clone(),
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
        app: scope_app,
        scope: scope_ref,
        limit,
        truncated,
        reused_refs,
        carried_refs,
        items,
        lines,
        refs_path: refs_path.display().to_string(),
        diagnostics,
    };
    println!("{}", serde_json::to_string(&out)?);
    Ok(())
}

/// Give every entry a ref id. With a previous index, elements that are still
/// present keep their old id (matched by bus name + object path) and new
/// elements continue above the previous maximum, so a ref never silently
/// moves onto a different element between two observations. Without one the
/// ids are dense from e1.
fn assign_refs(entries: &mut [RefEntry], previous: Option<&RefIndex>) -> usize {
    let (known, mut next): (HashMap<String, String>, u32) = match previous {
        Some(index) => (index.ref_by_object(), index.max_ref_number() + 1),
        None => (HashMap::new(), 1),
    };
    let mut reused = 0;
    for entry in entries.iter_mut() {
        if let Some(id) = known.get(&entry.object_key()) {
            entry.ref_id = id.clone();
            reused += 1;
        } else {
            entry.ref_id = format!("e{next}");
            next += 1;
        }
    }
    reused
}

/// Entries of the previous index whose object was not observed this time,
/// marked as carried over. Objects that were observed again are represented
/// by their fresh entry only.
fn carry_over(previous: &RefIndex, observed: &[RefEntry]) -> Vec<RefEntry> {
    let present: std::collections::HashSet<String> =
        observed.iter().map(RefEntry::object_key).collect();
    previous
        .entries
        .iter()
        .filter(|entry| !present.contains(&entry.object_key()))
        .map(|entry| RefEntry {
            carried: true,
            ..entry.clone()
        })
        .collect()
}

fn format_line(entry: &RefEntry) -> String {
    let mut line = String::new();
    for _ in 0..entry.depth {
        line.push_str("  ");
    }
    line.push_str("- ");
    line.push_str(&entry.role);
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
    if let Some(value) = &entry.value {
        let shown: String = value.chars().take(80).collect();
        let suffix = if value.chars().count() > 80 {
            "…"
        } else {
            ""
        };
        line.push_str(&format!(
            " value={}",
            json_quote(&format!("{shown}{suffix}"))
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
    roots: &[WalkRoot<'_>],
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

    for root in roots.iter().take(MAX_APPS) {
        if entries.len() >= limit {
            truncated = true;
            break;
        }

        // Iterative depth-first walk inside this root.
        let mut stack: Vec<(AccessibleProxy<'_>, u32)> = vec![(root.proxy.clone(), 0)];
        while let Some((node, depth)) = stack.pop() {
            if entries.len() >= limit {
                truncated = true;
                break;
            }
            if diag.visited >= MAX_VISITS {
                truncated = true;
                break;
            }
            diag.visited += 1;

            match describe(conn, &node, depth, root.app_pid).await {
                Outcome::Keep(entry) => {
                    diag.accepted += 1;
                    entries.push(*entry);
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
                    stack.push((child, depth + 1));
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
    Keep(Box<RefEntry>),
    SkipState,
    SkipRole,
    SkipGeometry,
    Error,
}

async fn describe(
    conn: &AccessibilityConnection,
    node: &AccessibleProxy<'_>,
    depth: u32,
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

    let interfaces = node.get_interfaces().await.ok();
    let actions = action_names(conn, node).await;
    let value = read_value(conn, node, role, &states, interfaces.as_ref()).await;

    let inner = node.inner();
    Outcome::Keep(Box::new(RefEntry {
        ref_id: String::new(),
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
        depth,
        value,
        carried: false,
    }))
}

/// Current value of a control when it has one: numbers for Value-interface
/// widgets (sliders, spin buttons, progress bars), the text of editable text
/// widgets, and a mask for password fields. Best effort; None when the node
/// has no readable value.
async fn read_value(
    conn: &AccessibilityConnection,
    node: &AccessibleProxy<'_>,
    role: Role,
    states: &StateSet,
    interfaces: Option<&InterfaceSet>,
) -> Option<String> {
    if role == Role::PasswordText {
        return Some("•••".to_string());
    }
    let interfaces = interfaces?;
    if interfaces.contains(Interface::Value) {
        let proxy = connection::value_for(conn, node).await.ok()?;
        let current = proxy.current_value().await.ok()?;
        return Some(format_number(current));
    }
    if states.contains(State::Editable) && interfaces.contains(Interface::Text) {
        let proxy = connection::text_for(conn, node).await.ok()?;
        let count = proxy.character_count().await.ok()?;
        let end = count.min(MAX_VALUE_CHARS as i32);
        let text = proxy.get_text(0, end).await.ok()?;
        return Some(text);
    }
    None
}

/// Render an AT-SPI value without a spurious fraction for whole numbers.
fn format_number(value: f64) -> String {
    if value.fract() == 0.0 && value.abs() < 1e15 {
        format!("{}", value as i64)
    } else {
        format!("{value}")
    }
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
            depth: 0,
            value: None,
            carried: false,
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
    fn format_line_indents_by_depth_and_shows_value() {
        let mut e = entry("spin button", "Count", 10, 20, 60, 24);
        e.depth = 2;
        e.value = Some("42".to_string());
        assert_eq!(
            format_line(&e),
            "    - spin button \"Count\" [ref=e3] @10,20 60x24 value=\"42\""
        );
        let mut long = entry("text", "", 0, 0, 10, 10);
        long.value = Some("x".repeat(100));
        let line = format_line(&long);
        assert!(line.ends_with("…\""), "{line}");
    }

    #[test]
    fn assign_refs_reuses_ids_by_object_identity() {
        let previous = RefIndex {
            snapshot_id: "s0".to_string(),
            app_pid: 0,
            entries: vec![
                RefEntry {
                    ref_id: "e4".to_string(),
                    object_path: "/a".to_string(),
                    ..entry("button", "A", 0, 0, 1, 1)
                },
                RefEntry {
                    ref_id: "e9".to_string(),
                    object_path: "/b".to_string(),
                    ..entry("button", "B", 0, 0, 1, 1)
                },
            ],
        };
        let mut fresh = vec![
            RefEntry {
                object_path: "/b".to_string(),
                ..entry("button", "B", 0, 0, 1, 1)
            },
            RefEntry {
                object_path: "/c".to_string(),
                ..entry("button", "C", 0, 0, 1, 1)
            },
            RefEntry {
                object_path: "/a".to_string(),
                ..entry("button", "A", 0, 0, 1, 1)
            },
        ];
        let reused = assign_refs(&mut fresh, Some(&previous));
        assert_eq!(reused, 2);
        assert_eq!(fresh[0].ref_id, "e9");
        assert_eq!(fresh[1].ref_id, "e10");
        assert_eq!(fresh[2].ref_id, "e4");

        let mut dense = vec![entry("a", "", 0, 0, 1, 1), entry("b", "", 0, 0, 1, 1)];
        assert_eq!(assign_refs(&mut dense, None), 0);
        assert_eq!(dense[0].ref_id, "e1");
        assert_eq!(dense[1].ref_id, "e2");
    }

    #[test]
    fn carry_over_reserves_unobserved_refs_for_the_next_walk() {
        let previous = RefIndex {
            snapshot_id: "s0".to_string(),
            app_pid: 0,
            entries: vec![
                RefEntry {
                    ref_id: "e1".to_string(),
                    object_path: "/a".to_string(),
                    ..entry("frame", "App", 0, 0, 100, 100)
                },
                RefEntry {
                    ref_id: "e2".to_string(),
                    object_path: "/b".to_string(),
                    ..entry("text", "", 10, 10, 50, 20)
                },
            ],
        };
        // A subtree walk re-observed only /b.
        let mut observed = vec![RefEntry {
            object_path: "/b".to_string(),
            ..entry("text", "", 10, 10, 50, 20)
        }];
        assert_eq!(assign_refs(&mut observed, Some(&previous)), 1);
        assert_eq!(observed[0].ref_id, "e2");
        let carried = carry_over(&previous, &observed);
        assert_eq!(carried.len(), 1);
        assert_eq!(carried[0].ref_id, "e1");
        assert!(carried[0].carried);

        // The next whole-target walk reuses both ids from the merged index.
        let mut merged = observed.clone();
        merged.extend(carried);
        let index = RefIndex {
            snapshot_id: "s1".to_string(),
            app_pid: 0,
            entries: merged,
        };
        let mut whole = vec![
            RefEntry {
                object_path: "/a".to_string(),
                ..entry("frame", "App", 0, 0, 100, 100)
            },
            RefEntry {
                object_path: "/b".to_string(),
                ..entry("text", "", 10, 10, 50, 20)
            },
            RefEntry {
                object_path: "/c".to_string(),
                ..entry("push button", "OK", 0, 0, 10, 10)
            },
        ];
        assert_eq!(assign_refs(&mut whole, Some(&index)), 2);
        assert_eq!(whole[0].ref_id, "e1");
        assert_eq!(whole[1].ref_id, "e2");
        assert_eq!(whole[2].ref_id, "e3");
        assert!(whole.iter().all(|e| !e.carried));
    }

    #[test]
    fn format_number_drops_zero_fraction() {
        assert_eq!(format_number(42.0), "42");
        assert_eq!(format_number(0.5), "0.5");
        assert_eq!(format_number(-3.0), "-3");
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
