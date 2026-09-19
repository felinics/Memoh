//! Persistent ref index for the desktop accessibility tree. A `snapshot`
//! invocation writes the latest mapping to `/tmp/a11y-cli-refs.json` together
//! with the snapshot id it belongs to; later ref-based invocations read the
//! same file to resolve `eN` back into a `(bus_name, object_path)` pair and
//! refuse refs that were taken in a different snapshot.

use std::path::{Path, PathBuf};
use std::time::{SystemTime, UNIX_EPOCH};

use anyhow::{Context, Result};
use atspi::object_ref::{ObjectRef, ObjectRefOwned};
use atspi::zbus::names::UniqueName;
use atspi::zbus::zvariant::ObjectPath;
use serde::{Deserialize, Serialize};

const DEFAULT_REFS_PATH: &str = "/tmp/a11y-cli-refs.json";

/// Largest coordinate a pointer event can address. RFB carries pointer
/// positions as 16-bit values, and AT-SPI reports extents near `i32::MIN` for
/// nodes that have never been laid out, so anything outside this range is not
/// a real screen position.
pub const MAX_POINTER_COORD: i32 = 32767;

/// One row in the persisted refs file.
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct RefEntry {
    pub ref_id: String,
    pub bus_name: String,
    pub object_path: String,
    pub role: String,
    pub name: String,
    pub x: i32,
    pub y: i32,
    pub width: i32,
    pub height: i32,
    /// Interesting AT-SPI states at snapshot time (`focused`, `editable`,
    /// `checked`, ...). Older index files predate the field, so it defaults
    /// to empty instead of failing to parse.
    #[serde(default)]
    pub states: Vec<String>,
    /// Names of the AT-SPI actions the element exposed at snapshot time.
    #[serde(default)]
    pub actions: Vec<String>,
    /// Process id of the application that owns the element (0 if unknown).
    #[serde(default)]
    pub app_pid: u32,
}

impl RefEntry {
    /// Whether the entry carries a usable on-screen box. Entries without one
    /// (popups, virtual children, unrealised tree cells that report absurd
    /// extents) can still be driven through AT-SPI actions but must never be
    /// turned into pointer coordinates.
    pub fn has_geometry(&self) -> bool {
        if self.width <= 0 || self.height <= 0 {
            return false;
        }
        let (cx, cy) = self.center();
        (0..=MAX_POINTER_COORD).contains(&cx) && (0..=MAX_POINTER_COORD).contains(&cy)
    }

    /// Bounding box center, used as the RFB fallback target.
    pub fn center(&self) -> (i32, i32) {
        let cx = self.x.saturating_add(self.width / 2);
        let cy = self.y.saturating_add(self.height / 2);
        (cx, cy)
    }

    /// Rebuild an `ObjectRefOwned` so we can construct proxies again.
    pub fn to_object_ref(&self) -> Result<ObjectRefOwned> {
        let name = UniqueName::try_from(self.bus_name.clone())
            .with_context(|| format!("invalid bus name {:?}", self.bus_name))?;
        let path = ObjectPath::try_from(self.object_path.clone())
            .with_context(|| format!("invalid object path {:?}", self.object_path))?;
        Ok(ObjectRef::new_owned(name, path))
    }
}

#[derive(Debug, Serialize, Deserialize, Default)]
pub struct RefIndex {
    /// Identifier of the snapshot these entries were taken in. Empty for
    /// index files written by helpers that predate snapshot binding.
    #[serde(default)]
    pub snapshot_id: String,
    /// Process id the snapshot was scoped to, 0 for the whole desktop.
    #[serde(default)]
    pub app_pid: u32,
    pub entries: Vec<RefEntry>,
}

fn refs_path() -> PathBuf {
    std::env::var("A11Y_CLI_REFS")
        .map(PathBuf::from)
        .unwrap_or_else(|_| PathBuf::from(DEFAULT_REFS_PATH))
}

/// Mint a snapshot id that is unique per invocation on this machine: wall
/// clock nanoseconds mixed with the helper's pid, rendered as `s<hex>`.
pub fn new_snapshot_id() -> String {
    let nanos = SystemTime::now()
        .duration_since(UNIX_EPOCH)
        .map(|d| d.as_nanos() as u64)
        .unwrap_or(0);
    let pid = u64::from(std::process::id());
    format!("s{:x}", nanos.rotate_left(17) ^ (pid << 40) ^ pid)
}

