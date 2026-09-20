//! `a11y-cli` is a small AT-SPI2 helper invoked by the Memoh workspace
//! Computer Use tools. It probes the accessibility bus, lists applications,
//! walks the desktop (or one application's) accessibility tree to produce a
//! flat ref list (`e1..eN`) bound to a snapshot id, and performs ref-based
//! actions on the focused desktop. When an action cannot be performed through
//! AT-SPI, the binary returns a `fallback` coordinate that the Go caller will
//! replay over RFB.

use anyhow::Result;
use clap::{Parser, Subcommand};

mod action;
mod apps;
mod connection;
mod probe;
mod refs;
mod snapshot;

/// Version of the JSON contract shared with the Go caller
/// (`internal/agent/tool/computer_a11y.go`). Bump it whenever a field the Go
/// side relies on changes shape; the Go side refuses output from a different
/// version instead of silently misreading it.
pub const PROTOCOL_VERSION: u32 = 3;

/// Top-level CLI definition.
#[derive(Parser)]
#[command(name = "a11y-cli", about = "Memoh workspace AT-SPI2 helper", version)]
struct Cli {
    #[command(subcommand)]
    command: Command,
}

#[derive(Subcommand)]
enum Command {
    /// Connect to the accessibility bus and report status as JSON.
    Probe,
    /// List the applications on the accessibility bus with their process id,
    /// toolkit, and top-level windows.
    Apps,
    /// Walk the desktop (or one application's) accessibility tree and emit a
    /// flat snapshot bound to a fresh snapshot id.
    Snapshot {
        /// Hard cap on the number of interactive nodes returned.
        #[arg(long, default_value_t = 300)]
        limit: usize,
        /// Restrict the walk to one application: `app:<pid>`, a bare pid, a
        /// bus name such as `:1.42`, or an application name.
        #[arg(long)]
        app: Option<String>,
    },
    /// Resolve a ref from the persisted snapshot index to its geometry
    /// without touching the accessibility bus or re-numbering anything.
    Locate {
        #[arg(long)]
        r#ref: String,
        /// Snapshot id the ref was taken in; mismatches are refused.
        #[arg(long)]
        snapshot: Option<String>,
    },
    /// Invoke the default action (typically "click") on a ref.
    Click {
        #[arg(long)]
        r#ref: String,
        #[arg(long)]
        snapshot: Option<String>,
    },
    /// Insert text at the caret of the editable element backing a ref.
    Type {
        #[arg(long)]
        r#ref: String,
        #[arg(long)]
        text: String,
        #[arg(long)]
        snapshot: Option<String>,
    },
    /// Replace the editable contents of a ref.
    Fill {
        #[arg(long)]
        r#ref: String,
        #[arg(long)]
        text: String,
        #[arg(long)]
        snapshot: Option<String>,
    },
    /// Set the value of a ref directly: editable text contents, or the
    /// numeric value of a slider / spin button.
    SetValue {
        #[arg(long)]
        r#ref: String,
        #[arg(long)]
        value: String,
        #[arg(long)]
        snapshot: Option<String>,
    },
    /// Select text inside a ref, or place the caret before/after it.
    SelectText {
        #[arg(long)]
        r#ref: String,
        #[arg(long)]
        text: String,
        #[arg(long, default_value = "")]
        prefix: String,
        #[arg(long, default_value = "")]
        suffix: String,
        /// `text`, `cursor_before`, or `cursor_after`.
        #[arg(long, default_value = "text")]
        mode: String,
        #[arg(long)]
        snapshot: Option<String>,
    },
    /// Invoke a named AT-SPI action exposed by a ref.
    Action {
        #[arg(long)]
        r#ref: String,
        #[arg(long)]
        name: String,
        #[arg(long)]
        snapshot: Option<String>,
    },
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    futures_lite::future::block_on(async move {
        match cli.command {
            Command::Probe => probe::run().await,
            Command::Apps => apps::run().await,
            Command::Snapshot { limit, app } => snapshot::run(limit, app.as_deref()).await,
            Command::Locate { r#ref, snapshot } => action::locate(&r#ref, snapshot.as_deref()),
            Command::Click { r#ref, snapshot } => action::click(&r#ref, snapshot.as_deref()).await,
            Command::Type {
                r#ref,
                text,
                snapshot,
            } => action::type_text(&r#ref, &text, snapshot.as_deref()).await,
            Command::Fill {
                r#ref,
                text,
                snapshot,
            } => action::fill_text(&r#ref, &text, snapshot.as_deref()).await,
            Command::SetValue {
                r#ref,
                value,
                snapshot,
            } => action::set_value(&r#ref, &value, snapshot.as_deref()).await,
            Command::SelectText {
                r#ref,
                text,
                prefix,
                suffix,
                mode,
                snapshot,
            } => {
                action::select_text(&r#ref, &text, &prefix, &suffix, &mode, snapshot.as_deref())
                    .await
            }
            Command::Action {
                r#ref,
                name,
                snapshot,
            } => action::named_action(&r#ref, &name, snapshot.as_deref()).await,
        }
    })
}
