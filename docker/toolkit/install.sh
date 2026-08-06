#!/bin/sh
# Download Node.js (glibc + musl), Python (glibc + musl), uv, and Xvnc runtime
# files into a workspace runtime assembly.
#
# Usage:
#   ./docker/toolkit/install.sh toolkit_output_dir [arch] [display_output_dir]
#
# Arguments:
#   toolkit_output_dir  Required target toolkit directory
#   arch                amd64 or arm64 (default: auto-detect from uname -m)
#   display_output_dir  Target display directory (default: toolkit/display)
#
# Environment variables for mirrors (useful in mainland China):
#   NODEJS_MIRROR       Default: https://nodejs.org/dist
#   NODEJS_MUSL_MIRROR  Default: https://unofficial-builds.nodejs.org/download/release
#   NPM_MIRROR          Default: https://registry.npmjs.org
#   PYTHON_VERSION      Default: pinned CPython version below
#   PYTHON_STANDALONE_TAG
#                       Default: pinned python-build-standalone release tag below
#   PYTHON_STANDALONE_MIRROR
#                       Default: https://github.com/astral-sh/python-build-standalone/releases/download
#   PYTHON_EXTRA_ARCHES Optional space-separated extra Python runtime arches:
#                       amd64/x86_64 or arm64/aarch64. Useful for emulated
#                       workspace images in local development.
#   CODEX_VERSION       Default: pinned @openai/codex version below
#   CODEX_ACP_VERSION   Default: pinned @agentclientprotocol/codex-acp version below
#   CLAUDE_AGENT_ACP_VERSION
#                       Default: pinned @agentclientprotocol/claude-agent-acp version below
#   HERMES_AGENT_VERSION
#                       Default: pinned hermes-agent[acp,mcp] version below
#   HERMES_AGENT_PACKAGE
#                       Default: hermes-agent[acp,mcp]==$HERMES_AGENT_VERSION
#   ALPINE_MIRROR       Default: https://dl-cdn.alpinelinux.org/alpine
#   DEBIAN_MIRROR       Default: https://deb.debian.org/debian
#   DEBIAN_VERSION      Default: bookworm
#   UV_MIRROR           Default: https://github.com/astral-sh/uv/releases/latest/download
#   MEMOH_DISPLAY_OUTDIR
#                       Optional override for display_output_dir.
#   MEMOH_TOOLKIT_GLIBC_ONLY
#                       Set to 1 for the official Debian workspace image.
#   MEMOH_TOOLKIT_SKIP_DISPLAY
#                       Set to 1 when the image provides display packages itself.
#
set -eu

ALPINE_VERSION=3.23
NODE_VERSION=24.14.0
NPM_VERSION=10.9.2
PYTHON_VERSION="${PYTHON_VERSION:-3.14.6}"
PYTHON_STANDALONE_TAG="${PYTHON_STANDALONE_TAG:-20260623}"
CODEX_VERSION="${CODEX_VERSION:-0.145.0}"
CODEX_ACP_VERSION="${CODEX_ACP_VERSION:-1.1.9}"
CLAUDE_AGENT_ACP_VERSION="${CLAUDE_AGENT_ACP_VERSION:-0.64.2}"
HERMES_AGENT_VERSION="${HERMES_AGENT_VERSION:-0.19.0}"
HERMES_AGENT_PACKAGE="${HERMES_AGENT_PACKAGE:-hermes-agent[acp,mcp]==$HERMES_AGENT_VERSION}"

if [ "$#" -lt 1 ] || [ -z "${1:-}" ]; then
  echo "ERROR: toolkit_output_dir is required." >&2
  echo "Usage: $0 toolkit_output_dir [arch] [display_output_dir]" >&2
  exit 2
fi

OUTDIR="$1"
ARCH="${2:-}"
DISPLAY_OUTDIR="${MEMOH_DISPLAY_OUTDIR:-${3:-$OUTDIR/display}}"
SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"

if [ -z "$ARCH" ]; then
  case "$(uname -m)" in
    x86_64)  ARCH=amd64 ;;
    aarch64) ARCH=arm64 ;;
    arm64)   ARCH=arm64 ;;
    *) echo "ERROR: unsupported architecture: $(uname -m)" >&2; exit 1 ;;
  esac
fi

NODEJS_MIRROR="${NODEJS_MIRROR:-https://nodejs.org/dist}"
NODEJS_MUSL_MIRROR="${NODEJS_MUSL_MIRROR:-https://unofficial-builds.nodejs.org/download/release}"
NPM_MIRROR="${NPM_MIRROR:-https://registry.npmjs.org}"
PYTHON_STANDALONE_MIRROR="${PYTHON_STANDALONE_MIRROR:-https://github.com/astral-sh/python-build-standalone/releases/download}"
ALPINE_MIRROR="${ALPINE_MIRROR:-https://dl-cdn.alpinelinux.org/alpine}"
DEBIAN_MIRROR="${DEBIAN_MIRROR:-https://deb.debian.org/debian}"
DEBIAN_VERSION="${DEBIAN_VERSION:-bookworm}"
UV_MIRROR="${UV_MIRROR:-https://github.com/astral-sh/uv/releases/latest/download}"

case "$ARCH" in
  amd64) NODE_ARCH=x64;  UV_ARCH=x86_64;  APK_ARCH=x86_64;  DEB_ARCH=amd64; NPM_CPU=x64;  PYTHON_ARCH=x86_64 ;;
  arm64) NODE_ARCH=arm64; UV_ARCH=aarch64; APK_ARCH=aarch64; DEB_ARCH=arm64; NPM_CPU=arm64; PYTHON_ARCH=aarch64 ;;
  *) echo "ERROR: unsupported arch: $ARCH" >&2; exit 1 ;;
