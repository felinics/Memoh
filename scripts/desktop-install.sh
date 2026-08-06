#!/bin/sh

apt_get_once() {
  timeout_seconds="${MEMOH_APT_COMMAND_TIMEOUT:-300}"
  if has_cmd timeout; then
    timeout "$timeout_seconds" apt-get \
      -o "Acquire::Retries=${MEMOH_APT_RETRIES:-2}" \
      -o "Acquire::Connect::Timeout=${MEMOH_APT_CONNECT_TIMEOUT:-20}" \
      -o "Acquire::http::Timeout=${MEMOH_APT_HTTP_TIMEOUT:-30}" \
      -o "Acquire::https::Timeout=${MEMOH_APT_HTTP_TIMEOUT:-30}" \
      "$@"
    return $?
  fi
  apt-get \
    -o "Acquire::Retries=${MEMOH_APT_RETRIES:-2}" \
    -o "Acquire::Connect::Timeout=${MEMOH_APT_CONNECT_TIMEOUT:-20}" \
    -o "Acquire::http::Timeout=${MEMOH_APT_HTTP_TIMEOUT:-30}" \
    -o "Acquire::https::Timeout=${MEMOH_APT_HTTP_TIMEOUT:-30}" \
    "$@"
}

apt_get() {
  max_attempts="${MEMOH_APT_COMMAND_RETRIES:-3}"
  retry_delay="${MEMOH_APT_RETRY_DELAY:-5}"
  case "$max_attempts" in
    ""|*[!0-9]*|0) max_attempts=3 ;;
  esac
  case "$retry_delay" in
    ""|*[!0-9]*) retry_delay=5 ;;
  esac

  attempt=1
  while :; do
    if apt_get_once "$@"; then
      return 0
    else
      status=$?
    fi
    if [ "$attempt" -ge "$max_attempts" ]; then
      return "$status"
    fi
    wait_seconds=$((retry_delay * attempt))
    echo "apt-get failed with exit code $status (attempt $attempt/$max_attempts); retrying in ${wait_seconds}s" >&2
    sleep "$wait_seconds"
    attempt=$((attempt + 1))
  done
}

run_limited() {
  timeout_seconds="${1:-180}"
  shift
  if has_cmd timeout; then
    timeout "$timeout_seconds" "$@"
    return $?
  fi
  "$@"
}

install_debian_optional() {
  optional_packages=""
  for package in "$@"; do
    if apt-cache show "$package" >/dev/null 2>&1; then
      optional_packages="$optional_packages $package"
    fi
  done
  [ -n "$optional_packages" ] || return 0
  # Optional styling packages should never block display setup.
  apt_get install -y --no-install-recommends $optional_packages || true
}

install_alpine_optional() {
  for package in "$@"; do
    apk add --no-cache "$package" >/dev/null 2>&1 || true
  done
}

display_style_enabled() {
  style="${MEMOH_DISPLAY_DESKTOP_STYLE:-macos}"
  case "$style" in
    ""|0|false|False|FALSE|off|Off|OFF|none|None|NONE) return 1 ;;
    *) return 0 ;;
  esac
}

has_macos_theme_assets() {
  has_one_dir \
    "$HOME/.themes/WhiteSur-Dark-alt" \
    "$HOME/.themes/WhiteSur-Dark" \
    "$HOME/.themes/WhiteSur-Dark-solid" \
    "$HOME/.local/share/themes/WhiteSur-Dark-alt" \
    "$HOME/.local/share/themes/WhiteSur-Dark" \
    "$HOME/.local/share/themes/WhiteSur-Dark-solid" \
    /usr/local/share/themes/WhiteSur-Dark-alt \
    /usr/local/share/themes/WhiteSur-Dark \
    /usr/local/share/themes/WhiteSur-Dark-solid \
    /usr/share/themes/WhiteSur-Dark-alt \
    /usr/share/themes/WhiteSur-Dark \
    /usr/share/themes/WhiteSur-Dark-solid &&
    has_one_dir \
      "$HOME/.local/share/icons/WhiteSur" \
      "$HOME/.icons/WhiteSur" \
      "$HOME/.local/share/icons/WhiteSur-dark" \
      "$HOME/.icons/WhiteSur-dark" \
      /usr/local/share/icons/WhiteSur \
      /usr/local/share/icons/WhiteSur-dark \
      /usr/share/icons/WhiteSur \
      /usr/share/icons/WhiteSur-dark &&
    has_one_dir \
      "$HOME/.local/share/plank/themes/macOS Dark" \
      "$HOME/.local/share/plank/themes/Big Sur Dark" \
      "/usr/local/share/plank/themes/macOS Dark" \
      "/usr/local/share/plank/themes/Big Sur Dark" \
      "/usr/share/plank/themes/macOS Dark" \
      "/usr/share/plank/themes/Big Sur Dark" &&
    has_macos_wallpaper_assets
}