pub fn write(snapshot_id: &str, app_pid: u32, entries: &[RefEntry]) -> Result<PathBuf> {
    let target = refs_path();
    write_to(&target, snapshot_id, app_pid, entries)?;
    Ok(target)
}

fn write_to(path: &Path, snapshot_id: &str, app_pid: u32, entries: &[RefEntry]) -> Result<()> {
    let index = RefIndex {
        snapshot_id: snapshot_id.to_string(),
        app_pid,
        entries: entries.to_vec(),
    };
    let data = serde_json::to_vec_pretty(&index).context("serialize refs index")?;
    let parent = path.parent();
    if let Some(parent) = parent {
        if !parent.as_os_str().is_empty() {
            std::fs::create_dir_all(parent)
                .with_context(|| format!("create parent directory for {}", path.display()))?;
        }
    }
    std::fs::write(path, data).with_context(|| format!("write refs index to {}", path.display()))
}

fn read_index() -> Result<(PathBuf, RefIndex)> {
    let target = refs_path();
    let data = std::fs::read(&target)
        .with_context(|| format!("read refs index at {}", target.display()))?;
    let index: RefIndex = serde_json::from_slice(&data).context("parse refs index")?;
    Ok((target, index))
}

/// Resolve a ref. When `expected_snapshot` is given the index must have been
/// written by that exact snapshot; a ref from an older observation is refused
/// instead of being mapped onto whatever element now carries the same number.
pub fn lookup(ref_id: &str, expected_snapshot: Option<&str>) -> Result<RefEntry> {
    let (target, index) = read_index()?;
    if let Some(expected) = expected_snapshot.map(str::trim).filter(|s| !s.is_empty()) {
        if index.snapshot_id != expected {
            let current = if index.snapshot_id.is_empty() {
                "an index without a snapshot id".to_string()
            } else {
                format!("snapshot {}", index.snapshot_id)
            };
            anyhow::bail!(
                "ref {ref_id} was taken in snapshot {expected}, but the current index is {current}; observe again before acting"
            );
        }
    }
    let normalized = normalize(ref_id);
    for entry in index.entries {
        if entry.ref_id == normalized {
            return Ok(entry);
        }
    }
    anyhow::bail!(
        "ref {ref_id} is not present in {} (run `a11y-cli snapshot` first)",
        target.display()
    )
}