esac

ALPINE_MAIN_REPO="${ALPINE_MIRROR}/v${ALPINE_VERSION}/main"
ALPINE_COMMUNITY_REPO="${ALPINE_MIRROR}/v${ALPINE_VERSION}/community"
ALPINE_MAIN_ARCH_REPO="${ALPINE_MAIN_REPO}/${APK_ARCH}"
ALPINE_COMMUNITY_ARCH_REPO="${ALPINE_COMMUNITY_REPO}/${APK_ARCH}"

TMPDIR="$(mktemp -d)"
cleanup() {
  rm -rf "$TMPDIR"
}
trap cleanup EXIT INT TERM

apk_main_index_path="$TMPDIR/APKINDEX-main.tar.gz"
apk_community_index_path="$TMPDIR/APKINDEX-community.tar.gz"
apk_main_index_text="$TMPDIR/APKINDEX-main"
apk_community_index_text="$TMPDIR/APKINDEX-community"

ensure_apk_indexes() {
  if [ ! -f "$apk_main_index_path" ]; then
    wget -qO "$apk_main_index_path" "${ALPINE_MAIN_ARCH_REPO}/APKINDEX.tar.gz"
    tar -xzOf "$apk_main_index_path" APKINDEX > "$apk_main_index_text"
  fi
  if [ ! -f "$apk_community_index_path" ]; then
    wget -qO "$apk_community_index_path" "${ALPINE_COMMUNITY_ARCH_REPO}/APKINDEX.tar.gz"
    tar -xzOf "$apk_community_index_path" APKINDEX > "$apk_community_index_text"
  fi
}

apk_package_field() {
  pkg="$1"
  field="$2"
  for index_text in "$apk_main_index_text" "$apk_community_index_text"; do
    value="$(awk -v pkg="$pkg" -v field="$field" '
      $0 == "P:" pkg { hit = 1; next }
      hit && index($0, field ":") == 1 { print substr($0, length(field) + 2); exit }
      /^$/ { hit = 0 }
    ' "$index_text")"
    if [ -n "$value" ]; then
      echo "$value"
      return
    fi
  done
}

apk_package_repo() {
  pkg="$1"
  for repo in main community; do
    case "$repo" in
      main) index_text="$apk_main_index_text"; repo_url="$ALPINE_MAIN_ARCH_REPO" ;;
      community) index_text="$apk_community_index_text"; repo_url="$ALPINE_COMMUNITY_ARCH_REPO" ;;
    esac
    if awk -v pkg="$pkg" '
      $0 == "P:" pkg { found = 1; exit }
      END { exit found ? 0 : 1 }
    ' "$index_text"; then
      echo "$repo_url"
      return
    fi
  done
}

apk_package_filename() {
  pkg="$1"
  version="$(apk_package_field "$pkg" V)"
  if [ -n "$version" ]; then
    echo "$pkg-$version.apk"
  fi
}

apk_package_deps() {
  pkg="$1"
  apk_package_field "$pkg" D | tr ' ' '\n' | awk '
    /^$/ { next }
    /^!/ { next }
    {
      dep = $0
      if (dep !~ /^(so:|cmd:|pc:)/) sub(/[<>=~].*/, "", dep)
      if (dep != "") print dep
    }
  '
}

apk_package_provider() {
  dep="$1"
  for index_text in "$apk_main_index_text" "$apk_community_index_text"; do
    provider="$(awk -v dep="$dep" '
      /^P:/ { pkg = substr($0, 3); next }
      /^p:/ {
        split(substr($0, 3), provides, " ")
        for (i in provides) {
          item = provides[i]
          sub(/[<>=~].*/, "", item)
          if (item == dep) {
            print pkg
            exit
          }
        }
      }
    ' "$index_text")"
    if [ -n "$provider" ]; then
      echo "$provider"
      return
    fi
  done
}

resolve_apk_dependency() {
  dep="$1"
  if [ -n "$(apk_package_filename "$dep")" ]; then
    echo "$dep"
    return
  fi
  apk_package_provider "$dep"
}

install_apk_package() {
  pkg="$1"
  root="$2"
  case " $INSTALLED_APK_PACKAGES " in
    *" $pkg "*) return ;;
  esac

  for dep in $(apk_package_deps "$pkg"); do
    dep_pkg="$(resolve_apk_dependency "$dep")"
    if [ -n "$dep_pkg" ]; then
      install_apk_package "$dep_pkg" "$root"
    fi
  done

  apk_file="$(apk_package_filename "$pkg")"
  repo_url="$(apk_package_repo "$pkg")"
  if [ -z "$apk_file" ] || [ -z "$repo_url" ]; then
    echo "ERROR: failed to resolve Alpine package $pkg (${APK_ARCH})" >&2
    exit 1
  fi

  pkg_path="$TMPDIR/$apk_file"
  extract_dir="$TMPDIR/extract-$pkg"
  rm -rf "$extract_dir"
  mkdir -p "$extract_dir"
  if [ ! -f "$pkg_path" ]; then
    wget -qO "$pkg_path" "${repo_url}/$apk_file"
  fi
  tar -xzf "$pkg_path" -C "$extract_dir"
  cp -a "$extract_dir/." "$root/"
  INSTALLED_APK_PACKAGES="$INSTALLED_APK_PACKAGES $pkg"
}