has_one_dir() {
  for dir in "$@"; do
    [ -d "$dir" ] && return 0
  done
  return 1
}

has_macos_wallpaper_assets() {
  for dir in "$HOME/.local/share/backgrounds/WhiteSur" /usr/local/share/backgrounds/WhiteSur /usr/share/backgrounds/WhiteSur; do
    [ -d "$dir" ] || continue
    find "$dir" -type f \( -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.png' \) 2>/dev/null | grep -q . && return 0
  done
  return 1
}

install_macos_assets_globally() {
  case "${MEMOH_DISPLAY_INSTALL_ASSETS_GLOBAL:-}" in
    1|true|True|TRUE|yes|Yes|YES|on|On|ON) return 0 ;;
    *) return 1 ;;
  esac
}

clone_or_update_theme_repo() {
  url="$1"
  dest="$2"
  [ -n "$url" ] && [ -n "$dest" ] || return 1
  if [ -d "$dest/.git" ]; then
    run_limited "${MEMOH_THEME_FETCH_TIMEOUT:-180}" git -C "$dest" fetch --depth=1 origin >/dev/null 2>&1 || return 1
    run_limited "${MEMOH_THEME_FETCH_TIMEOUT:-180}" git -C "$dest" reset --hard origin/HEAD >/dev/null 2>&1 || return 1
    return 0
  fi
  rm -rf "$dest"
  run_limited "${MEMOH_THEME_FETCH_TIMEOUT:-180}" git clone --depth=1 "$url" "$dest" >/dev/null 2>&1
}

install_macos_theme_assets() {
  display_style_enabled || return 0
  has_macos_theme_assets && return 0
  has_cmd git || return 0
  has_cmd bash || return 0

  theme_cache="${XDG_CACHE_HOME:-$HOME/.cache}/memoh-display-themes"
  if install_macos_assets_globally; then
    theme_dest=/usr/local/share/themes
    icon_dest=/usr/local/share/icons
    plank_dest=/usr/local/share/plank/themes
    wallpaper_dest=/usr/local/share/backgrounds/WhiteSur
  else
    theme_dest="$HOME/.themes"
    icon_dest="$HOME/.local/share/icons"
    plank_dest="$HOME/.local/share/plank/themes"
    wallpaper_dest="$HOME/.local/share/backgrounds/WhiteSur"
  fi
  mkdir -p "$theme_cache" "$theme_dest" "$icon_dest" "$plank_dest" "$wallpaper_dest"

  progress 58 desktop "Installing macOS desktop theme"

  if clone_or_update_theme_repo https://github.com/vinceliuice/WhiteSur-gtk-theme.git "$theme_cache/WhiteSur-gtk-theme"; then
    (
      cd "$theme_cache/WhiteSur-gtk-theme"
      export SUDO_USER="${SUDO_USER:-root}"
      run_limited "${MEMOH_THEME_INSTALL_TIMEOUT:-240}" ./install.sh \
        -d "$theme_dest" \
        -n WhiteSur \
        -c dark \
        -o normal \
        -t default \
        -a alt \
        -m \
        --silent-mode
    ) || true
  fi

  if clone_or_update_theme_repo https://github.com/vinceliuice/WhiteSur-icon-theme.git "$theme_cache/WhiteSur-icon-theme"; then
    (
      cd "$theme_cache/WhiteSur-icon-theme"
      run_limited "${MEMOH_THEME_INSTALL_TIMEOUT:-180}" ./install.sh \
        -d "$icon_dest" \
        -n WhiteSur \
        -t default \
        -a
    ) || true
  fi

  if clone_or_update_theme_repo https://github.com/vinceliuice/WhiteSur-cursors.git "$theme_cache/WhiteSur-cursors"; then
    (
      cd "$theme_cache/WhiteSur-cursors"
      run_limited "${MEMOH_THEME_INSTALL_TIMEOUT:-120}" ./install.sh
    ) || true
  fi

  if clone_or_update_theme_repo https://github.com/x64Bits/plank-themes.git "$theme_cache/plank-themes"; then
    find "$theme_cache/plank-themes" -mindepth 1 -maxdepth 1 -type d ! -name .git -exec cp -a {} "$plank_dest/" \; 2>/dev/null || true
  fi

  if clone_or_update_theme_repo https://github.com/vinceliuice/WhiteSur-wallpapers.git "$theme_cache/WhiteSur-wallpapers"; then
    find "$theme_cache/WhiteSur-wallpapers" -type f \( -iname '*.jpg' -o -iname '*.jpeg' -o -iname '*.png' \) -exec cp -a {} "$wallpaper_dest/" \; 2>/dev/null || true
  fi
}