/// Normalize ref ids like `e3`, `E03`, or `ref=e3` to the canonical `eN` form.
pub fn normalize(ref_id: &str) -> String {
    let trimmed = ref_id.trim();
    let lower = trimmed.to_ascii_lowercase();
    let without_prefix = lower.strip_prefix("ref=").unwrap_or(&lower);
    let without_e = without_prefix.strip_prefix('e').unwrap_or(without_prefix);
    match without_e.parse::<u32>() {
        Ok(idx) if idx > 0 => format!("e{idx}"),
        _ => trimmed.to_string(),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicU64, Ordering};

    static REFS_COUNTER: AtomicU64 = AtomicU64::new(0);

    /// Return a unique refs path in the OS temp dir so concurrent tests do
    /// not stomp on each other's `A11Y_CLI_REFS` env value.
    fn unique_refs_path() -> PathBuf {
        let pid = std::process::id();
        let n = REFS_COUNTER.fetch_add(1, Ordering::SeqCst);
        std::env::temp_dir().join(format!("a11y-cli-refs-test-{pid}-{n}.json"))
    }

    fn sample_entry() -> RefEntry {
        RefEntry {
            ref_id: "e1".to_string(),
            bus_name: ":1.42".to_string(),
            object_path: "/org/a11y/atspi/accessible/root".to_string(),
            role: "push button".to_string(),
            name: "Reload".to_string(),
            x: 100,
            y: 50,
            width: 30,
            height: 20,
            states: Vec::new(),
            actions: Vec::new(),
            app_pid: 0,
        }
    }

    #[test]
    fn has_geometry_requires_positive_box() {
        assert!(sample_entry().has_geometry());
        let flat = RefEntry {
            width: 0,
            height: 12,
            ..sample_entry()
        };
        assert!(!flat.has_geometry());
    }

    #[test]
    fn has_geometry_rejects_unrealised_extents() {
        // GTK tree cells that were never laid out report extents around
        // i32::MIN; those must never become pointer targets.
        let bogus = RefEntry {
            x: -2147483648 + 200,
            y: -2147483648 + 10,
            width: 528,
            height: 38,
            ..sample_entry()
        };
        assert!(!bogus.has_geometry());
        let offscreen = RefEntry {
            x: -1000,
            y: -1000,
            width: 5,
            height: 5,
            ..sample_entry()
        };
        assert!(!offscreen.has_geometry());
        let huge = RefEntry {
            x: 40000,
            y: 10,
            width: 20,
            height: 20,
            ..sample_entry()
        };
        assert!(!huge.has_geometry());
    }

    #[test]
    fn legacy_index_without_states_still_parses() {
        let legacy = r#"{"entries":[{"ref_id":"e1","bus_name":":1.9","object_path":"/a","role":"entry","name":"Search","x":1,"y":2,"width":3,"height":4}]}"#;
        let index: RefIndex = serde_json::from_str(legacy).expect("legacy index parses");
        assert_eq!(index.entries.len(), 1);
        assert!(index.entries[0].states.is_empty());
        assert!(index.entries[0].actions.is_empty());
        assert!(index.snapshot_id.is_empty());
    }

    #[test]
    fn snapshot_ids_are_unique_and_prefixed() {
        let a = new_snapshot_id();
        let b = new_snapshot_id();
        assert!(a.starts_with('s') && b.starts_with('s'));
        assert_ne!(a, b);
    }

    #[test]
    fn normalize_accepts_canonical_form() {
        assert_eq!(normalize("e3"), "e3");
    }

    #[test]
    fn normalize_uppercases_and_strips_padding() {
        assert_eq!(normalize("  E3  "), "e3");
        assert_eq!(normalize("E03"), "e3");
    }

    #[test]
    fn normalize_handles_bare_numbers() {
        assert_eq!(normalize("3"), "e3");
        assert_eq!(normalize("007"), "e7");
    }

    #[test]
    fn normalize_strips_ref_prefix() {
        assert_eq!(normalize("ref=e3"), "e3");
        assert_eq!(normalize("REF=E03"), "e3");
        assert_eq!(normalize("ref=7"), "e7");
    }

    #[test]
    fn normalize_falls_back_for_invalid_inputs() {
        assert_eq!(normalize("e0"), "e0");
        assert_eq!(normalize("abc"), "abc");
        assert_eq!(normalize(""), "");
    }

    #[test]
    fn center_returns_midpoint_of_bounding_box() {
        let entry = RefEntry {
            x: 100,
            y: 50,
            width: 40,
            height: 20,
            ..sample_entry()
        };
        assert_eq!(entry.center(), (120, 60));
    }

    #[test]
    fn center_handles_zero_dimensions() {
        let entry = RefEntry {
            x: 10,
            y: 20,
            width: 0,
            height: 0,
            ..sample_entry()
        };
        assert_eq!(entry.center(), (10, 20));
    }

    #[test]
    fn write_and_lookup_roundtrip_binds_to_snapshot() {
        let path = unique_refs_path();
        // SAFETY: tests are single-threaded with regard to this env var
        // because each test invocation uses a unique path.
        unsafe { std::env::set_var("A11Y_CLI_REFS", &path) };
        let entries = vec![
            RefEntry {
                ref_id: "e1".to_string(),
                ..sample_entry()
            },
            RefEntry {
                ref_id: "e2".to_string(),
                name: "Stop".to_string(),
                ..sample_entry()
            },
        ];
        let written = write("s123", 0, &entries).expect("write should succeed");
        assert_eq!(written, path);

        let entry = lookup("e2", None).expect("lookup should find e2");
        assert_eq!(entry.ref_id, "e2");
        assert_eq!(entry.name, "Stop");

        let entry = lookup("REF=E1", Some("s123")).expect("matching snapshot resolves");
        assert_eq!(entry.ref_id, "e1");

        let err = lookup("e1", Some("s999")).expect_err("foreign snapshot must be refused");
        assert!(err.to_string().contains("observe again"), "{err}");

        assert!(lookup("e99", None).is_err(), "missing refs should error");

        let _ = std::fs::remove_file(&path);
        unsafe { std::env::remove_var("A11Y_CLI_REFS") };
    }
}