install_ca_bundle() {
  dest_dir="$OUTDIR/certs"
  dest_path="$dest_dir/ca-certificates.crt"

  mkdir -p "$dest_dir"
  for candidate in \
    /etc/ssl/certs/ca-certificates.crt \
    /etc/ssl/cert.pem \
    /opt/homebrew/etc/ca-certificates/cert.pem \
    /usr/local/etc/openssl@3/cert.pem \
    /usr/local/etc/openssl/cert.pem; do
    if [ -f "$candidate" ]; then
      cp "$candidate" "$dest_path"
      chmod 0644 "$dest_path"
      echo "CA bundle installed from $candidate"
      return
    fi
  done

  echo "warning: no host CA bundle found; ACP agents may fail HTTPS requests in minimal workspace images." >&2
}

apk_package_filename_from_index() {
  pkg="$1"
  index_text="$2"
  awk -v pkg="$pkg" '
    $0 == "P:" pkg { hit = 1; next }
    hit && /^V:/ { print pkg "-" substr($0, 3) ".apk"; exit }
    /^$/ { hit = 0 }
  ' "$index_text"
}

install_musl_runtime_libs() {
  dest_dir="$OUTDIR/node-musl/runtime-lib"
  if [ -f "$dest_dir/libgcc_s.so.1" ] && [ -f "$dest_dir/libstdc++.so.6" ]; then
    echo "musl runtime libs already installed; skipping download."
    return
  fi

  rm -rf "$dest_dir"
  mkdir -p "$dest_dir"

  echo "Downloading musl runtime libs (${APK_ARCH})..."
  ensure_apk_indexes

  for pkg in libgcc libstdc++; do
    apk_file="$(apk_package_filename_from_index "$pkg" "$apk_main_index_text")"
    if [ -z "$apk_file" ]; then
      echo "ERROR: failed to resolve Alpine package for $pkg (${APK_ARCH})" >&2
      exit 1
    fi
    pkg_path="$TMPDIR/$apk_file"
    extract_dir="$TMPDIR/extract-$pkg"
    rm -rf "$extract_dir"
    mkdir -p "$extract_dir"
    wget -qO "$pkg_path" "${ALPINE_MAIN_ARCH_REPO}/$apk_file"
    tar -xzf "$pkg_path" -C "$extract_dir"
    cp -a "$extract_dir/usr/lib/." "$dest_dir/"
  done
}

extract_debian_data_archive() {
  deb_path="$1"
  extract_dir="$2"

  if command -v ar >/dev/null 2>&1; then
    (cd "$extract_dir" && ar x "$deb_path")
    find "$extract_dir" -name 'data.tar.*' | head -n 1
    return
  fi

  if command -v python3 >/dev/null 2>&1; then
    python3 - "$deb_path" "$extract_dir" <<'PY'
import os
import sys

deb_path, extract_dir = sys.argv[1], sys.argv[2]
with open(deb_path, "rb") as fh:
    if fh.read(8) != b"!<arch>\n":
        raise SystemExit("not a Debian ar archive")
    while True:
        header = fh.read(60)
        if not header:
            break
        if len(header) != 60 or header[58:60] != b"`\n":
            raise SystemExit("invalid Debian ar member header")
        name = header[:16].decode("utf-8").strip().rstrip("/")
        size = int(header[48:58].decode("utf-8").strip())
        payload = fh.read(size)
        if size % 2:
            fh.read(1)
        if name.startswith("data.tar."):
            out = os.path.join(extract_dir, name)
            with open(out, "wb") as out_fh:
                out_fh.write(payload)
            print(out)
            raise SystemExit(0)
raise SystemExit("Debian package has no data archive")
PY
    return
  fi

  echo "ERROR: ar or python3 is required to extract Debian packages" >&2
  exit 1
}

extract_debian_package() {
  deb_path="$1"
  extract_dir="$2"

  if command -v dpkg-deb >/dev/null 2>&1; then
    dpkg-deb -x "$deb_path" "$extract_dir"
    return
  fi

  data_archive="$(extract_debian_data_archive "$deb_path" "$extract_dir")"
  if [ -z "$data_archive" ]; then
    echo "ERROR: libssl3 Debian package has no data archive" >&2
    exit 1
  fi
  case "$data_archive" in
    *.tar.xz) tar -xJf "$data_archive" -C "$extract_dir" ;;
    *.tar.gz) tar -xzf "$data_archive" -C "$extract_dir" ;;
    *.tar.zst)
      if tar --help 2>/dev/null | grep -q -- '--zstd'; then
        tar --zstd -xf "$data_archive" -C "$extract_dir"
      elif command -v zstd >/dev/null 2>&1; then
        zstd -dc "$data_archive" | tar -xf - -C "$extract_dir"
      else
        echo "ERROR: zstd is required to extract $data_archive" >&2
        exit 1
      fi
      ;;
    *) echo "ERROR: unsupported Debian data archive $data_archive" >&2; exit 1 ;;
  esac
}

