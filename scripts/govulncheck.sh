#!/usr/bin/env bash
set -euo pipefail

# Fails on any vulnerability govulncheck proves our code can actually reach.
# Vulnerabilities that only exist in imported-but-uncalled code are reported by
# govulncheck but do not fail this gate — that reachability filter is the whole
# reason we run govulncheck instead of a plain dependency scanner.
#
# The accepted list below holds the reachable findings we consciously carry.
# Every entry needs a reason and an exit condition. An entry that stops matching
# a finding also fails this gate, so the list cannot rot silently.
accepted() {
  # github.com/docker/docker v28.5.2+incompatible, reached from the Docker
  # container backend (domains/runtime/internal/container/docker). The only
  # published fix is github.com/moby/moby/v2 >= 2.0.0-beta.14, and moving a
  # production container runtime onto a beta module is a worse trade than
  # carrying these. Drop them once moby/moby/v2 has a stable release.
  cat <<'EOF'
GO-2026-4883 moby: plugin privilege off-by-one; fix only in moby/v2 beta
GO-2026-4887 moby: authz bypass on oversized bodies; fix only in moby/v2 beta
GO-2026-5617 moby: docker cp bind mount redirection race; fix only in moby/v2 beta
GO-2026-5668 moby: docker cp symlink swap race; fix only in moby/v2 beta
EOF
}

workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT
report="$workdir/report.json"

go run golang.org/x/vuln/cmd/govulncheck@latest -format json ./... >"$report"

# A finding carries a call stack (trace[0].function) only when the vulnerable
# symbol is reachable from our code.
jq -r 'select(.finding.trace[0].function != null) | .finding.osv' "$report" | sort -u >"$workdir/reachable"
accepted | awk '{print $1}' | sort -u >"$workdir/accepted"

comm -23 "$workdir/reachable" "$workdir/accepted" >"$workdir/unexpected"
comm -13 "$workdir/reachable" "$workdir/accepted" >"$workdir/stale"

status=0

if [ -s "$workdir/unexpected" ]; then
  status=1
  echo "Reachable vulnerabilities with no accepted exception:" >&2
  while read -r id; do
    summary="$(jq -r --arg id "$id" 'select(.osv.id == $id) | .osv.summary' "$report" | head -1)"
    echo "  $id  $summary" >&2
    echo "    https://pkg.go.dev/vuln/$id" >&2
  done <"$workdir/unexpected"
  echo "Upgrade the affected module, or add the ID to accepted() in $0 with a reason." >&2
fi

if [ -s "$workdir/stale" ]; then
  status=1
  echo "Accepted exceptions that no longer match any reachable finding:" >&2
  sed 's/^/  /' "$workdir/stale" >&2
  echo "Remove them from accepted() in $0." >&2
fi

if [ "$status" -eq 0 ]; then
  echo "govulncheck: no reachable vulnerabilities outside the $(wc -l <"$workdir/accepted" | tr -d ' ') accepted exceptions."
fi

exit "$status"