install_debian_style_extras() {
  display_style_enabled || return 0
  has_macos_theme_assets && return 0
  if [ "${MEMOH_APT_INDEX_UPDATED:-0}" != "1" ]; then
    apt_get update && MEMOH_APT_INDEX_UPDATED=1 || true
  fi
  progress 54 desktop "Installing macOS desktop theme dependencies"
  install_debian_optional sudo git unzip bash sassc libglib2.0-bin libglib2.0-dev-bin libglib2.0-dev libxml2-utils librsvg2-common gtk2-engines-murrine gtk2-engines-pixbuf plank papirus-icon-theme arc-theme fonts-inter gnome-themes-extra bibata-cursor-theme xfce4-appmenu-plugin xfce4-windowck-plugin appmenu-gtk2-module appmenu-gtk3-module appmenu-registrar
  has_macos_theme_assets && return 0
  install_macos_theme_assets
}

install_alpine_style_extras() {
  display_style_enabled || return 0
  has_macos_theme_assets && return 0
  install_alpine_optional sudo git unzip bash sassc glib-dev libxml2-utils librsvg plank papirus-icon-theme arc-theme font-inter
  install_macos_theme_assets
}

install_style_extras_for_current_os() {
  display_style_enabled || return 0
  if is_debian_like; then
    install_debian_style_extras
  elif is_alpine; then
    install_alpine_style_extras
  else
    install_macos_theme_assets
  fi
}

install_debian() {
  has_cmd apt-get || { echo "This image looks Debian-like but apt-get is unavailable. Install the Memoh workspace toolkit or use a Debian/Alpine image." >&2; exit 1; }
  export DEBIAN_FRONTEND=noninteractive
  export APT_LISTCHANGES_FRONTEND=none
  progress 18 system "Detected Debian workspace"
  progress 24 installing "Updating package index"
  apt_get update
  MEMOH_APT_INDEX_UPDATED=1
  if ! has_cmd apt-extracttemplates; then
    apt_get install -y --no-install-recommends apt-utils
  fi
  progress 42 installing "Installing VNC, desktop, accessibility, and CJK fonts"
  apt_get install -y --no-install-recommends ca-certificates curl gnupg dbus-x11 x11-xserver-utils xterm xfce4 xfce4-terminal tigervnc-standalone-server fontconfig fonts-dejavu fonts-noto-cjk fonts-noto-color-emoji procps at-spi2-core tini
  install_debian_style_extras
  if ! find_browser >/dev/null 2>&1; then
    progress 66 browser "Installing browser"
    if apt_get install -y --no-install-recommends chromium || apt_get install -y --no-install-recommends chromium-browser; then
      return 0
    fi
    arch="$(dpkg --print-architecture)"
    [ "$arch" = "amd64" ] || return 1
    install -d -m 0755 /etc/apt/keyrings
    rm -f /etc/apt/keyrings/google-chrome.gpg
    curl -fsSL https://dl.google.com/linux/linux_signing_key.pub | gpg --batch --yes --dearmor -o /etc/apt/keyrings/google-chrome.gpg
    echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/google-chrome.gpg] http://dl.google.com/linux/chrome/deb/ stable main" >/etc/apt/sources.list.d/google-chrome.list
    apt_get update
    apt_get install -y --no-install-recommends google-chrome-stable
  fi
}

install_alpine() {
  has_cmd apk || { echo "This image looks Alpine-like but apk is unavailable. Install the Memoh workspace toolkit or use a Debian/Alpine image." >&2; exit 1; }
  progress 18 system "Detected Alpine workspace"
  progress 42 installing "Installing VNC, desktop, browser, accessibility, and CJK fonts"
  apk add --no-cache tigervnc xkeyboard-config xfce4 xfce4-terminal dbus-x11 xterm chromium fontconfig ttf-dejavu font-noto-cjk font-noto-emoji at-spi2-core
  install_alpine_style_extras
}
