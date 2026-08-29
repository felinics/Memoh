package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestReplacementLookupsRunAfterSessionAuthorization locks an ordering that is
// invisible to the type system and cheap to undo by accident.
//
// resolveWSTargetTurnID reads session history to map the deprecated message_id
// spelling onto a round. Running it before authorizeWSSession answers "does
// this message id belong to this session" for a caller who cannot read the
// session at all — a cross-session existence oracle, even though no write
// follows it. The two replacement cases are the only place both calls appear,
// so the invariant is checked where it lives.
func TestReplacementLookupsRunAfterSessionAuthorization(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "local_channel.go", nil, 0)
	if err != nil {
		t.Fatalf("parse local_channel.go: %v", err)
	}

	for _, kind := range []string{"retry_message", "edit_message"} {
		clause := caseClauseFor(file, kind)
		if clause == nil {
			t.Fatalf("no %q case clause found", kind)
		}
		authorize := firstCallPos(clause, "authorizeWSSession")
		resolve := firstCallPos(clause, "resolveWSTargetTurnID")
		switch {
		case !authorize.IsValid():
			t.Fatalf("%s: no authorizeWSSession call", kind)
		case !resolve.IsValid():
			t.Fatalf("%s: no resolveWSTargetTurnID call", kind)
		case resolve < authorize:
			t.Fatalf(
				"%s: resolveWSTargetTurnID at %s runs before authorizeWSSession at %s; "+
					"the message_id lookup reads session history and must not answer an unauthorized caller",
				kind, fset.Position(resolve), fset.Position(authorize),
			)
		}
	}
}

// caseClauseFor finds the switch case whose value is the given string literal.
func caseClauseFor(file *ast.File, value string) *ast.CaseClause {
	var found *ast.CaseClause
	ast.Inspect(file, func(node ast.Node) bool {
		clause, ok := node.(*ast.CaseClause)
		if !ok || found != nil {
			return found == nil
		}
		for _, expr := range clause.List {
			lit, ok := expr.(*ast.BasicLit)
			if ok && lit.Kind == token.STRING && lit.Value == `"`+value+`"` {
				found = clause
				return false
			}
		}
		return true
	})
	return found
}

// firstCallPos reports where name is first called inside node.
func firstCallPos(node ast.Node, name string) token.Pos {
	pos := token.NoPos
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		var ident *ast.Ident
		switch fun := call.Fun.(type) {
		case *ast.Ident:
			ident = fun
		case *ast.SelectorExpr:
			ident = fun.Sel
		}
		if ident != nil && ident.Name == name && (!pos.IsValid() || call.Pos() < pos) {
			pos = call.Pos()
		}
		return true
	})
	return pos
}
