//! Implements the ref-based actions (`click`, `type`, `fill`, `set-value`,
//! `select-text`, `action`, `locate`) in terms of AT-SPI. Whenever an AT-SPI
//! invocation fails we still emit `fallback: { x, y }` (when the element has
//! a real on-screen box) so the Go caller can replay the action via RFB
//! pointer/key events.

use anyhow::Result;
use serde::Serialize;

use crate::connection;
use crate::refs::{self, RefEntry};

#[derive(Serialize)]
struct ActionResult {
    ok: bool,
    protocol_version: u32,
    action: &'static str,
    #[serde(rename = "ref")]
    ref_id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    detail: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    error: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    fallback: Option<Point>,
    /// Set when the failure is a capability gap rather than a transient
    /// error, so the caller can report "unsupported" instead of retrying.
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    unsupported: bool,
    #[serde(skip_serializing_if = "Option::is_none")]
    selection: Option<Selection>,
}

#[derive(Serialize, Clone, Copy, PartialEq, Eq, Debug)]
struct Point {
    x: i32,
    y: i32,
}

/// Outcome of `select-text`: character offsets inside the element's text.
#[derive(Serialize, Clone, Debug, PartialEq, Eq)]
pub struct Selection {
    pub start: i32,
    pub end: i32,
    pub mode: String,
}

/// Pointer fallback for an entry: only meaningful when the entry has a real
/// on-screen box. Without one, the previous behaviour clicked at the
/// saturated centre of a zero box (typically 0,0) — the wrong element.
fn fallback_point(entry: &RefEntry) -> Option<Point> {
    if !entry.has_geometry() {
        return None;
    }
    let (x, y) = entry.center();
    Some(Point { x, y })
}

impl ActionResult {
    fn success(action: &'static str, entry: &RefEntry, detail: impl Into<String>) -> Self {
        Self {
            ok: true,
            protocol_version: crate::PROTOCOL_VERSION,
            action,
            ref_id: entry.ref_id.clone(),
            detail: Some(detail.into()),
            error: None,
            fallback: None,
            unsupported: false,
            selection: None,
        }
    }

    fn failure(action: &'static str, entry: &RefEntry, error: impl Into<String>) -> Self {
        Self {
            ok: false,
            protocol_version: crate::PROTOCOL_VERSION,
            action,
            ref_id: entry.ref_id.clone(),
            detail: None,
            error: Some(error.into()),
            fallback: fallback_point(entry),
            unsupported: false,
            selection: None,
        }
    }

    /// A capability gap: the element does not expose what the action needs.
    /// No pointer fallback is offered because replaying raw input would not
    /// give the action its meaning either.
    fn unsupported(action: &'static str, entry: &RefEntry, error: impl Into<String>) -> Self {
        Self {
            ok: false,
            protocol_version: crate::PROTOCOL_VERSION,
            action,
            ref_id: entry.ref_id.clone(),
            detail: None,
            error: Some(error.into()),
            fallback: None,
            unsupported: true,
            selection: None,
        }
    }

    fn emit(&self) -> Result<()> {
        println!("{}", serde_json::to_string(self)?);
        Ok(())
    }
}