install_glibc_openssl_libs() {
  dest_dir="$OUTDIR/glibc-lib"
  if [ -f "$dest_dir/libssl.so.3" ] && [ -f "$dest_dir/libcrypto.so.3" ]; then
    echo "glibc OpenSSL runtime libs already installed; skipping download."
    return
  fi

  rm -rf "$dest_dir"
  mkdir -p "$dest_dir"

  if [ "$(uname -s)" = "Linux" ]; then
    for lib_dir in /usr/lib/x86_64-linux-gnu /usr/lib/aarch64-linux-gnu /lib/x86_64-linux-gnu /lib/aarch64-linux-gnu; do
      if [ -f "$lib_dir/libssl.so.3" ] && [ -f "$lib_dir/libcrypto.so.3" ]; then
        cp -a "$lib_dir/libssl.so.3" "$lib_dir/libcrypto.so.3" "$dest_dir/"
        echo "glibc OpenSSL runtime libs installed from $lib_dir"
        return
      fi
    done
  fi

  echo "Downloading glibc OpenSSL runtime libs (${DEB_ARCH})..."
  index_gz="$TMPDIR/debian-packages-${DEB_ARCH}.gz"
  index_text="$TMPDIR/debian-packages-${DEB_ARCH}"
  wget -qO "$index_gz" "${DEBIAN_MIRROR}/dists/${DEBIAN_VERSION}/main/binary-${DEB_ARCH}/Packages.gz"
  gunzip -c "$index_gz" > "$index_text"

  deb_filename="$(awk '
    /^Package: libssl3$/ { hit = 1; next }
    hit && /^Filename: / { print substr($0, 11); exit }
    /^$/ { hit = 0 }
  ' "$index_text")"
  if [ -z "$deb_filename" ]; then
    echo "ERROR: failed to resolve Debian package libssl3 (${DEB_ARCH})" >&2
    exit 1
  fi

  deb_path="$TMPDIR/libssl3.deb"
  extract_dir="$TMPDIR/extract-libssl3"
  rm -rf "$extract_dir"
  mkdir -p "$extract_dir"
  wget -qO "$deb_path" "${DEBIAN_MIRROR}/${deb_filename}"
  extract_debian_package "$deb_path" "$extract_dir"

  libssl_path="$(find "$extract_dir" -path '*/libssl.so.3' | head -n 1)"
  libcrypto_path="$(find "$extract_dir" -path '*/libcrypto.so.3' | head -n 1)"
  if [ -z "$libssl_path" ] || [ -z "$libcrypto_path" ]; then
    echo "ERROR: libssl3 package did not contain libssl.so.3 and libcrypto.so.3" >&2
    exit 1
  fi
  cp -a "$libssl_path" "$libcrypto_path" "$dest_dir/"
}

install_pinned_npm() {
  node_dir="$1"
  dest_dir="$OUTDIR/$node_dir/lib/node_modules/npm"
  extract_dir="$TMPDIR/npm-$node_dir"
  if [ -f "$dest_dir/bin/npm-cli.js" ]; then
    current_version="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$dest_dir/package.json" 2>/dev/null | head -n 1)"
    if [ "$current_version" = "$NPM_VERSION" ]; then
      echo "npm v${NPM_VERSION} already installed for $node_dir; skipping download."
      return
    fi
    echo "Replacing npm v${current_version:-unknown} with pinned npm v${NPM_VERSION} for $node_dir."
  fi

  ensure_npm_archive

  rm -rf "$dest_dir" "$extract_dir"
  mkdir -p "$extract_dir" "$(dirname "$dest_dir")"
  tar -xzf "$npm_archive" -C "$extract_dir"
  mv "$extract_dir/package" "$dest_dir"
}

ensure_npm_archive() {
  npm_archive="$TMPDIR/npm.tgz"
  if [ -f "$npm_archive" ]; then
    return
  fi
  echo "Downloading npm v${NPM_VERSION}..."
  wget -qO "$npm_archive" "${NPM_MIRROR}/npm/-/npm-${NPM_VERSION}.tgz"
}

install_node_glibc() {
  dest_dir="$OUTDIR/node-glibc"
  if [ -x "$dest_dir/bin/node" ]; then
    echo "Node.js v${NODE_VERSION} (glibc, ${NODE_ARCH}) already installed; skipping download."
    return
  fi

  rm -rf "$dest_dir"
  mkdir -p "$dest_dir"
  echo "Downloading Node.js v${NODE_VERSION} (glibc, ${NODE_ARCH})..."
  wget -qO- "${NODEJS_MIRROR}/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${NODE_ARCH}.tar.xz" \
    | tar -xJf - --strip-components=1 -C "$dest_dir"
}

install_node_musl() {
  dest_dir="$OUTDIR/node-musl"
  if [ -x "$dest_dir/bin/node" ]; then
    echo "Node.js v${NODE_VERSION} (musl, ${NODE_ARCH}) already installed; skipping download."
    return
  fi

  rm -rf "$dest_dir"
  mkdir -p "$dest_dir"
  MUSL_URL="${NODEJS_MUSL_MIRROR}/v${NODE_VERSION}/node-v${NODE_VERSION}-linux-${NODE_ARCH}-musl.tar.xz"
  echo "Downloading Node.js v${NODE_VERSION} (musl, ${NODE_ARCH})..."
  musl_archive="$TMPDIR/node-musl.tar.xz"
  if wget -qO "$musl_archive" "$MUSL_URL" 2>/dev/null; then
    tar -xJf "$musl_archive" --strip-components=1 -C "$dest_dir"
  else
    echo "ERROR: failed to download musl Node.js build for ${NODE_ARCH}" >&2
    exit 1
  fi
}

install_uv() {
  if [ -x "$OUTDIR/uv" ]; then
    echo "uv already installed; skipping download."
    return
  fi

  echo "Downloading uv (${UV_ARCH})..."
  extract_dir="$TMPDIR/uv"
  mkdir -p "$extract_dir"
  wget -qO- "${UV_MIRROR}/uv-${UV_ARCH}-unknown-linux-musl.tar.gz" \
    | tar -xzf - --strip-components=1 -C "$extract_dir"
  mv "$extract_dir/uv" "$OUTDIR/uv"
  chmod +x "$OUTDIR/uv"
}

