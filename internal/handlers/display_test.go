package handlers

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestResolveDisplayHostIPsStripsPorts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		host string
		want []string
	}{
		{name: "plain ipv4", host: "100.123.2.67", want: []string{"100.123.2.67"}},
		{name: "ipv4 port", host: "100.123.2.67:8082", want: []string{"100.123.2.67"}},
		{name: "bracketed ipv6 port", host: "[::1]:8082", want: []string{"::1"}},
		{name: "plain ipv6", host: "::1", want: []string{"::1"}},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveDisplayHostIPs(context.Background(), tt.host); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("resolveDisplayHostIPs(%q) = %#v, want %#v", tt.host, got, tt.want)
			}
		})
	}
}

func TestFirstHeaderValue(t *testing.T) {
	t.Parallel()

	got := firstHeaderValue("100.123.2.67, 10.0.0.2")
	if got != "100.123.2.67" {
		t.Fatalf("firstHeaderValue returned %q", got)
	}
}

func readDisplayScript(t *testing.T, name string) string {
	t.Helper()
	var scriptPath string
	switch name {
	case "desktop-install.sh":
		scriptPath = "../../scripts/desktop-install.sh"
	case "desktop-style.sh":
		scriptPath = "../../scripts/desktop-style.sh"
	case "display-apply-style.sh":
		scriptPath = "../../scripts/display-apply-style.sh"
	case "display-prepare.sh":
		scriptPath = "../../scripts/display-prepare.sh"
	default:
		t.Fatalf("unsupported display script %q", name)
	}
	data, err := os.ReadFile(scriptPath) //nolint:gosec // scriptPath is selected from fixed repository paths above.
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestDisplayPrepareCommandUsesImageScripts(t *testing.T) {
	t.Parallel()

	if got := displayPrepareCommand(); got != "/bin/sh /opt/memoh/scripts/display-prepare.sh" {
		t.Fatalf("displayPrepareCommand() = %q", got)
	}
	prepareScript := readDisplayScript(t, "display-prepare.sh")
	styleScript := readDisplayScript(t, "desktop-style.sh")
	installScript := readDisplayScript(t, "desktop-install.sh")
	applyScript := readDisplayScript(t, "display-apply-style.sh")
	cmd := strings.Join([]string{prepareScript, styleScript, installScript, applyScript}, "\n")
	if strings.Contains(prepareScript, "cat >/tmp/memoh-") {
		t.Fatal("display prepare must not inject scripts at runtime")
	}
	if strings.Contains(prepareScript, "desktop-install.sh") || strings.Contains(prepareScript, "apt-get install") || strings.Contains(prepareScript, "apk add") {
		t.Fatal("display prepare must not install packages at runtime")
	}
	if !strings.Contains(cmd, "install_debian()") || !strings.Contains(cmd, "install_alpine()") {
		t.Fatal("injected install script must define Debian and Alpine installers")
	}
	if !strings.Contains(cmd, "configure_plank()") || !strings.Contains(cmd, "WhiteSur-Dark") {
		t.Fatal("injected style script must define macOS-like desktop styling")
	}
	if !strings.Contains(cmd, "configure_topbar()") || !strings.Contains(cmd, "windowck-plugin") || !strings.Contains(cmd, "appmenu") {
		t.Fatal("injected style script must define macOS-like topbar styling")
	}
	if !strings.Contains(cmd, "memoh-logo-white") || strings.Contains(cmd, "memoh-apple-symbol") {
		t.Fatal("injected style script must use the white Memoh logo for the topbar menu icon")
	}
	if !strings.Contains(cmd, "memoh_logo_png_base64()") ||
		!strings.Contains(cmd, "memoh-logo-white.png") ||
		!strings.Contains(cmd, "/usr/share/icons/hicolor/48x48/apps") ||
		!strings.Contains(cmd, "base64 -d") {
		t.Fatal("injected style script must install a PNG Memoh logo fallback for panels without SVG icon loading")
	}
	if !strings.Contains(cmd, "xfconf_replace_int_array xfce4-panel /panels 1") || !strings.Contains(cmd, "restart_xfce_panel()") {
		t.Fatal("injected style script must remove the default Xfce bottom panel")
	}
	if !strings.Contains(cmd, "xsetroot -cursor_name left_ptr") || !strings.Contains(cmd, "nohup xfwm4 --replace") {
		t.Fatal("injected style script must restore the Xfce window manager and pointer cursor")
	}
	if !strings.Contains(cmd, "plugin-ids 101 102 103 104 105 106 107 108") ||
		!strings.Contains(cmd, "xfconf_reset xfce4-panel /plugins/plugin-109") ||
		strings.Contains(cmd, "plugin-109 string actions") {
		t.Fatal("injected style script must omit the Xfce actions menu with logout and power options")
	}
	if !strings.Contains(cmd, `write_chromium_wrapper "$browser"`) ||
		!strings.Contains(cmd, `write_desktop_file "$file" "Chromium" "chromium" "$wrapper"`) ||
		!strings.Contains(cmd, `--user-data-dir="\$profile"`) ||
		!strings.Contains(cmd, `rm -f "\$profile"/SingletonLock`) ||
		strings.Contains(cmd, `write_desktop_file "$file" "Browser" "web-browser"`) {
		t.Fatal("injected style script must pin the dock browser launcher to a Chromium wrapper with an isolated profile")
	}
	terminalIndex := strings.Index(cmd, `terminal="$(command -v xfce4-terminal`)
	terminalLauncherIndex := strings.Index(cmd, `write_desktop_file "$file" "Terminal" "utilities-terminal" "$terminal"`)
	xtermFallbackIndex := strings.Index(cmd, `/usr/share/applications/debian-xterm.desktop`)
	if terminalIndex < 0 || terminalLauncherIndex < terminalIndex || xtermFallbackIndex < terminalLauncherIndex ||
		strings.Contains(cmd, `write_desktop_file "$file" "XTerm" "xterm" "$terminal"`) {
		t.Fatal("injected style script must prefer a custom terminal launcher with the themed terminal icon over Debian's mini.xterm desktop file")
	}
	filesIndex := strings.Index(cmd, `files_desktop_file()`)
	filesLauncherIndex := strings.Index(cmd, `write_desktop_file "$file" "Files" "$icon"`)
	thunarFallbackIndex := strings.Index(cmd, `/usr/share/applications/thunar.desktop`)
	if filesIndex < 0 || filesLauncherIndex < filesIndex || thunarFallbackIndex < filesLauncherIndex ||
		!strings.Contains(cmd, "Memoh-WhiteSur-dark") ||
		!strings.Contains(cmd, "global_theme_dir") ||
		!strings.Contains(cmd, "install_file_manager_icon_aliases") ||
		!strings.Contains(cmd, "file_manager_icon_path()") ||
		!strings.Contains(cmd, "folder-blue.svg") ||
		!strings.Contains(cmd, "org.xfce.filemanager") ||
		!strings.Contains(cmd, "org.xfce.thunar") ||
		!strings.Contains(cmd, "inode-directory") ||
		!strings.Contains(cmd, "text-x-generic.svg") ||
		!strings.Contains(cmd, `require_xfconf_value xsettings /Net/IconThemeName Memoh-WhiteSur-dark`) ||
		!strings.Contains(cmd, "restart_file_manager()") ||
		strings.Contains(cmd, "memoh_files_icon_png_base64") ||
		strings.Contains(cmd, "write_memoh_files_icon_png") {
		t.Fatal("injected style script must use a custom Files launcher and a WhiteSur-backed icon theme overlay")
	}
	if !strings.Contains(installScript, "install_style_extras_for_current_os") {
		t.Fatal("image build installer must include styling assets")
	}
	if !strings.Contains(prepareScript, "/opt/memoh/scripts/display-apply-style.sh --check") ||
		!strings.Contains(prepareScript, "/opt/memoh/scripts/display-apply-style.sh --ensure") {
		t.Fatal("display prepare must call the image-provided style helper")
	}
	if !strings.Contains(prepareScript, `desktop_style_current()`) ||
		!strings.Contains(prepareScript, `/bin/sh /opt/memoh/scripts/display-apply-style.sh --check`) {
		t.Fatal("display prepare readiness must include the desktop style marker")
	}
	if strings.Contains(prepareScript, `nohup /bin/sh /opt/memoh/scripts/desktop-style.sh`) {
		t.Fatal("display prepare must not apply desktop style asynchronously")
	}
	styleIndex := strings.Index(prepareScript, `progress 90 styling "Applying desktop style"`)
	browserIndex := strings.Index(prepareScript, `progress 94 browser "Launching browser"`)
	completeIndex := strings.LastIndex(prepareScript, "\nemit_complete\nexit 0")
	if styleIndex < 0 || browserIndex < 0 || styleIndex > browserIndex || browserIndex > completeIndex {
		t.Fatal("display prepare must apply desktop style synchronously before browser launch and completion")
	}
	if !strings.Contains(prepareScript, `/bin/sh /opt/memoh/scripts/display-apply-style.sh --ensure`) {
		t.Fatal("display prepare must ensure the current style version before reporting ready")
	}
	if !strings.Contains(cmd, "SUDO_USER") || !strings.Contains(cmd, "sudo git unzip bash") {
		t.Fatal("display prepare must support WhiteSur's installer in non-login root containers")
	}
	if !strings.Contains(cmd, "MEMOH_DISPLAY_INSTALL_ASSETS_GLOBAL") ||
		!strings.Contains(cmd, "/usr/local/share/themes") ||
		!strings.Contains(cmd, "/usr/local/share/icons") ||
		!strings.Contains(cmd, "/usr/local/share/backgrounds/WhiteSur") ||
		!strings.Contains(cmd, "/usr/local/share/plank/themes") {
		t.Fatal("display prepare must support image-baked desktop style assets in global paths")
	}
	if !strings.Contains(cmd, "librsvg2-common") {
		t.Fatal("display prepare must install SVG icon loading support for themed icon assets")
	}
	if !strings.Contains(cmd, "xfce4-appmenu-plugin") || !strings.Contains(cmd, "xfce4-windowck-plugin") || !strings.Contains(cmd, "appmenu-gtk3-module") {
		t.Fatal("display prepare must install macOS-like topbar plugins when available")
	}
	if topbarIndex := strings.Index(cmd, "configure_topbar\n  xfconf_set xfce4-panel /panels/panel-1/mode"); topbarIndex < 0 {
		t.Fatal("injected style script must set panel geometry after rebuilding the topbar panel")
	}
	if !strings.Contains(cmd, "if verify_style; then\n  exit 0\nfi\nexit 1") {
		t.Fatal("injected style script must return non-zero when style verification fails")
	}
	if strings.Contains(prepareScript, "apt-get install") || strings.Contains(prepareScript, "apk add") {
		t.Fatal("package installation details should stay in scripts/desktop-install.sh")
	}
	if strings.Contains(prepareScript, "set -- $(tr") {
		t.Fatal("Xvnc process detection must not word-split shell command lines")
	}
	if !strings.Contains(prepareScript, "grep -Eq '(^|/)Xvnc$'") || !strings.Contains(prepareScript, "grep -Fxq ':99'") {
		t.Fatal("Xvnc process detection must match real Xvnc processes on display :99")
	}
	if !strings.Contains(prepareScript, "grep -Eq '(^|/)(google-chrome-stable|google-chrome|chromium|chromium-browser|chrome)$'") {
		t.Fatal("browser process detection must match real browser argv entries only")
	}
	if !strings.Contains(prepareScript, "grep -Eq '^--type=' && continue") {
		t.Fatal("CDP readiness detection must ignore Chromium child processes")
	}
	if !strings.Contains(prepareScript, "start_desktop_session()") ||
		!strings.Contains(prepareScript, "stop_fallback_wm") ||
		!strings.Contains(prepareScript, "start_xfwm4()") ||
		!strings.Contains(prepareScript, "process_pids_by_name startxfce4 xfce4-session xfdesktop") ||
		!strings.Contains(prepareScript, "process_pids_by_name xfwm4") {
		t.Fatal("display prepare must prefer Xfce over the fallback window manager")
	}
	if strings.Contains(prepareScript, "grep -E 'xfce4-session|xfwm4|twm'") {
		t.Fatal("display prepare must not treat twm as a healthy Xfce desktop session")
	}
	if !strings.Contains(prepareScript, "xsetroot -cursor_name left_ptr") {
		t.Fatal("display prepare must replace the default X root cursor")
	}
	if !strings.Contains(prepareScript, "SingletonLock") {
		t.Fatal("display prepare must clean stale Chromium profile locks before starting the browser")
	}
	if strings.Contains(prepareScript, "rfbunixpath") || strings.Contains(prepareScript, "RFB_SOCKET") {
		t.Fatal("display prepare should use loopback TCP VNC instead of a bind-mounted Unix RFB socket")
	}
	if !strings.Contains(prepareScript, "-localhost -rfbport \"$RFB_PORT\"") {
		t.Fatal("display prepare must keep VNC on container loopback")
	}
	if !strings.Contains(prepareScript, `XVNC_GEOMETRY="${MEMOH_DISPLAY_GEOMETRY:-1280x960}"`) {
		t.Fatal("display prepare must default to the 4:3 desktop geometry")
	}
	if !strings.Contains(prepareScript, `-geometry "$XVNC_GEOMETRY"`) {
		t.Fatal("display prepare must pass the configured geometry to Xvnc")
	}
}

func TestDisplayApplyStyleCommandUsesImageScripts(t *testing.T) {
	t.Parallel()

	if got := displayApplyStyleCommand(); got != "/bin/sh /opt/memoh/scripts/display-apply-style.sh --if-needed" {
		t.Fatalf("displayApplyStyleCommand() = %q", got)
	}
	applyScript := readDisplayScript(t, "display-apply-style.sh")
	styleScript := readDisplayScript(t, "desktop-style.sh")
	cmd := applyScript + "\n" + styleScript
	if strings.Contains(cmd, "cat >/tmp/memoh-") || strings.Contains(cmd, "desktop-install.sh") {
		t.Fatal("display style apply must not inject scripts or install packages at runtime")
	}
	if !strings.Contains(applyScript, `desktop_style_script="${MEMOH_DESKTOP_STYLE_SCRIPT:-/opt/memoh/scripts/desktop-style.sh}"`) ||
		!strings.Contains(applyScript, `/bin/sh "$desktop_style_script"`) {
		t.Fatal("display style command must run the image-provided desktop style script")
	}
	if !strings.Contains(cmd, "style_marker=\"$style_config_dir/display-style.version\"") ||
		!strings.Contains(cmd, "style_version='2026-07-22.1'") ||
		!strings.Contains(cmd, "style_is_current") {
		t.Fatal("display style command must gate retries with a versioned marker")
	}
	if !strings.Contains(cmd, "style_lock=/tmp/memoh-desktop-style.lock") ||
		!strings.Contains(cmd, "acquire_style_lock") {
		t.Fatal("display style command must serialize style application with a lock")
	}
	if !strings.Contains(cmd, "style_lock_stale_seconds=60") ||
		!strings.Contains(cmd, `printf '%s\n' "$$" >"$style_lock/pid"`) ||
		!strings.Contains(cmd, `now_seconds >"$style_lock/created_at"`) ||
		!strings.Contains(cmd, "cleanup_stale_style_lock") ||
		!strings.Contains(cmd, `kill -0 "$owner_pid"`) ||
		!strings.Contains(cmd, `ps -p "$owner_pid"`) ||
		!strings.Contains(cmd, `Removing stale desktop style lock`) ||
		!strings.Contains(cmd, `rm -rf "$style_lock"`) ||
		!strings.Contains(cmd, `trap 'release_style_lock' EXIT INT TERM`) {
		t.Fatal("display style command must recover stale locks left behind by killed apply helpers")
	}
	if !strings.Contains(cmd, `tail -n 80 "$style_log"`) ||
		!strings.Contains(cmd, `printf '%s\n' "$style_version" >"$style_marker"`) {
		t.Fatal("display style command must persist success and print log tail on failure")
	}
	if !strings.Contains(applyScript, `mode="${1:---if-needed}"`) {
		t.Fatal("display style helper must only apply style when the marker is missing or stale")
	}
	if !strings.Contains(cmd, "configure_plank()") || !strings.Contains(cmd, "WhiteSur-Dark") {
		t.Fatal("display style command must include macOS-like desktop styling")
	}
	if !strings.Contains(cmd, "configure_topbar()") || !strings.Contains(cmd, "windowck-plugin") || !strings.Contains(cmd, "appmenu") {
		t.Fatal("display style command must include macOS-like topbar styling")
	}
	if strings.Contains(applyScript, "apt-get install") || strings.Contains(applyScript, "apk add") {
		t.Fatal("display style helper must rely on image-baked assets")
	}
	if !strings.Contains(cmd, "memoh-logo-white") || strings.Contains(cmd, "memoh-apple-symbol") {
		t.Fatal("display style command must use the white Memoh logo for the topbar menu icon")
	}
	if !strings.Contains(cmd, "memoh_logo_png_base64()") ||
		!strings.Contains(cmd, "memoh-logo-white.png") ||
		!strings.Contains(cmd, "/usr/share/icons/hicolor/48x48/apps") ||
		!strings.Contains(cmd, "base64 -d") {
		t.Fatal("display style command must install a PNG Memoh logo fallback for panels without SVG icon loading")
	}
	if !strings.Contains(cmd, "xfconf_replace_int_array xfce4-panel /panels 1") || !strings.Contains(cmd, "restart_xfce_panel()") {
		t.Fatal("display style command must remove the default Xfce bottom panel")
	}
	if !strings.Contains(cmd, "xsetroot -cursor_name left_ptr") || !strings.Contains(cmd, "nohup xfwm4 --replace") {
		t.Fatal("display style command must restore the Xfce window manager and pointer cursor")
	}
	if !strings.Contains(cmd, "plugin-ids 101 102 103 104 105 106 107 108") ||
		!strings.Contains(cmd, "xfconf_reset xfce4-panel /plugins/plugin-109") ||
		strings.Contains(cmd, "plugin-109 string actions") {
		t.Fatal("display style command must omit the Xfce actions menu with logout and power options")
	}
	if !strings.Contains(cmd, `write_chromium_wrapper "$browser"`) ||
		!strings.Contains(cmd, `write_desktop_file "$file" "Chromium" "chromium" "$wrapper"`) ||
		!strings.Contains(cmd, `--user-data-dir="\$profile"`) ||
		!strings.Contains(cmd, `rm -f "\$profile"/SingletonLock`) ||
		strings.Contains(cmd, `write_desktop_file "$file" "Browser" "web-browser"`) {
		t.Fatal("display style command must pin the dock browser launcher to a Chromium wrapper with an isolated profile")
	}
	if !strings.Contains(cmd, `write_desktop_file "$file" "Files" "$icon"`) ||
		!strings.Contains(cmd, "Memoh-WhiteSur-dark") ||
		!strings.Contains(cmd, "global_theme_dir") ||
		!strings.Contains(cmd, "install_file_manager_icon_aliases") ||
		!strings.Contains(cmd, "file_manager_icon_path()") ||
		!strings.Contains(cmd, "folder-blue.svg") ||
		!strings.Contains(cmd, "org.xfce.filemanager") ||
		!strings.Contains(cmd, "org.xfce.thunar") ||
		!strings.Contains(cmd, "inode-directory") ||
		!strings.Contains(cmd, "text-x-generic.svg") ||
		!strings.Contains(cmd, "verify_file_manager_style()") ||
		!strings.Contains(cmd, "restart_file_manager()") ||
		strings.Contains(cmd, "memoh_files_icon_png_base64") ||
		strings.Contains(cmd, "write_memoh_files_icon_png") {
		t.Fatal("display style command must configure the Files launcher and file-manager icon theme")
	}
}

func TestDisplayStyleStatusCommandChecksVersionMarker(t *testing.T) {
	t.Parallel()

	if got := displayStyleStatusCommand(); got != "/bin/sh /opt/memoh/scripts/display-apply-style.sh --check" {
		t.Fatalf("displayStyleStatusCommand() = %q", got)
	}
	cmd := readDisplayScript(t, "display-apply-style.sh")
	if !strings.Contains(cmd, "display-style.version") {
		t.Fatal("style status command must check the desktop style marker")
	}
	if !strings.Contains(cmd, "style_version='2026-07-22.1'") {
		t.Fatal("style status command must check the current style version")
	}
	if !strings.Contains(cmd, "MEMOH_DISPLAY_DESKTOP_STYLE") {
		t.Fatal("style status command must treat disabled desktop styling as current")
	}
}

func TestWorkspaceDockerfileInstallsDisplayAssetsGlobally(t *testing.T) {
	t.Parallel()

	data, err := os.ReadFile("../../docker/Dockerfile.workspace")
	if err != nil {
		t.Fatalf("read workspace Dockerfile: %v", err)
	}
	dockerfile := string(data)
	if !strings.Contains(dockerfile, "COPY scripts/desktop-install.sh /tmp/memoh-desktop-install.sh") {
		t.Fatal("workspace Dockerfile must install display assets through the shared install script")
	}
	if !strings.Contains(dockerfile, "MEMOH_DISPLAY_INSTALL_ASSETS_GLOBAL=1") {
		t.Fatal("workspace Dockerfile must bake static display style assets into global image paths")
	}
	if !strings.Contains(dockerfile, "MEMOH_TOOLKIT_GLIBC_ONLY=1") ||
		!strings.Contains(dockerfile, "COPY --from=toolkit-builder /opt/memoh/toolkit /opt/memoh/toolkit") {
		t.Fatal("workspace Dockerfile must own the canonical glibc toolkit")
	}
	if !strings.Contains(dockerfile, "COPY scripts/desktop-style.sh scripts/display-apply-style.sh scripts/display-prepare.sh /opt/memoh/scripts/") {
		t.Fatal("workspace Dockerfile must provide immutable display scripts")
	}
}