/// `a11y-cli locate --ref eN`: resolve a ref from the persisted index to its
/// role, name, box and centre. It never touches the accessibility bus and
/// never re-numbers refs, so the Go caller can turn a ref into pointer
/// coordinates (double-click, right-click, scroll) without the re-scan that
/// used to move refs onto different elements.
#[derive(Serialize)]
struct LocateResult {
    ok: bool,
    protocol_version: u32,
    action: &'static str,
    #[serde(rename = "ref")]
    ref_id: String,
    role: String,
    name: String,
    x: i32,
    y: i32,
    width: i32,
    height: i32,
    #[serde(skip_serializing_if = "Option::is_none")]
    center: Option<Point>,
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

pub fn locate(ref_id: &str, snapshot: Option<&str>) -> Result<()> {
    let entry = refs::lookup(ref_id, snapshot)?;
    let out = LocateResult {
        ok: true,
        protocol_version: crate::PROTOCOL_VERSION,
        action: "locate",
        ref_id: entry.ref_id.clone(),
        role: entry.role.clone(),
        name: entry.name.clone(),
        x: entry.x,
        y: entry.y,
        width: entry.width,
        height: entry.height,
        center: fallback_point(&entry),
        states: entry.states.clone(),
        actions: entry.actions.clone(),
        app_pid: entry.app_pid,
    };
    println!("{}", serde_json::to_string(&out)?);
    Ok(())
}

pub async fn click(ref_id: &str, snapshot: Option<&str>) -> Result<()> {
    let entry = refs::lookup(ref_id, snapshot)?;
    let outcome = try_click(&entry).await;
    match outcome {
        Ok(detail) => ActionResult::success("click", &entry, detail).emit(),
        Err(err) => ActionResult::failure("click", &entry, format!("{err:#}")).emit(),
    }
}

async fn try_click(entry: &RefEntry) -> Result<String> {
    let conn = connection::open().await?;
    let object = entry.to_object_ref()?;
    let accessible = connection::accessible_for(&conn, &object).await?;
    let actions = connection::action_for(&conn, &accessible).await?;

    let descriptors = actions.get_actions().await?;
    if descriptors.is_empty() {
        anyhow::bail!("the target element does not expose any AT-SPI actions");
    }
    let preferred = preferred_action_index(&descriptors);
    let success = actions.do_action(preferred as i32).await?;
    if !success {
        anyhow::bail!("AT-SPI reported the action did not run");
    }
    let label = descriptors
        .get(preferred)
        .map(|action| action.name.to_string())
        .unwrap_or_else(|| "click".to_string());
    Ok(label)
}

fn preferred_action_index(descriptors: &[atspi::Action]) -> usize {
    for (idx, action) in descriptors.iter().enumerate() {
        let lower = action.name.to_ascii_lowercase();
        if lower.contains("click") || lower.contains("press") || lower.contains("activate") {
            return idx;
        }
    }
    0
}

/// `a11y-cli action --ref eN --name <action>`: run one of the actions the
/// element itself advertises. The name must match an advertised action; the
/// helper never substitutes a click for an unknown name.
pub async fn named_action(ref_id: &str, name: &str, snapshot: Option<&str>) -> Result<()> {
    let entry = refs::lookup(ref_id, snapshot)?;
    match try_named_action(&entry, name).await {
        Ok(detail) => ActionResult::success("action", &entry, detail).emit(),
        Err(NamedActionError::NotExposed(msg)) => {
            ActionResult::unsupported("action", &entry, msg).emit()
        }
        Err(NamedActionError::Failed(err)) => {
            ActionResult::failure("action", &entry, format!("{err:#}")).emit()
        }
    }
}

enum NamedActionError {
    NotExposed(String),
    Failed(anyhow::Error),
}

impl From<anyhow::Error> for NamedActionError {
    fn from(err: anyhow::Error) -> Self {
        NamedActionError::Failed(err)
    }
}

async fn try_named_action(entry: &RefEntry, name: &str) -> Result<String, NamedActionError> {
    let conn = connection::open().await?;
    let object = entry.to_object_ref()?;
    let accessible = connection::accessible_for(&conn, &object).await?;
    let actions = match connection::action_for(&conn, &accessible).await {
        Ok(actions) => actions,
        Err(_) => {
            return Err(NamedActionError::NotExposed(
                "the target element does not expose the AT-SPI Action interface".to_string(),
            ))
        }
    };
    let descriptors = actions
        .get_actions()
        .await
        .map_err(|e| NamedActionError::Failed(e.into()))?;
    let Some(index) = find_action_index(&descriptors, name) else {
        let available: Vec<String> = descriptors.iter().map(|a| a.name.clone()).collect();
        return Err(NamedActionError::NotExposed(if available.is_empty() {
            format!("the target element exposes no actions, so {name:?} cannot run")
        } else {
            format!(
                "the target element does not expose an action named {name:?}; available: {}",
                available.join(", ")
            )
        }));
    };
    let success = actions
        .do_action(index as i32)
        .await
        .map_err(|e| NamedActionError::Failed(e.into()))?;
    if !success {
        return Err(NamedActionError::Failed(anyhow::anyhow!(
            "AT-SPI reported the action did not run"
        )));
    }
    Ok(descriptors[index].name.clone())
}

fn find_action_index(descriptors: &[atspi::Action], name: &str) -> Option<usize> {
    let wanted = name.trim().to_ascii_lowercase();
    if wanted.is_empty() {
        return None;
    }
    descriptors
        .iter()
        .position(|a| a.name.trim().to_ascii_lowercase() == wanted)
}

/// The `length` argument of AT-SPI `EditableText.InsertText` is interpreted as
/// the number of **UTF-8 bytes** by the toolkits we drive (GTK/ATK documents it
/// as "length ... in bytes", and Chromium copies `length` bytes out of the
/// string). Passing the Unicode scalar count instead truncates multi-byte text
/// such as CJK — e.g. "你好" is 2 chars but 6 bytes, so a length of 2 inserts a
/// broken prefix. Always derive the length from the UTF-8 byte length.
fn insert_text_length(text: &str) -> i32 {
    i32::try_from(text.len()).unwrap_or(i32::MAX)
}

pub async fn type_text(ref_id: &str, text: &str, snapshot: Option<&str>) -> Result<()> {
    let entry = refs::lookup(ref_id, snapshot)?;
    let outcome = try_type(&entry, text).await;
    match outcome {
        Ok(_) => ActionResult::success(
            "type",
            &entry,
            format!("inserted {} chars", text.chars().count()),
        )
        .emit(),
        Err(err) => ActionResult::failure("type", &entry, format!("{err:#}")).emit(),
    }
}

async fn try_type(entry: &RefEntry, text: &str) -> Result<()> {
    let conn = connection::open().await?;
    let object = entry.to_object_ref()?;
    let accessible = connection::accessible_for(&conn, &object).await?;
    let editable = connection::editable_for(&conn, &accessible).await?;
    let text_proxy = connection::text_for(&conn, &accessible).await?;
    let caret = text_proxy.caret_offset().await.unwrap_or(-1);
    let position = if caret < 0 { 0 } else { caret };
    let length = insert_text_length(text);
    let inserted = editable.insert_text(position, text, length).await?;
    if !inserted {
        anyhow::bail!("editable text widget refused to insert");
    }
    Ok(())
}

pub async fn fill_text(ref_id: &str, text: &str, snapshot: Option<&str>) -> Result<()> {
    let entry = refs::lookup(ref_id, snapshot)?;
    let outcome = try_fill(&entry, text).await;
    match outcome {
        Ok(_) => ActionResult::success(
            "fill",
            &entry,
            format!("set {} chars", text.chars().count()),
        )
        .emit(),
        Err(err) => ActionResult::failure("fill", &entry, format!("{err:#}")).emit(),
    }
}

async fn try_fill(entry: &RefEntry, text: &str) -> Result<()> {
    let conn = connection::open().await?;
    let object = entry.to_object_ref()?;
    let accessible = connection::accessible_for(&conn, &object).await?;
    let editable = connection::editable_for(&conn, &accessible).await?;
    let replaced = editable.set_text_contents(text).await?;
    if !replaced {
        anyhow::bail!("editable text widget refused to replace contents");
    }
    Ok(())
}

/// `a11y-cli set-value --ref eN --value V`: set an editable element's text
/// contents, or a Value-interface control's number. Elements that expose
/// neither report an unsupported capability instead of a pointer fallback.
pub async fn set_value(ref_id: &str, value: &str, snapshot: Option<&str>) -> Result<()> {
    let entry = refs::lookup(ref_id, snapshot)?;
    match try_set_value(&entry, value).await {
        Ok(detail) => ActionResult::success("set_value", &entry, detail).emit(),
        Err(SetValueError::Unsupported(msg)) => {
            ActionResult::unsupported("set_value", &entry, msg).emit()
        }
        Err(SetValueError::Failed(err)) => {
            ActionResult::failure("set_value", &entry, format!("{err:#}")).emit()
        }
    }
}

enum SetValueError {
    Unsupported(String),
    Failed(anyhow::Error),
}

impl From<anyhow::Error> for SetValueError {
    fn from(err: anyhow::Error) -> Self {
        SetValueError::Failed(err)
    }
}

async fn try_set_value(entry: &RefEntry, value: &str) -> Result<String, SetValueError> {
    let conn = connection::open().await?;
    let object = entry.to_object_ref()?;
    let accessible = connection::accessible_for(&conn, &object).await?;
    let interfaces = accessible
        .get_interfaces()
        .await
        .map_err(|e| SetValueError::Failed(e.into()))?;
    if interfaces.contains(atspi::Interface::EditableText) {
        let editable = connection::editable_for(&conn, &accessible).await?;
        let replaced = editable
            .set_text_contents(value)
            .await
            .map_err(|e| SetValueError::Failed(e.into()))?;
        if !replaced {
            return Err(SetValueError::Failed(anyhow::anyhow!(
                "editable text widget refused to replace contents"
            )));
        }
        return Ok(format!("text set to {} chars", value.chars().count()));
    }
    if interfaces.contains(atspi::Interface::Value) {
        let number: f64 = value.trim().parse().map_err(|_| {
            SetValueError::Unsupported(format!(
                "the target element takes a numeric value, but {value:?} is not a number"
            ))
        })?;
        let proxy = connection::value_for(&conn, &accessible).await?;
        let (min, max) = (
            proxy.minimum_value().await.unwrap_or(f64::NEG_INFINITY),
            proxy.maximum_value().await.unwrap_or(f64::INFINITY),
        );
        if number < min || number > max {
            return Err(SetValueError::Unsupported(format!(
                "value {number} is outside the control's range {min}..{max}"
            )));
        }
        proxy
            .set_current_value(number)
            .await
            .map_err(|e| SetValueError::Failed(e.into()))?;
        let now = proxy.current_value().await.unwrap_or(number);
        return Ok(format!("value set to {now}"));
    }
    Err(SetValueError::Unsupported(
        "the target element exposes neither EditableText nor Value, so its value cannot be set directly".to_string(),
    ))
}

/// `a11y-cli select-text`: find `prefix + text + suffix` exactly once in the
/// element's text and either select `text` or park the caret before/after it.
pub async fn select_text(
    ref_id: &str,
    text: &str,
    prefix: &str,
    suffix: &str,
    mode: &str,
    snapshot: Option<&str>,
) -> Result<()> {
    let entry = refs::lookup(ref_id, snapshot)?;
    match try_select_text(&entry, text, prefix, suffix, mode).await {
        Ok(selection) => {
            let mut result = ActionResult::success(
                "select_text",
                &entry,
                format!(
                    "{} chars {}-{} ({})",
                    selection.end - selection.start,
                    selection.start,
                    selection.end,
                    selection.mode
                ),
            );
            result.selection = Some(selection);
            result.emit()
        }
        Err(SelectError::Unsupported(msg)) => {
            ActionResult::unsupported("select_text", &entry, msg).emit()
        }
        Err(SelectError::Failed(err)) => {
            ActionResult::failure("select_text", &entry, format!("{err:#}")).emit()
        }
    }
}

enum SelectError {
    Unsupported(String),
    Failed(anyhow::Error),
}

impl From<anyhow::Error> for SelectError {
    fn from(err: anyhow::Error) -> Self {
        SelectError::Failed(err)
    }
}

/// Locate `prefix + text + suffix` in `haystack` and return the character
/// offsets of `text`. Exactly one match is required; zero or several are
/// errors the model has to disambiguate with a longer prefix/suffix.
pub fn find_unique_span(
    haystack: &str,
    text: &str,
    prefix: &str,
    suffix: &str,
) -> Result<(i32, i32), String> {
    if text.is_empty() {
        return Err("text to select is empty".to_string());
    }
    let hay: Vec<char> = haystack.chars().collect();
    let needle: Vec<char> = format!("{prefix}{text}{suffix}").chars().collect();
    if needle.len() > hay.len() {
        return Err(format!(
            "{:?} was not found in the element text",
            format!("{prefix}{text}{suffix}")
        ));
    }
    let mut matches = Vec::new();
    for start in 0..=(hay.len() - needle.len()) {
        if hay[start..start + needle.len()] == needle[..] {
            matches.push(start);
        }
    }
    match matches.len() {
        0 => Err(format!(
            "{:?} was not found in the element text",
            format!("{prefix}{text}{suffix}")
        )),
        1 => {
            let start = matches[0] + prefix.chars().count();
            let end = start + text.chars().count();
            Ok((start as i32, end as i32))
        }
        n => Err(format!(
            "{text:?} matches {n} times; add prefix or suffix to make the match unique"
        )),
    }
}

async fn try_select_text(
    entry: &RefEntry,
    text: &str,
    prefix: &str,
    suffix: &str,
    mode: &str,
) -> Result<Selection, SelectError> {
    let mode = mode.trim().to_ascii_lowercase();
    if !matches!(mode.as_str(), "text" | "cursor_before" | "cursor_after") {
        return Err(SelectError::Unsupported(format!(
            "selection_type {mode:?} is not one of text, cursor_before, cursor_after"
        )));
    }
    let conn = connection::open().await?;
    let object = entry.to_object_ref()?;
    let accessible = connection::accessible_for(&conn, &object).await?;
    let interfaces = accessible
        .get_interfaces()
        .await
        .map_err(|e| SelectError::Failed(e.into()))?;
    if !interfaces.contains(atspi::Interface::Text) {
        return Err(SelectError::Unsupported(
            "the target element does not expose the AT-SPI Text interface".to_string(),
        ));
    }
    let text_proxy = connection::text_for(&conn, &accessible).await?;
    let count = text_proxy
        .character_count()
        .await
        .map_err(|e| SelectError::Failed(e.into()))?;
    let content = text_proxy
        .get_text(0, count)
        .await
        .map_err(|e| SelectError::Failed(e.into()))?;
    let (start, end) = find_unique_span(&content, text, prefix, suffix)
        .map_err(|msg| SelectError::Failed(anyhow::anyhow!(msg)))?;
    let applied = match mode.as_str() {
        "cursor_before" => text_proxy.set_caret_offset(start).await,
        "cursor_after" => text_proxy.set_caret_offset(end).await,
        _ => {
            let selections = text_proxy.get_n_selections().await.unwrap_or(0);
            if selections > 0 {
                text_proxy.set_selection(0, start, end).await
            } else {
                text_proxy.add_selection(start, end).await
            }
        }
    }
    .map_err(|e| SelectError::Failed(e.into()))?;
    if !applied {
        return Err(SelectError::Failed(anyhow::anyhow!(
            "the text widget refused to change its selection or caret"
        )));
    }
    Ok(Selection { start, end, mode })
}

#[cfg(test)]
mod tests {
    use super::*;

