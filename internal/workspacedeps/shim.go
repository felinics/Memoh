package workspacedeps

import "strings"

// toolkitCABundle is the CA bundle the workspace image ships under the
// toolkit. Agent shims export it as SSL_CERT_FILE when nothing else set one,
// mirroring the toolkit's python3/pip3 wrappers (docker/toolkit/bin).
const toolkitCABundle = "/opt/memoh/toolkit/certs/ca-certificates.crt"

// ShimScript returns the contents of a PATH shim that execs entrypoint with
// the caller's arguments. Agent shims additionally point SSL_CERT_FILE at the
// toolkit CA bundle when it exists and the variable is unset.
func ShimScript(entrypoint string, agent bool) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	if agent {
		b.WriteString(`if [ -z "${SSL_CERT_FILE:-}" ] && [ -f ` + toolkitCABundle + ` ]; then
  export SSL_CERT_FILE=` + toolkitCABundle + `
fi
`)
	}
	b.WriteString("exec " + shellQuote(entrypoint) + ` "$@"` + "\n")
	return b.String()
}
