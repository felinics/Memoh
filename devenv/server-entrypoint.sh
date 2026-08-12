#!/bin/sh
set -e

DEV_TZ="${TZ:-UTC}"
if [ ! -f "/usr/share/zoneinfo/$DEV_TZ" ]; then
  echo "WARN: timezone '$DEV_TZ' not found, falling back to UTC." >&2
  DEV_TZ="UTC"
fi
ln -snf "/usr/share/zoneinfo/$DEV_TZ" /etc/localtime
printf '%s\n' "$DEV_TZ" > /etc/timezone

# Clean up stale CNI state from previous runs. After a container restart the
# cni0 bridge may linger with a zeroed MAC (00:00:00:00:00:00), causing the
# bridge plugin to fail with "could not set bridge's mac: invalid argument".
ip link delete cni0 2>/dev/null || true
rm -rf /var/lib/cni/networks/* /var/lib/cni/results/* 2>/dev/null || true

# Ensure IP forwarding and subnet MASQUERADE for CNI.
sysctl -w net.ipv4.ip_forward=1 2>/dev/null || true
iptables -t nat -C POSTROUTING -s 10.88.0.0/16 ! -o cni0 -j MASQUERADE 2>/dev/null || \
  iptables -t nat -A POSTROUTING -s 10.88.0.0/16 ! -o cni0 -j MASQUERADE 2>/dev/null || true

# Setup cgroup v2 delegation for nested containerd.
if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
  mkdir -p /sys/fs/cgroup/init
  while read -r pid; do
    echo "$pid" > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
  done < /sys/fs/cgroup/cgroup.procs

  sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers \
    > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
fi

mkdir -p /run/containerd
containerd &
CONTAINERD_PID=$!

echo "Waiting for containerd..."
for _ in $(seq 1 30); do
  if ctr version >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

if ! ctr version >/dev/null 2>&1; then
  echo "ERROR: containerd not responsive after 30s"
  exit 1
fi
echo "containerd is running (pid $CONTAINERD_PID)"

workspace_image_archive=/opt/memoh/dev-images/workspace-debian.tar
if [ ! -r "$workspace_image_archive" ]; then
  echo "ERROR: Development workspace image archive is missing: $workspace_image_archive" >&2
  echo "       Run 'mise run dev:workspace-image' before starting the development server." >&2
  exit 1
fi

containerd_namespace="${MEMOH_CONTAINERD_NAMESPACE:-default}"
workspace_image_ref="${MEMOH_DEV_WORKSPACE_IMAGE:-memohai/workspace:debian}"
# Importing the ~1GB archive takes far longer than the rest of this entrypoint,
# and it happens on every container start. The stamp records which archive the
# store already holds; it lives inside /var/lib/containerd so it is wiped by the
# same `docker compose down -v` that wipes the image store it vouches for.
workspace_image_stamp=/var/lib/containerd/.memoh-dev-workspace-image
workspace_archive_key="$(stat -c '%s:%Y' "$workspace_image_archive")"

workspace_image_present() {
  ctr --namespace "$containerd_namespace" images ls -q 2>/dev/null | grep -Fqx \
    -e "$workspace_image_ref" \
    -e "docker.io/$workspace_image_ref" \
    -e "docker.io/library/$workspace_image_ref"
}

if [ "$(cat "$workspace_image_stamp" 2>/dev/null || true)" = "$workspace_archive_key" ] \
  && workspace_image_present; then
  echo "Development workspace image is already imported (archive unchanged)."
else
  echo "Importing development workspace image..."
  ctr --namespace "$containerd_namespace" images import "$workspace_image_archive"
  printf '%s\n' "$workspace_archive_key" > "$workspace_image_stamp"
fi
echo "Development workspace image is ready."

# Build bridge binary into runtime directory (first boot)
echo "Building bridge binary..."
(cd /workspace && go build -o /opt/memoh/runtime/bridge ./cmd/bridge)
echo "Bridge binary ready."

echo "Starting server..."

trap 'kill ${SERVER_PID:-0} 2>/dev/null || true; kill ${CONTAINERD_PID:-0} 2>/dev/null || true; wait' TERM INT

"$@" &
SERVER_PID=$!

wait $SERVER_PID
EXIT_CODE=$?

kill $CONTAINERD_PID 2>/dev/null || true
wait $CONTAINERD_PID 2>/dev/null || true

exit $EXIT_CODE