    fn action(name: &str) -> atspi::Action {
        atspi::Action {
            name: name.to_string(),
            description: String::new(),
            keybinding: String::new(),
        }
    }

    fn entry(w: i32, h: i32) -> RefEntry {
        RefEntry {
            ref_id: "e2".to_string(),
            bus_name: ":1.7".to_string(),
            object_path: "/org/a11y/atspi/accessible/2".to_string(),
            role: "push button".to_string(),
            name: "OK".to_string(),
            x: 100,
            y: 40,
            width: w,
            height: h,
            states: Vec::new(),
            actions: Vec::new(),
            app_pid: 0,
        }
    }

    #[test]
    fn fallback_point_uses_center_of_real_box() {
        assert_eq!(
            fallback_point(&entry(50, 20)),
            Some(Point { x: 125, y: 50 })
        );
    }

    #[test]
    fn fallback_point_is_absent_without_geometry() {
        assert_eq!(fallback_point(&entry(0, 0)), None);
        assert_eq!(fallback_point(&entry(30, 0)), None);
    }

    #[test]
    fn failure_result_omits_fallback_without_geometry() {
        let result = ActionResult::failure("click", &entry(0, 0), "no actions");
        let json = serde_json::to_string(&result).expect("serialize");
        assert!(!json.contains("fallback"), "unexpected fallback in {json}");
        assert!(json.contains("\"protocol_version\":3"));
        assert!(!json.contains("unsupported"));
    }

