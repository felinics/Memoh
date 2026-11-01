package protocolgen

import (
	"fmt"
	"strings"
)

// emitMethods renders method constants and the typed dispatch tables that tie
// wire method names to generated params/response types.
func (g *generator) emitMethods(b *strings.Builder) error {
	version, err := snapshotVersion()
	if err != nil {
		return err
	}
	b.WriteString("// PinnedCodexVersion is the codex CLI version the vendored schema snapshot\n")
	b.WriteString("// (and therefore these generated types) corresponds to.\n")
	fmt.Fprintf(b, "const PinnedCodexVersion = %q\n\n", version)

	b.WriteString("// Client request methods (Memoh → app-server).\nconst (\n")
	for _, m := range clientMethods {
		fmt.Fprintf(b, "\tMethod%s = %q\n", exportedName(m.Method), m.Method)
	}
	b.WriteString(")\n\n")

	b.WriteString("// Server request methods (app-server → Memoh, expect a response).\nconst (\n")
	for _, m := range serverRequestMethods {
		fmt.Fprintf(b, "\tMethod%s = %q\n", exportedName(m.Method), m.Method)
	}
	b.WriteString(")\n\n")

	b.WriteString("// Server notification methods decoded into typed params.\nconst (\n")
	for _, m := range serverNotifications {
		fmt.Fprintf(b, "\tMethod%s = %q\n", exportedName(m), m)
	}
	b.WriteString(")\n\n")

	b.WriteString("// NewResponseForMethod returns a pointer to the zero response value for a\n")
	b.WriteString("// client request method, ready for unmarshaling. ok is false for methods\n")
	b.WriteString("// whose responses are not generated (initialize) or unknown methods.\n")
	b.WriteString("func NewResponseForMethod(method string) (resp any, ok bool) {\n\tswitch method {\n")
	for _, m := range clientMethods {
		if m.Response == "" {
			continue
		}
		fmt.Fprintf(b, "\tcase %q:\n\t\treturn new(%s), true\n", m.Method, m.Response)
	}
	b.WriteString("\t}\n\treturn nil, false\n}\n\n")

	b.WriteString("// DecodeServerRequestParams decodes the params of an app-server → Memoh\n")
	b.WriteString("// request into its generated type. ok is false for unknown methods; the\n")
	b.WriteString("// caller keeps the raw envelope in that case.\n")
	b.WriteString("func DecodeServerRequestParams(method string, params []byte) (decoded any, ok bool, err error) {\n\tswitch method {\n")
	for _, m := range serverRequestMethods {
		variant := g.corpus.serverRequest[m.Method]
		paramsRef := refName(variant.Properties["params"].Ref)
		fmt.Fprintf(b, "\tcase %q:\n", m.Method)
		fmt.Fprintf(b, "\t\tv := new(%s)\n\t\terr = jsonUnmarshal(params, v)\n\t\treturn v, true, err\n", paramsRef)
	}
	b.WriteString("\t}\n\treturn nil, false, nil\n}\n\n")

	b.WriteString("// DecodeServerNotificationParams decodes the params of an app-server\n")
	b.WriteString("// notification into its generated type. ok is false for methods outside the\n")
	b.WriteString("// typed subset; those still surface to the caller as raw envelopes.\n")
	b.WriteString("func DecodeServerNotificationParams(method string, params []byte) (decoded any, ok bool, err error) {\n\tswitch method {\n")
	for _, m := range serverNotifications {
		variant := g.corpus.serverNotification[m]
		fmt.Fprintf(b, "\tcase %q:\n", m)
		params, hasParams := variant.Properties["params"]
		if !hasParams || params.Ref == "" {
			b.WriteString("\t\treturn nil, true, nil\n")
			continue
		}
		fmt.Fprintf(b, "\t\tv := new(%s)\n\t\terr = jsonUnmarshal(params, v)\n\t\treturn v, true, err\n", refName(params.Ref))
	}
	b.WriteString("\t}\n\treturn nil, false, nil\n}\n")
	return nil
}