python_arch_from_toolkit_arch() {
  case "$1" in
    amd64|x86_64) echo x86_64 ;;
    arm64|aarch64) echo aarch64 ;;
    *) echo "ERROR: unsupported Python runtime arch: $1" >&2; exit 1 ;;
  esac
}

install_python_runtime() {
  libc="$1"
  python_arch="$2"
  link_default="$3"
  triple="${python_arch}-unknown-linux-$libc"
  dest_dir="$OUTDIR/python-$libc-$python_arch"
  marker="$dest_dir/.memoh-python-version"

  if [ -x "$dest_dir/bin/python3" ] &&
    [ -f "$marker" ] &&
    grep -qx "${PYTHON_VERSION}+${PYTHON_STANDALONE_TAG} ${triple}" "$marker"; then
    echo "Python ${PYTHON_VERSION} (${libc}, ${python_arch}) already installed; skipping download."
    if [ "$link_default" = "true" ]; then
      rm -f "$OUTDIR/python-$libc"
      ln -s "$(basename "$dest_dir")" "$OUTDIR/python-$libc"
    fi
    return
  fi

  rm -rf "$dest_dir"
  mkdir -p "$dest_dir"

  archive="cpython-${PYTHON_VERSION}+${PYTHON_STANDALONE_TAG}-${triple}-install_only_stripped.tar.gz"
  url="${PYTHON_STANDALONE_MIRROR}/${PYTHON_STANDALONE_TAG}/${archive}"
  echo "Downloading Python ${PYTHON_VERSION} (${libc}, ${python_arch})..."
  wget -qO- "$url" | tar -xzf - --strip-components=1 -C "$dest_dir"

  if [ ! -x "$dest_dir/bin/python3" ]; then
    echo "ERROR: Python archive did not install bin/python3 for ${triple}" >&2
    exit 1
  fi
  echo "${PYTHON_VERSION}+${PYTHON_STANDALONE_TAG} ${triple}" > "$marker"
  if [ "$link_default" = "true" ]; then
    rm -f "$OUTDIR/python-$libc"
    ln -s "$(basename "$dest_dir")" "$OUTDIR/python-$libc"
  fi
}

npm_cli() {
  node_dir="$1"
  echo "$OUTDIR/$node_dir/lib/node_modules/npm/bin/npm-cli.js"
}

run_toolkit_npm() {
  node_dir="$1"
  shift
  node_bin="$OUTDIR/$node_dir/bin/node"
  npm_bin="$(npm_cli "$node_dir")"
  if [ ! -x "$node_bin" ] || [ ! -f "$npm_bin" ]; then
    return 1
  fi

  case "$node_dir" in
    node-musl)
      if [ -d "$OUTDIR/node-musl/runtime-lib" ]; then
        LD_LIBRARY_PATH="$OUTDIR/node-musl/runtime-lib${LD_LIBRARY_PATH:+:$LD_LIBRARY_PATH}" \
          "$node_bin" "$npm_bin" "$@"
      else
        "$node_bin" "$npm_bin" "$@"
      fi
      ;;
    *)
      "$node_bin" "$npm_bin" "$@"
      ;;
  esac
}

install_acp_packages_with_toolkit_npm() {
  node_dir="$1"
  run_toolkit_npm "$node_dir" \
    install \
    -g \
    --prefix "$OUTDIR/acp" \
    --include=optional \
    --omit=dev \
    --no-audit \
    --no-fund \
    --registry "$NPM_MIRROR" \
    --os=linux \
    --cpu="$NPM_CPU" \
    --libc=glibc \
    "@openai/codex@$CODEX_VERSION" \
    "@agentclientprotocol/codex-acp@$CODEX_ACP_VERSION" \
    "@agentclientprotocol/claude-agent-acp@$CLAUDE_AGENT_ACP_VERSION"
}

install_acp_packages_with_host_npm() {
  npm \
    install \
    -g \
    --prefix "$OUTDIR/acp" \
    --include=optional \
    --omit=dev \
    --no-audit \
    --no-fund \
    --registry "$NPM_MIRROR" \
    --os=linux \
    --cpu="$NPM_CPU" \
    --libc=glibc \
    "@openai/codex@$CODEX_VERSION" \
    "@agentclientprotocol/codex-acp@$CODEX_ACP_VERSION" \
    "@agentclientprotocol/claude-agent-acp@$CLAUDE_AGENT_ACP_VERSION"
}

# acp_package_at_version checks that an installed ACP npm package matches the
# pinned version, so bumping a pin in this script triggers a reinstall instead
# of being silently skipped by the file-existence check.
acp_package_at_version() {
  pkg_json="$OUTDIR/acp/lib/node_modules/$1/package.json"
  [ -f "$pkg_json" ] || return 1
  installed="$(sed -n 's/.*"version"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$pkg_json" | head -n 1)"
  [ "$installed" = "$2" ]
}

