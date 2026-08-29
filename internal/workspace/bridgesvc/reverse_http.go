package bridgesvc

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/felinics/memoh/internal/workspace/bridgepb"
)

const (
	reverseHTTPTimeout       = 2 * time.Minute
	reverseHTTPRouteMetadata = "x-memoh-reverse-http-route"
)

type ReverseHTTPBroker struct {
	nextID uint64

	mu      sync.Mutex
	sendMu  sync.Mutex
	streams map[string]bridgepb.ContainerService_ReverseHTTPServer
	pending map[string]reverseHTTPPending
}

type reverseHTTPPending struct {
	route string
	ch    chan *bridgepb.ReverseHTTPFrame
}

func NewReverseHTTPBroker() *ReverseHTTPBroker {
	return &ReverseHTTPBroker{
		streams: map[string]bridgepb.ContainerService_ReverseHTTPServer{},
		pending: map[string]reverseHTTPPending{},
	}
}

func (b *ReverseHTTPBroker) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if b == nil {
		http.Error(w, "Memoh tools proxy is not configured", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		http.Error(w, "read request body failed", http.StatusBadRequest)
		return
	}

	id := b.nextRequestID()
	route := reverseHTTPRouteFromRequest(req)
	responseCh := make(chan *bridgepb.ReverseHTTPFrame, 1)
	stream, err := b.registerPending(route, id, responseCh)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	defer b.unregisterPending(id)

	if err := b.sendFrame(stream, &bridgepb.ReverseHTTPFrame{
		Frame: &bridgepb.ReverseHTTPFrame_Request{
			Request: &bridgepb.ReverseHTTPRequest{
				Id:      id,
				Method:  req.Method,
				Url:     requestURI(req),
				Headers: httpHeaderToProto(req.Header),
				Body:    body,
			},
		},
	}); err != nil {
		http.Error(w, "forward Memoh tools request failed", http.StatusBadGateway)
		return
	}

	timer := time.NewTimer(reverseHTTPTimeout)
	defer timer.Stop()
	select {
	case frame := <-responseCh:
		if responseErr := frame.GetError(); responseErr != nil {
			http.Error(w, responseErr.GetError(), http.StatusBadGateway)
			return
		}
		response := frame.GetResponse()
		if response == nil {
			http.Error(w, "invalid Memoh tools response", http.StatusBadGateway)
			return
		}
		copyProtoHeaders(w.Header(), response.GetHeaders())
		statusCode := int(response.GetStatusCode())
		if statusCode < 100 || statusCode > 999 {
			statusCode = http.StatusOK
		}
		w.WriteHeader(statusCode)
		_, _ = w.Write(response.GetBody())
	case <-req.Context().Done():
		return
	case <-timer.C:
		http.Error(w, "Memoh tools request timed out", http.StatusGatewayTimeout)
	}
}

func (b *ReverseHTTPBroker) ServeReverseHTTP(stream bridgepb.ContainerService_ReverseHTTPServer) error {
	if b == nil {
		return status.Error(codes.Unavailable, "reverse HTTP broker is not configured")
	}
	route := reverseHTTPRouteFromContext(stream.Context())
	b.replaceStream(route, stream)
	defer b.clearStream(route, stream, "reverse HTTP stream closed")

	for {
		frame, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || stream.Context().Err() != nil {
				return nil
			}
			return err
		}
		if frame.GetResponse() == nil && frame.GetError() == nil {
			continue
		}
		b.deliver(frame)
	}
}

func (b *ReverseHTTPBroker) nextRequestID() string {
	id := atomic.AddUint64(&b.nextID, 1)
	return "reverse-http-" + strconv.FormatUint(id, 10)
}

