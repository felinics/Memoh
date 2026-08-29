package tools

import (
	"context"
	"net"
	"strings"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

type computerDisplayExecServer struct {
	pb.UnimplementedContainerServiceServer

	mu       sync.Mutex
	ready    bool
	commands []string
}

func (s *computerDisplayExecServer) Exec(stream pb.ContainerService_ExecServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}

	s.mu.Lock()
	command := req.GetCommand()
	s.commands = append(s.commands, command)
	exitCode := int32(0)
	if strings.Contains(command, "memoh-computer-display-ready-probe") {
		if !s.ready {
			exitCode = 1
		}
	} else if strings.Contains(command, "/opt/memoh/scripts/display-prepare.sh") {
		s.ready = true
	}
	s.mu.Unlock()

	return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: exitCode})
}

func newComputerDisplayTestClient(t *testing.T, server pb.ContainerServiceServer) *bridge.Client {
	t.Helper()

	listener := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer()
	pb.RegisterContainerServiceServer(grpcServer, server)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = grpcServer.Serve(listener)
	}()
	t.Cleanup(func() {
		grpcServer.Stop()
		<-done
	})

	dialer := func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}
	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return bridge.NewClientFromConn(conn)
}

func TestEnsureComputerDisplayHotStartsRuntime(t *testing.T) {
	server := &computerDisplayExecServer{}
	client := newComputerDisplayTestClient(t, server)
	provider := &BrowserProvider{
		containers: containerTestBridgeProvider{client: client},
	}

	if _, err := provider.ensureComputerDisplay(t.Context(), "bot-1"); err != nil {
		t.Fatalf("ensureComputerDisplay() error = %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.commands) != 3 {
		t.Fatalf("expected readiness probe, preparation, and verification; got %d commands", len(server.commands))
	}
	if !strings.Contains(server.commands[1], "/opt/memoh/scripts/display-prepare.sh") {
		t.Fatalf("expected the shared display preparation command, got: %s", server.commands[1])
	}
}

func TestBrowserKeyChordHelpers(t *testing.T) {
	parts := splitKeyChord("Control+Shift+a")
	if len(parts) != 3 || parts[0] != "Control" || parts[1] != "Shift" || parts[2] != "a" {
		t.Fatalf("unexpected chord parts: %#v", parts)
	}
	if got := namedKeysym("Enter"); got != 0xff0d {
		t.Fatalf("unexpected Enter keysym: %#x", got)
	}
	if got := namedKeysym("Control"); got != 0xffe3 {
		t.Fatalf("unexpected Control keysym: %#x", got)
	}
	if got := keysymForRune('你'); got != 0x01000000|uint32('你') {
		t.Fatalf("unexpected unicode keysym: %#x", got)
	}
}

func TestBrowserCDPKeyInfo(t *testing.T) {
	enter := keyInfoForCDP("Enter")
	if enter.Key != "Enter" || enter.KeyCode != 13 {
		t.Fatalf("unexpected Enter key info: %#v", enter)
	}
	letter := keyInfoForCDP("a")
	if letter.Key != "a" || letter.Code != "KeyA" || letter.KeyCode != int('A') || letter.Text != "a" {
		t.Fatalf("unexpected letter key info: %#v", letter)
	}
	if got := cdpModifier("Control") | cdpModifier("Shift"); got != 10 {
		t.Fatalf("unexpected modifier mask: %d", got)
	}
}

func TestBrowserScrollDeltas(t *testing.T) {
	if got := scrollDeltaY("down", 500); got != 500 {
		t.Fatalf("unexpected down delta: %d", got)
	}
	if got := scrollDeltaY("up", 500); got != -500 {
		t.Fatalf("unexpected up delta: %d", got)
	}
	if got := scrollDeltaX("left", 300); got != -300 {
		t.Fatalf("unexpected left delta: %d", got)
	}
	if got := scrollDeltaX("right", 300); got != 300 {
		t.Fatalf("unexpected right delta: %d", got)
	}
}

func TestBrowserActionAliases(t *testing.T) {
	if got := normalizeBrowserAction("dblclick"); got != "double_click" {
		t.Fatalf("unexpected dblclick alias: %q", got)
	}
	if got := normalizeBrowserAction("scrollintoview"); got != "scroll_into_view" {
		t.Fatalf("unexpected scrollintoview alias: %q", got)
	}
	if got := normalizeBrowserAction("fill"); got != "fill" {
		t.Fatalf("unexpected canonical action: %q", got)
	}
}

func TestBrowserRefHelpers(t *testing.T) {
	for _, input := range []string{"12", "e12", "E12", "ref=e12"} {
		if got := normalizeBrowserRef(input); got != "e12" {
			t.Fatalf("normalizeBrowserRef(%q) = %q", input, got)
		}
	}
	if _, err := browserRefIndex("e0"); err == nil {
		t.Fatal("expected invalid zero ref")
	}
	target := browserTargetArg(map[string]any{"ref": "12", "selector": "#fallback"}, "selector", "ref")
	if target.Ref != "e12" || target.Selector != "#fallback" {
		t.Fatalf("unexpected target: %#v", target)
	}
	result := target.withResult(map[string]any{"ok": true})
	if result["ref"] != "e12" || result["selector"] != "#fallback" {
		t.Fatalf("target metadata missing from result: %#v", result)
	}
}

func TestWrapRuntimeExpressionScopesHelper(t *testing.T) {
	wrapped := wrapRuntimeExpression("memohInteractiveElements().length")
	if !strings.HasPrefix(wrapped, "(async () => {") {
		t.Fatalf("expected async wrapper, got: %s", wrapped)
	}
	if !strings.Contains(wrapped, "const memohInteractiveSelector") {
		t.Fatalf("expected helper in wrapper: %s", wrapped)
	}
	if strings.Contains(wrapped, "eval(") {
		t.Fatalf("wrapper should not rely on eval: %s", wrapped)
	}
	if !strings.Contains(wrapped, "return await (\nmemohInteractiveElements().length\n);") {
		t.Fatalf("expected expression to be evaluated inside wrapper: %s", wrapped)
	}
}

func TestBrowserSchemasAreStrict(t *testing.T) {
	schema := browserObjectSchema(map[string]any{"action": map[string]any{"type": "string"}}, []string{"action"})
	if schema["additionalProperties"] != false {
		t.Fatalf("expected strict browser schema, got %#v", schema["additionalProperties"])
	}
	if required, ok := schema["required"].([]string); !ok || len(required) != 1 || required[0] != "action" {
		t.Fatalf("unexpected required fields: %#v", schema["required"])
	}
}

func TestBuildScreenshotResultDropsShareMetadata(t *testing.T) {
	p := &BrowserProvider{dataRoot: "/data"}
	result := p.buildScreenshotBytesResult(t.Context(), "", []byte("png-bytes"), "image/png", "/data/.memoh/screenshots", nil)
	asMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}
	if _, exists := asMap["shared"]; exists {
		t.Fatalf("expected shared field to be removed, got %#v", asMap)
	}
	content, ok := asMap["content"].([]map[string]any)
	if !ok || len(content) == 0 {
		t.Fatalf("expected text content, got %#v", asMap["content"])
	}
	text, _ := content[0]["text"].(string)
	if !strings.HasPrefix(text, "Screenshot saved to ") && !strings.HasPrefix(text, "Screenshot captured") {
		t.Fatalf("unexpected screenshot text: %q", text)
	}
}

func TestScreenshotDirUsesSharedMemohDirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		dataRoot string
		want     string
	}{
		{name: "default", want: "/data/.memoh/screenshots"},
		{name: "configured", dataRoot: "/workspace/data/", want: "/workspace/data/.memoh/screenshots"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &BrowserProvider{dataRoot: tt.dataRoot}
			if got := provider.screenshotDir(); got != tt.want {
				t.Fatalf("screenshotDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestComputerA11yShellQuote(t *testing.T) {
	if got := shellQuote("hello world"); got != "'hello world'" {
		t.Fatalf("unexpected quote: %q", got)
	}
	if got := shellQuote("it's a test"); got != `'it'\''s a test'` {
		t.Fatalf("unexpected escaped quote: %q", got)
	}
	if got := shellQuoteArgs([]string{"click", "--ref", "e3"}); got != "'click' '--ref' 'e3'" {
		t.Fatalf("unexpected quoted args: %q", got)
	}
}

func TestComputerRefFallbackPoint(t *testing.T) {
	item := a11ySnapshotItem{Ref: "e3", Center: &a11yPoint{X: 120, Y: 240}}
	item.CenterX = item.Center.X
	item.CenterY = item.Center.Y
	if item.Ref != "e3" {
		t.Fatalf("expected ref e3, got %q", item.Ref)
	}
	if item.CenterX != 120 || item.CenterY != 240 {
		t.Fatalf("expected center to propagate, got %d,%d", item.CenterX, item.CenterY)
	}
	if got := normalizeBrowserRef("E3"); got != "e3" {
		t.Fatalf("expected canonical ref e3, got %q", got)
	}
}
