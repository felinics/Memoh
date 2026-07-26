package catalog

import (
	"log/slog"
	"reflect"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/domains/channel/identity"
	"github.com/memohai/memoh/domains/channel/internal/adapter/matrix"
	"github.com/memohai/memoh/domains/channel/internal/adapter/qq"
	"github.com/memohai/memoh/domains/channel/route"
)

func TestNewRegistryWiresAdapterSpecificPersistence(t *testing.T) {
	identities := identity.NewService(nil, nil)
	routes := route.NewService(nil, nil)
	store := gateway.NewStore(nil, nil)
	registry := NewRegistry(Deps{
		Log:        slog.Default(),
		Identities: identities,
		Routes:     routes,
	})
	if err := WirePersistence(registry, store); err != nil {
		t.Fatalf("wire adapter persistence: %v", err)
	}

	qqAdapter, ok := registry.Get(qq.Type)
	if !ok {
		t.Fatal("QQ adapter is not registered")
	}
	qqValue := reflect.ValueOf(qqAdapter).Elem()
	if qqValue.FieldByName("identity").IsNil() {
		t.Fatal("QQ identity resolver is not wired")
	}
	if qqValue.FieldByName("routes").IsNil() {
		t.Fatal("QQ route resolver is not wired")
	}

	matrixAdapter, ok := registry.Get(matrix.Type)
	if !ok {
		t.Fatal("Matrix adapter is not registered")
	}
	matrixValue := reflect.ValueOf(matrixAdapter).Elem()
	if matrixValue.FieldByName("saveSince").IsNil() {
		t.Fatal("Matrix sync state saver is not wired")
	}
}