install_acp_packages() {
  codex_bin="$OUTDIR/acp/lib/node_modules/@openai/codex/bin/codex.js"
  codex_acp_bin="$OUTDIR/acp/lib/node_modules/@agentclientprotocol/codex-acp/dist/index.js"
  claude_agent_acp_bin="$OUTDIR/acp/lib/node_modules/@agentclientprotocol/claude-agent-acp/dist/index.js"
  if [ -f "$codex_bin" ] && [ -f "$codex_acp_bin" ] && [ -f "$claude_agent_acp_bin" ] &&
    acp_package_at_version "@openai/codex" "$CODEX_VERSION" &&
    acp_package_at_version "@agentclientprotocol/codex-acp" "$CODEX_ACP_VERSION" &&
    acp_package_at_version "@agentclientprotocol/claude-agent-acp" "$CLAUDE_AGENT_ACP_VERSION"; then
    echo "ACP agent packages already installed at pinned versions; skipping npm install."
    return
  fi

  echo "Installing ACP agent packages for linux-${NPM_CPU}..."
  mkdir -p "$OUTDIR/acp"

  # On Linux builders, prefer the freshly downloaded target Node/npm so Docker
  # builds do not depend on a host npm install. On macOS development hosts the
  # downloaded Linux Node cannot run, so fall back to the project npm.
  if [ "$(uname -s)" = "Linux" ]; then
    if install_acp_packages_with_toolkit_npm node-glibc; then
      return
    fi
    if install_acp_packages_with_toolkit_npm node-musl; then
      return
    fi
  fi

  if command -v npm >/dev/null 2>&1; then
    install_acp_packages_with_host_npm
    return
  fi

  echo "ERROR: npm is required to install Codex ACP packages into the workspace toolkit." >&2
  exit 1
}

install_hermes_acp_package() {
  if [ "$(uname -s)" != "Linux" ]; then
    echo "warning: skipping Hermes ACP package cache on non-Linux host; build the Linux workspace toolkit in Docker/CI for container Hermes support." >&2
    return
  fi
  if [ ! -x "$OUTDIR/uv" ]; then
    echo "ERROR: uv is required to install Hermes ACP into the workspace toolkit." >&2
    exit 1
  fi

  mkdir -p "$OUTDIR/uv-cache" "$OUTDIR/uv-tools" "$OUTDIR/python"
  if UV_CACHE_DIR="$OUTDIR/uv-cache" \
    UV_TOOL_DIR="$OUTDIR/uv-tools" \
    UV_PYTHON_INSTALL_DIR="$OUTDIR/python" \
    "$OUTDIR/uv" tool run --offline --from "$HERMES_AGENT_PACKAGE" hermes-acp --help >/dev/null 2>&1; then
    echo "Hermes ACP package already cached at pinned version; skipping uv install."
    return
  fi

  echo "Installing Hermes ACP package with uv ($HERMES_AGENT_PACKAGE)..."
  UV_CACHE_DIR="$OUTDIR/uv-cache" \
    UV_TOOL_DIR="$OUTDIR/uv-tools" \
    UV_PYTHON_INSTALL_DIR="$OUTDIR/python" \
    "$OUTDIR/uv" tool run --from "$HERMES_AGENT_PACKAGE" hermes-acp --help >/dev/null
}