    #[test]
    fn unsupported_result_flags_capability_gap_without_fallback() {
        let result = ActionResult::unsupported("set_value", &entry(50, 20), "no value");
        let json = serde_json::to_string(&result).expect("serialize");
        assert!(json.contains("\"unsupported\":true"), "{json}");
        assert!(!json.contains("fallback"), "{json}");
    }

    #[test]
    fn preferred_index_picks_click_first() {
        let descriptors = [action("focus"), action("click"), action("press")];
        assert_eq!(preferred_action_index(&descriptors), 1);
    }

    #[test]
    fn preferred_index_matches_press_when_no_click() {
        let descriptors = [action("focus"), action("press"), action("activate")];
        assert_eq!(preferred_action_index(&descriptors), 1);
    }

    #[test]
    fn preferred_index_matches_activate_when_no_click_or_press() {
        let descriptors = [action("focus"), action("activate")];
        assert_eq!(preferred_action_index(&descriptors), 1);
    }

    #[test]
    fn preferred_index_is_case_insensitive() {
        let descriptors = [action("Focus"), action("CLICK")];
        assert_eq!(preferred_action_index(&descriptors), 1);
    }

    #[test]
    fn preferred_index_falls_back_to_zero() {
        let descriptors = [action("focus"), action("describe")];
        assert_eq!(preferred_action_index(&descriptors), 0);
    }

