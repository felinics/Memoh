package containerd

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/containernetworking/cni/libcni"
	cnitypes "github.com/containernetworking/cni/pkg/types"
)

func TestIsCNICheckUnsupportedMatchesLibcniSentinel(t *testing.T) {
	list, err := libcni.ConfListFromBytes([]byte(`{"cniVersion":"0.3.1","name":"memoh","plugins":[{"type":"bridge"}]}`))
	if err != nil {
		t.Fatalf("parse conf list: %v", err)
	}
	checkErr := libcni.NewCNIConfig(nil, nil).CheckNetworkList(context.Background(), list, &libcni.RuntimeConf{ContainerID: "c", NetNS: "/proc/1/ns/net", IfName: "eth0"})
	if checkErr == nil {
		t.Fatal("CheckNetworkList() error = nil, want unsupported CHECK")
	}
	if !isCNICheckUnsupported(checkErr) {
		t.Fatalf("isCNICheckUnsupported(%v) = false, want true", checkErr)
	}
	if isCNICheckUnsupported(errors.New("plugin bridge does not support the CHECK command")) {
		t.Fatal("isCNICheckUnsupported matched text without the libcni sentinel")
	}
}

func TestCNISetupRetryConditionsReadOnlyPluginMessage(t *testing.T) {
	pluginErr := func(msg string) error {
		return fmt.Errorf("plugin type=%q failed (add): %w", "bridge", cnitypes.NewError(cnitypes.ErrInternal, msg, ""))
	}
	tests := map[string]struct {
		err   error
		check func(error) bool
		want  bool
	}{
		"duplicate allocation": {
			err:   pluginErr("failed to allocate for range 0: 10.88.0.5 has been allocated to c1, duplicate allocation is not allowed"),
			check: isDuplicateAllocationError,
			want:  true,
		},
		"veth exists": {
			err:   pluginErr(`failed to setup veth: container veth name "eth0" peer name "veth1" already exists`),
			check: isVethExistsError,
			want:  true,
		},
		"bridge mac": {
			err:   pluginErr(`failed to set bridge addr: could not set bridge's mac: invalid argument`),
			check: isBridgeMACError,
			want:  true,
		},
		"already exists outside the plugin": {
			err:   fmt.Errorf("write cni cache: %w", errors.New("file already exists")),
			check: isVethExistsError,
			want:  false,
		},
		"duplicate allocation outside the plugin": {
			err:   errors.New("duplicate allocation"),
			check: isDuplicateAllocationError,
			want:  false,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := tt.check(tt.err); got != tt.want {
				t.Fatalf("check(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