install_toolkit_wrappers() {
  source_dir="$SCRIPT_DIR/bin"
  if [ ! -d "$source_dir" ]; then
    echo "warning: toolkit command wrappers not found at $source_dir" >&2
    return
  fi

  mkdir -p "$OUTDIR/bin"
  cp -R "$source_dir"/. "$OUTDIR/bin/"
  for wrapper in "$OUTDIR"/bin/*; do
    [ -e "$wrapper" ] || continue
    chmod +x "$wrapper"
  done
}

write_display_wrappers() {
  mkdir -p "$DISPLAY_OUTDIR/bin"

  write_display_musl_wrapper() {
    name="$1"
    cat > "$DISPLAY_OUTDIR/bin/$name" <<EOF
#!/bin/sh
set -eu
SELF="\$0"
if command -v readlink >/dev/null 2>&1; then
  RESOLVED="\$(readlink -f "\$0" 2>/dev/null || true)"
  if [ -n "\$RESOLVED" ]; then
    SELF="\$RESOLVED"
  fi
fi
ROOT="\$(CDPATH= cd -- "\$(dirname -- "\$SELF")/../root" && pwd)"
ARCH="\$(uname -m)"
case "\$ARCH" in
  x86_64)  LOADER="\$ROOT/lib/ld-musl-x86_64.so.1" ;;
  aarch64|arm64) LOADER="\$ROOT/lib/ld-musl-aarch64.so.1" ;;
  *) echo "ERROR: unsupported architecture: \$ARCH" >&2; exit 1 ;;
esac
export PATH="\$ROOT/../bin:\$ROOT/usr/bin:\$PATH"
export XKB_CONFIG_ROOT="\$ROOT/usr/share/X11/xkb"
export FONTCONFIG_PATH="\$ROOT/etc/fonts"
export XDG_DATA_DIRS="\$ROOT/usr/share:\${XDG_DATA_DIRS:-/usr/local/share:/usr/share}"
exec "\$LOADER" --library-path "\$ROOT/lib:\$ROOT/usr/lib" "\$ROOT/usr/bin/$name" "\$@"
EOF
    chmod +x "$DISPLAY_OUTDIR/bin/$name"
  }

  cat > "$DISPLAY_OUTDIR/bin/xkbcomp" <<'EOF'
#!/bin/sh
set -eu
SELF="$0"
if command -v readlink >/dev/null 2>&1; then
  RESOLVED="$(readlink -f "$0" 2>/dev/null || true)"
  if [ -n "$RESOLVED" ]; then
    SELF="$RESOLVED"
  fi
fi
ROOT="$(CDPATH= cd -- "$(dirname -- "$SELF")/../root" && pwd)"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  LOADER="$ROOT/lib/ld-musl-x86_64.so.1" ;;
  aarch64|arm64) LOADER="$ROOT/lib/ld-musl-aarch64.so.1" ;;
  *) echo "ERROR: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
exec "$LOADER" --library-path "$ROOT/lib:$ROOT/usr/lib" "$ROOT/usr/bin/xkbcomp" "$@"
EOF
  chmod +x "$DISPLAY_OUTDIR/bin/xkbcomp"

  cat > "$DISPLAY_OUTDIR/bin/Xvnc" <<'EOF'
#!/bin/sh
set -eu
SELF="$0"
if command -v readlink >/dev/null 2>&1; then
  RESOLVED="$(readlink -f "$0" 2>/dev/null || true)"
  if [ -n "$RESOLVED" ]; then
    SELF="$RESOLVED"
  fi
fi
ROOT="$(CDPATH= cd -- "$(dirname -- "$SELF")/../root" && pwd)"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64)  LOADER="$ROOT/lib/ld-musl-x86_64.so.1" ;;
  aarch64|arm64) LOADER="$ROOT/lib/ld-musl-aarch64.so.1" ;;
  *) echo "ERROR: unsupported architecture: $ARCH" >&2; exit 1 ;;
esac
export PATH="$ROOT/../bin:$ROOT/usr/bin:$PATH"
export XKB_CONFIG_ROOT="$ROOT/usr/share/X11/xkb"
export FONTCONFIG_PATH="$ROOT/etc/fonts"
exec "$LOADER" --library-path "$ROOT/lib:$ROOT/usr/lib" "$ROOT/usr/bin/Xvnc" -xkbdir "$ROOT/usr/share/X11/xkb" "$@"
EOF
  chmod +x "$DISPLAY_OUTDIR/bin/Xvnc"

  write_display_musl_wrapper xsetroot
  write_display_musl_wrapper twm
  write_display_musl_wrapper xterm
}

display_bundle_installed() {
  [ -x "$DISPLAY_OUTDIR/root/usr/bin/Xvnc" ] || return 1
  [ -x "$DISPLAY_OUTDIR/root/usr/bin/xkbcomp" ] || return 1
  [ -x "$DISPLAY_OUTDIR/root/usr/bin/xsetroot" ] || return 1
  [ -x "$DISPLAY_OUTDIR/root/usr/bin/twm" ] || return 1
  [ -x "$DISPLAY_OUTDIR/root/usr/bin/xterm" ] || return 1

  write_display_wrappers
}

check_display_bundle_executables() {
  case "$(uname -s)" in
    Linux)
      "$DISPLAY_OUTDIR/bin/Xvnc" -version >/dev/null 2>&1 || return 1
      "$DISPLAY_OUTDIR/bin/xkbcomp" -version >/dev/null 2>&1 || return 1
      "$DISPLAY_OUTDIR/bin/xsetroot" -version >/dev/null 2>&1 || return 1
      "$DISPLAY_OUTDIR/bin/twm" -V >/dev/null 2>&1 || return 1
      "$DISPLAY_OUTDIR/bin/xterm" -version >/dev/null 2>&1 || return 1
      ;;
    *)
      if ! command -v docker >/dev/null 2>&1; then
        echo "ERROR: checking the Linux display runtime on $(uname -s) requires docker." >&2
        return 1
      fi
      display_abs="$(cd "$DISPLAY_OUTDIR" && pwd)"
      docker run --rm \
        -v "$display_abs:/display:ro" \
        "alpine:${ALPINE_VERSION}" \
        sh -eu -c '
          /display/bin/Xvnc -version >/dev/null 2>&1
          /display/bin/xkbcomp -version >/dev/null 2>&1
          /display/bin/xsetroot -version >/dev/null 2>&1
          /display/bin/twm -V >/dev/null 2>&1
          /display/bin/xterm -version >/dev/null 2>&1
        ' || return 1
      ;;
  esac
}

remove_display_bundle() {
  [ -e "$DISPLAY_OUTDIR" ] || return

  if rm -rf "$DISPLAY_OUTDIR" 2>/dev/null; then
    return
  fi

  if ! command -v docker >/dev/null 2>&1; then
    echo "ERROR: failed to remove existing display runtime at $DISPLAY_OUTDIR" >&2
    echo "       The directory may contain files owned by a Docker-mapped user." >&2
    exit 1
  fi

  display_parent="$(dirname "$DISPLAY_OUTDIR")"
  display_base="$(basename "$DISPLAY_OUTDIR")"
  case "$display_base" in
    ""|"."|".."|*/*)
      echo "ERROR: refusing to remove unsafe display path: $DISPLAY_OUTDIR" >&2
      exit 1
      ;;
  esac

  mkdir -p "$display_parent"
  display_parent_abs="$(cd "$display_parent" && pwd)"
  if ! docker run --rm \
    -v "$display_parent_abs:/out" \
    "alpine:${ALPINE_VERSION}" \
    sh -eu -c 'rm -rf "/out/$1"' \
    sh "$display_base"; then
    echo "ERROR: failed to remove existing display runtime at $DISPLAY_OUTDIR with docker" >&2
    exit 1
  fi
}

