#!/bin/sh
set -e

CONTAINER_BACKEND=$(sed -n '
  /^[[:space:]]*\[container\][[:space:]]*$/,/^[[:space:]]*\[/ {
    s/^[[:space:]]*backend[[:space:]]*=[[:space:]]*"\([^"]*\)".*$/\1/p
  }
' "${CONFIG_PATH:-/app/config.toml}" 2>/dev/null | head -n 1)
CONTAINERD_PID=""

if [ "$CONTAINER_BACKEND" != "docker" ] && [ "$CONTAINER_BACKEND" != "apple" ]; then
  # ---- Clean up stale CNI state from previous runs ----
  # After a container restart the cni0 bridge may linger with a zeroed MAC
  # (00:00:00:00:00:00), causing "could not set bridge's mac: invalid argument".
  ip link delete cni0 2>/dev/null || true
  rm -rf /var/lib/cni/networks/* /var/lib/cni/results/* 2>/dev/null || true

  # ---- Ensure IP forwarding and subnet MASQUERADE for CNI ----
  sysctl -w net.ipv4.ip_forward=1 2>/dev/null || true
  iptables -t nat -C POSTROUTING -s 10.88.0.0/16 ! -o cni0 -j MASQUERADE 2>/dev/null || \
    iptables -t nat -A POSTROUTING -s 10.88.0.0/16 ! -o cni0 -j MASQUERADE 2>/dev/null || true

  # ---- Setup cgroup v2 delegation for nested containerd ----
  if [ -f /sys/fs/cgroup/cgroup.controllers ]; then
    echo "Setting up cgroup v2 delegation..."
    mkdir -p /sys/fs/cgroup/init
    # Move existing processes out of root cgroup to allow subtree control
    while read -r pid; do
      echo "$pid" > /sys/fs/cgroup/init/cgroup.procs 2>/dev/null || true
    done < /sys/fs/cgroup/cgroup.procs
    # Enable all available controllers for subtree delegation
    sed -e 's/ / +/g' -e 's/^/+/' < /sys/fs/cgroup/cgroup.controllers \
      > /sys/fs/cgroup/cgroup.subtree_control 2>/dev/null || true
  fi

  # ---- Start containerd in background ----
  mkdir -p /run/containerd
  containerd &
  CONTAINERD_PID=$!

  echo "Waiting for containerd..."
  for i in $(seq 1 30); do
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
fi

echo "Starting memoh-server..."

# ---- Start server (foreground, trap signals for graceful shutdown) ----
shutdown() {
  echo "Shutting down..."
  kill "$SERVER_PID" 2>/dev/null || true
  if [ -n "$CONTAINERD_PID" ]; then
    kill "$CONTAINERD_PID" 2>/dev/null || true
  fi
  wait
}
trap shutdown TERM INT

/app/memoh-server serve &
SERVER_PID=$!

wait $SERVER_PID
EXIT_CODE=$?

if [ -n "$CONTAINERD_PID" ]; then
  kill "$CONTAINERD_PID" 2>/dev/null || true
  wait "$CONTAINERD_PID" 2>/dev/null || true
fi

exit $EXIT_CODE