    #[test]
    fn preferred_index_handles_empty_descriptors() {
        let descriptors: [atspi::Action; 0] = [];
        assert_eq!(preferred_action_index(&descriptors), 0);
    }

    #[test]
    fn named_action_index_matches_case_insensitively_or_not_at_all() {
        let descriptors = [action("click"), action("Expand or contract")];
        assert_eq!(
            find_action_index(&descriptors, "expand OR contract"),
            Some(1)
        );
        assert_eq!(find_action_index(&descriptors, "click"), Some(0));
        assert_eq!(find_action_index(&descriptors, "menu"), None);
        assert_eq!(find_action_index(&descriptors, ""), None);
    }

    #[test]
    fn insert_text_length_uses_utf8_byte_count() {
        // ASCII: byte length equals char count.
        assert_eq!(insert_text_length("abc"), 3);
        // CJK: each char is 3 UTF-8 bytes, so the length must be 6, not 2.
        assert_eq!(insert_text_length("你好"), 6);
        // Mixed content keeps byte semantics.
        assert_eq!(insert_text_length("a你b"), 5);
        // Astral plane (emoji) is 4 bytes.
        assert_eq!(insert_text_length("😀"), 4);
        assert_eq!(insert_text_length(""), 0);
    }

    #[test]
    fn find_unique_span_uses_character_offsets() {
        // "你好 world" — offsets are characters, not bytes.
        assert_eq!(find_unique_span("你好 world", "world", "", ""), Ok((3, 8)));
        assert_eq!(find_unique_span("abcabc", "b", "a", "ca"), Ok((1, 2)));
        assert_eq!(find_unique_span("abcabc", "b", "ca", ""), Ok((4, 5)));
    }

    #[test]
    fn find_unique_span_rejects_missing_and_ambiguous_matches() {
        let err = find_unique_span("abcabc", "b", "", "").unwrap_err();
        assert!(err.contains("matches 2 times"), "{err}");
        let err = find_unique_span("abc", "zz", "", "").unwrap_err();
        assert!(err.contains("not found"), "{err}");
        assert!(find_unique_span("abc", "", "", "").is_err());
        assert!(find_unique_span("ab", "abc", "", "").is_err());
    }
}
