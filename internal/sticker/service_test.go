package sticker

import (
	"context"
	"net"
	"path"
	"strconv"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// workspaceFS is an in-memory stand-in for a bot workspace that reproduces the
// bridge's actual path contract: ListDir reports names RELATIVE to the
// directory it listed, while writes and rooted reads take absolute paths.
// That asymmetry is the whole reason this test exists — a fake that echoed
// absolute paths back would have happily agreed with a service that reads from
// the wrong directory.
type workspaceFS struct {
	pb.UnimplementedContainerServiceServer
	files map[string][]byte
	// readRoots records the root of every rooted read, so a test can assert the
	// library never reads outside itself.
	readRoots []string
}

func newWorkspaceFS() *workspaceFS {
	return &workspaceFS{files: map[string][]byte{}}
}

func (w *workspaceFS) WriteFile(_ context.Context, req *pb.WriteFileRequest) (*pb.WriteFileResponse, error) {
	w.files[req.GetPath()] = append([]byte(nil), req.GetContent()...)
	return &pb.WriteFileResponse{}, nil
}

func (w *workspaceFS) ListDir(_ context.Context, req *pb.ListDirRequest) (*pb.ListDirResponse, error) {
	dir := strings.TrimSuffix(req.GetPath(), "/")
	entries := make([]*pb.FileEntry, 0, len(w.files))
	for full, content := range w.files {
		rel, ok := strings.CutPrefix(full, dir+"/")
		if !ok || (!req.GetRecursive() && strings.Contains(rel, "/")) {
			continue
		}
		entries = append(entries, &pb.FileEntry{Path: rel, Size: int64(len(content))})
	}
	if len(entries) == 0 {
		return nil, status.Error(codes.NotFound, "no such directory")
	}
	if limit := req.GetMaxEntries(); limit > 0 && len(entries) > int(limit) {
		return nil, status.Error(codes.ResourceExhausted, "too many entries")
	}
	return &pb.ListDirResponse{Entries: entries, TotalCount: int32(len(entries))}, nil //nolint:gosec // G115: test fixture holds a handful of entries
}

func (w *workspaceFS) ReadRawNoFollow(req *pb.ReadRawNoFollowRequest, stream grpc.ServerStreamingServer[pb.DataChunk]) error {
	w.readRoots = append(w.readRoots, req.GetRoot())
	content, ok := w.files[path.Join(req.GetRoot(), req.GetRelativePath())]
	if !ok {
		return status.Error(codes.NotFound, "no such file")
	}
	return stream.Send(&pb.DataChunk{Data: content})
}

type fixedProvider struct{ client *bridge.Client }

func (p fixedProvider) MCPClient(context.Context, string) (*bridge.Client, error) {
	return p.client, nil
}

func newLibraryService(t *testing.T, fs *workspaceFS) *Service {
	t.Helper()

	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	pb.RegisterContainerServiceServer(srv, fs)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = srv.Serve(lis)
	}()
	t.Cleanup(func() {
		srv.Stop()
		<-done
	})

	conn, err := grpc.NewClient(
		"passthrough://bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return New(nil, fixedProvider{client: bridge.NewClientFromConn(conn)})
}

// 保存成功之后必须搜得到。曾经不是这样:目录列表给的是相对名,直接拿去读会读到
// /data/<name>,读失败又被跳过,于是"库是空的"和"读错了地方"表现完全一样。
func TestServiceSaveThenSearchRoundTrips(t *testing.T) {
	t.Parallel()

	fs := newWorkspaceFS()
	service := newLibraryService(t, fs)
	ctx := context.Background()

	saved, err := service.Save(ctx, "bot-1", Entry{
		Ref:         "file-1",
		UniqueID:    "unique-1",
		Pack:        "猫猫日常",
		Emoji:       "😺",
		Kind:        KindStatic,
		Description: "猫猫开心地挥手",
	})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if dir := path.Dir(saved.Path); dir != stickerDirPath() {
		t.Fatalf("saved to %q, want a file under %q", saved.Path, stickerDirPath())
	}
	if _, ok := fs.files[saved.Path]; !ok {
		t.Fatalf("workspace has %v, want a file at %q", keys(fs.files), saved.Path)
	}

	found, err := service.Search(ctx, "bot-1", "猫猫", 0)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(found) != 1 {
		t.Fatalf("Search returned %d entries, want the one just saved", len(found))
	}
	if found[0].Ref != "file-1" || found[0].Description != "猫猫开心地挥手" {
		t.Fatalf("found = %+v", found[0])
	}
	if found[0].Path != saved.Path {
		t.Fatalf("entry path = %q, want %q", found[0].Path, saved.Path)
	}
	// 读取必须锚在库目录里,否则库里的一个软链就能把贴纸查询变成任意文件读取。
	for _, root := range fs.readRoots {
		if root != stickerDirPath() {
			t.Fatalf("read rooted at %q, want %q", root, stickerDirPath())
		}
	}
	if _, ok := fs.files[stickerOverviewPath()]; !ok {
		t.Fatalf("workspace has %v, want a rebuilt overview", keys(fs.files))
	}
}

// 同一张贴纸再次保存要合并进原文件,而不是长出第二个条目——这条依赖读得回来,
// 所以它同时是上面那个路径契约的回归。
func TestServiceSaveMergesExistingEntry(t *testing.T) {
	t.Parallel()

	fs := newWorkspaceFS()
	service := newLibraryService(t, fs)
	ctx := context.Background()

	first, err := service.Save(ctx, "bot-1", Entry{
		Ref: "old-file", UniqueID: "unique-1", Pack: "猫猫日常", Description: "猫猫挥手",
	})
	if err != nil {
		t.Fatalf("first Save: %v", err)
	}
	second, err := service.Save(ctx, "bot-1", Entry{
		Ref: "new-file", UniqueID: "unique-1", Pack: "猫猫日常", Description: "猫猫用力挥手",
	})
	if err != nil {
		t.Fatalf("second Save: %v", err)
	}
	if second.Path != first.Path {
		t.Fatalf("second save landed at %q, want the first entry's file %q", second.Path, first.Path)
	}
	entries, err := service.List(ctx, "bot-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("library has %d entries, want 1 merged entry", len(entries))
	}
	if entries[0].Ref != "new-file" || entries[0].Description != "猫猫用力挥手" {
		t.Fatalf("merged entry = %+v", entries[0])
	}
}

// 文件名一旦对身份做有损转换,两张不同的贴纸就会落到同一个文件上:查找时按完整
// 身份判定为新条目,写入时却把旧的覆盖掉。这两个 id 在旧实现下(小写、去符号、
// 截断十位)是同一个名字。
func TestServiceSaveDistinctStickersNeverShareAFile(t *testing.T) {
	t.Parallel()

	fs := newWorkspaceFS()
	service := newLibraryService(t, fs)
	ctx := context.Background()

	a, err := service.Save(ctx, "bot-1", Entry{Ref: "file-a", UniqueID: "AgAD-kQ0aA_x1", Description: "第一张"})
	if err != nil {
		t.Fatalf("Save a: %v", err)
	}
	b, err := service.Save(ctx, "bot-1", Entry{Ref: "file-b", UniqueID: "agadkq0_aax1", Description: "第二张"})
	if err != nil {
		t.Fatalf("Save b: %v", err)
	}
	if a.Path == b.Path {
		t.Fatalf("two stickers share %q", a.Path)
	}
	entries, err := service.List(ctx, "bot-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("library has %d entries, want both stickers", len(entries))
	}
}

// 工作区是 agent 和用户都能写的,所以库里可以出现任意大小的垃圾文件。单个超限
// 的条目跳过即可(它本来就读不成条目),整库超预算必须报错而不是少返回几条——
// 静默截断的库和空库在调用方看来没有区别。
func TestServiceListBoundsWorkspaceReads(t *testing.T) {
	t.Parallel()

	fs := newWorkspaceFS()
	service := newLibraryService(t, fs)
	ctx := context.Background()

	if _, err := service.Save(ctx, "bot-1", Entry{Ref: "file-1", UniqueID: "unique-1", Description: "正常条目"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fs.files[path.Join(stickerDirPath(), "huge.md")] = []byte(strings.Repeat("x", maxEntryBytes+1))

	entries, err := service.List(ctx, "bot-1")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 1 || entries[0].Ref != "file-1" {
		t.Fatalf("entries = %+v, want the oversized file skipped and the rest kept", entries)
	}

	for i := range maxLibraryFiles + 1 {
		fs.files[path.Join(stickerDirPath(), "pad-"+strconv.Itoa(i)+".md")] = []byte("x")
	}
	if _, err := service.List(ctx, "bot-1"); err == nil {
		t.Fatal("List accepted a library past the file bound")
	}
}

func TestEntryFileNameFromListing(t *testing.T) {
	t.Parallel()

	for listed, want := range map[string]string{
		"misc-abc123.md":                       "misc-abc123.md",
		"/data/stickers/misc-abc123.md":        "misc-abc123.md",
		"./misc-abc123.md":                     "misc-abc123.md",
		"/etc/passwd":                          "",
		"../../etc/passwd":                     "",
		"":                                     "",
		"   ":                                  "",
		"/data/stickers/nested/misc-abc123.md": "nested/misc-abc123.md",
	} {
		if got := entryFileNameFromListing(listed); got != want {
			t.Fatalf("entryFileNameFromListing(%q) = %q, want %q", listed, got, want)
		}
	}
}

func keys(files map[string][]byte) []string {
	out := make([]string, 0, len(files))
	for name := range files {
		out = append(out, name)
	}
	return out
}