func (b *ReverseHTTPBroker) registerPending(route, id string, ch chan *bridgepb.ReverseHTTPFrame) (bridgepb.ContainerService_ReverseHTTPServer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	stream := b.streams[route]
	if stream == nil && route != "" {
		stream = b.streams[""]
	}
	if stream == nil {
		return nil, errors.New("memoh tools proxy is not connected")
	}
	b.pending[id] = reverseHTTPPending{route: route, ch: ch}
	return stream, nil
}

func (b *ReverseHTTPBroker) unregisterPending(id string) {
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

func (b *ReverseHTTPBroker) replaceStream(route string, stream bridgepb.ContainerService_ReverseHTTPServer) {
	b.mu.Lock()
	b.failPendingLocked(route, "reverse HTTP stream replaced")
	b.streams[route] = stream
	b.mu.Unlock()
}

func (b *ReverseHTTPBroker) clearStream(route string, stream bridgepb.ContainerService_ReverseHTTPServer, message string) {
	b.mu.Lock()
	if b.streams[route] == stream {
		delete(b.streams, route)
		b.failPendingLocked(route, message)
	}
	b.mu.Unlock()
}

func (b *ReverseHTTPBroker) failPendingLocked(route, message string) {
	for id, pending := range b.pending {
		if pending.route != route {
			continue
		}
		delete(b.pending, id)
		pending.ch <- &bridgepb.ReverseHTTPFrame{
			Frame: &bridgepb.ReverseHTTPFrame_Error{
				Error: &bridgepb.ReverseHTTPError{
					Id:    id,
					Error: message,
				},
			},
		}
	}
}

func (b *ReverseHTTPBroker) deliver(frame *bridgepb.ReverseHTTPFrame) {
	id := ""
	if response := frame.GetResponse(); response != nil {
		id = response.GetId()
	} else if responseErr := frame.GetError(); responseErr != nil {
		id = responseErr.GetId()
	}
	if strings.TrimSpace(id) == "" {
		return
	}

	b.mu.Lock()
	pending := b.pending[id]
	if pending.ch != nil {
		delete(b.pending, id)
	}
	b.mu.Unlock()
	if pending.ch != nil {
		pending.ch <- frame
	}
}

func (b *ReverseHTTPBroker) sendFrame(stream bridgepb.ContainerService_ReverseHTTPServer, frame *bridgepb.ReverseHTTPFrame) error {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	return stream.Send(frame)
}

func requestURI(req *http.Request) string {
	if req == nil || req.URL == nil {
		return "/"
	}
	if uri := strings.TrimSpace(req.URL.RequestURI()); uri != "" {
		return uri
	}
	return "/"
}

func reverseHTTPRouteFromRequest(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	return normalizeReverseHTTPRoute(req.URL.Path)
}

func reverseHTTPRouteFromContext(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	values := md.Get(reverseHTTPRouteMetadata)
	if len(values) == 0 {
		return ""
	}
	return normalizeReverseHTTPRoute(values[0])
}

func normalizeReverseHTTPRoute(route string) string {
	route = strings.TrimSpace(route)
	if route == "" || route == "/" {
		return ""
	}
	if !strings.HasPrefix(route, "/") {
		route = "/" + route
	}
	return route
}

func httpHeaderToProto(header http.Header) []*bridgepb.HTTPHeader {
	out := make([]*bridgepb.HTTPHeader, 0, len(header))
	for key, values := range header {
		name := strings.TrimSpace(key)
		if name == "" || isHopByHopHeader(name) {
			continue
		}
		out = append(out, &bridgepb.HTTPHeader{
			Name:   name,
			Values: append([]string(nil), values...),
		})
	}
	return out
}

func copyProtoHeaders(dst http.Header, headers []*bridgepb.HTTPHeader) {
	for _, header := range headers {
		name := strings.TrimSpace(header.GetName())
		if name == "" || isHopByHopHeader(name) {
			continue
		}
		for _, value := range header.GetValues() {
			dst.Add(name, value)
		}
	}
}

func isHopByHopHeader(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-length":
		return true
	default:
		return false
	}
}