install_display_bundle() {
  mkdir -p "$DISPLAY_OUTDIR"
  if display_bundle_installed; then
    echo "Display bundle already installed to $DISPLAY_OUTDIR; skipping download."
    return
  fi

  remove_display_bundle
  mkdir -p "$DISPLAY_OUTDIR/bin"

  echo "Installing display runtime from Alpine packages (${APK_ARCH})..."
  if command -v apk >/dev/null 2>&1; then
    apk add \
      --root "$DISPLAY_OUTDIR/root" \
      --initdb \
      --no-cache \
      --no-scripts \
      --allow-untrusted \
      --repository "$ALPINE_MAIN_REPO" \
      --repository "$ALPINE_COMMUNITY_REPO" \
      tigervnc \
      xkeyboard-config \
      font-misc-misc \
      xsetroot \
      twm \
      xterm
  elif command -v docker >/dev/null 2>&1; then
    display_abs="$(cd "$DISPLAY_OUTDIR" && pwd)"
    host_uid="$(id -u)"
    host_gid="$(id -g)"
    docker run --rm \
      -v "$display_abs:/out" \
      "alpine:${ALPINE_VERSION}" \
      sh -eu -c 'apk add --root /out/root --initdb --no-cache --no-scripts --allow-untrusted --repository "$1" --repository "$2" tigervnc xkeyboard-config font-misc-misc xsetroot twm xterm; chown -R "$3:$4" /out/bin /out/root' \
      sh "$ALPINE_MAIN_REPO" "$ALPINE_COMMUNITY_REPO" "$host_uid" "$host_gid"
  else
    echo "ERROR: installing the display runtime requires apk or docker." >&2
    exit 1
  fi

  if ! display_bundle_installed; then
    echo "ERROR: display bundle check failed after installation" >&2
    exit 1
  fi

  if ! check_display_bundle_executables; then
    echo "ERROR: display bundle executable validation failed after installation" >&2
    exit 1
  fi

  echo "Display bundle installed to $DISPLAY_OUTDIR"
}

is_linux_elf() {
  [ -r "$1" ] || return 1
  magic="$(dd if="$1" bs=1 count=4 2>/dev/null | LC_ALL=C od -An -tx1 | tr -d ' \n')"
  [ "$magic" = "7f454c46" ]
}

install_a11y_cli() {
  dest_dir="$DISPLAY_OUTDIR/bin"
  dest_path="$dest_dir/a11y-cli"
  if [ -x "$dest_path" ] && is_linux_elf "$dest_path"; then
    return
  fi
  if [ -e "$dest_path" ] && ! is_linux_elf "$dest_path"; then
    echo "Removing non-Linux a11y-cli at $dest_path"
    rm -f "$dest_path"
  fi
  mkdir -p "$dest_dir"

  # Honor an explicit override so cross-arch release pipelines can drop a
  # prebuilt Linux binary into place.
  if [ -n "${MEMOH_A11Y_CLI_BINARY:-}" ] && is_linux_elf "$MEMOH_A11Y_CLI_BINARY"; then
    cp "$MEMOH_A11Y_CLI_BINARY" "$dest_path"
    chmod +x "$dest_path"
    echo "a11y-cli installed from $MEMOH_A11Y_CLI_BINARY"
    return
  fi

  # Prefer the cross-built Linux binary produced by `mise run a11y-cli:build`.
  # `target/release/a11y-cli` is only safe when the host itself is Linux,
  # otherwise it is the macOS/Windows host build and would crash inside the
  # workspace container with "Exec format error".
  for candidate in \
    "target/linux/release/a11y-cli" \
    "target/release/a11y-cli" \
    "target/x86_64-unknown-linux-gnu/release/a11y-cli" \
    "target/aarch64-unknown-linux-gnu/release/a11y-cli" \
    "target/x86_64-unknown-linux-musl/release/a11y-cli" \
    "target/aarch64-unknown-linux-musl/release/a11y-cli"; do
    if is_linux_elf "$candidate"; then
      cp "$candidate" "$dest_path"
      chmod +x "$dest_path"
      echo "a11y-cli installed from $candidate"
      return
    fi
  done

  echo "warning: no Linux a11y-cli release binary found." >&2
  echo "         Run 'mise run a11y-cli:build' or set MEMOH_A11Y_CLI_BINARY to a Linux ELF." >&2
}

mkdir -p "$OUTDIR/node-glibc"

install_node_glibc
install_glibc_openssl_libs
install_python_runtime gnu "$PYTHON_ARCH" true
for extra_arch in ${PYTHON_EXTRA_ARCHES:-}; do
  extra_python_arch="$(python_arch_from_toolkit_arch "$extra_arch")"
  if [ "$extra_python_arch" = "$PYTHON_ARCH" ]; then
    continue
  fi
  install_python_runtime gnu "$extra_python_arch" false
done
install_pinned_npm node-glibc

if [ "${MEMOH_TOOLKIT_GLIBC_ONLY:-0}" != "1" ]; then
  mkdir -p "$OUTDIR/node-musl"
  install_node_musl
  install_musl_runtime_libs
  install_python_runtime musl "$PYTHON_ARCH" true
  for extra_arch in ${PYTHON_EXTRA_ARCHES:-}; do
    extra_python_arch="$(python_arch_from_toolkit_arch "$extra_arch")"
    if [ "$extra_python_arch" = "$PYTHON_ARCH" ]; then
      continue
    fi
    install_python_runtime musl "$extra_python_arch" false
  done
  install_pinned_npm node-musl
fi

install_uv
install_acp_packages
install_hermes_acp_package
install_toolkit_wrappers
install_ca_bundle

echo "Toolkit installed to $OUTDIR"
if [ "${MEMOH_TOOLKIT_SKIP_DISPLAY:-0}" != "1" ]; then
  install_display_bundle
  install_a11y_cli
fi
