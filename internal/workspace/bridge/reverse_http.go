package bridge

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"

	"google.golang.org/grpc/metadata"

	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

const reverseHTTPRouteMetadata = "x-memoh-reverse-http-route"

func (c *Client) ServeReverseHTTP(ctx context.Context, handler http.Handler) (func(), error) {
	return c.ServeReverseHTTPRoute(ctx, "", handler)
}

func (c *Client) ServeReverseHTTPRoute(ctx context.Context, route string, handler http.Handler) (func(), error) {
	if c == nil || c.svc == nil {
		return nil, errors.New("workspace bridge client is required")
	}
	if handler == nil {
		return nil, errors.New("reverse HTTP handler is required")
	}

	streamCtx, cancel := context.WithCancel(ctx)
	if route = normalizeReverseHTTPRoute(route); route != "" {
		streamCtx = metadata.AppendToOutgoingContext(streamCtx, reverseHTTPRouteMetadata, route)
	}
	stream, err := c.svc.ReverseHTTP(streamCtx)
	if err != nil {
		cancel()
		return nil, mapError(err)
	}

	var sendMu sync.Mutex
	sendFrame := func(frame *pb.ReverseHTTPFrame) error {
		if err := streamCtx.Err(); err != nil {
			return err
		}
		sendMu.Lock()
		defer sendMu.Unlock()
		if err := streamCtx.Err(); err != nil {
			return err
		}
		return stream.Send(frame)
	}

	var requests sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			frame, err := stream.Recv()
			if err != nil {
				return
			}
			request := frame.GetRequest()
			if request == nil {
				continue
			}
			requests.Add(1)
			go func() {
				defer requests.Done()
				serveReverseHTTPRequest(streamCtx, handler, sendFrame, request)
			}()
		}
	}()

	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			_ = stream.CloseSend()
			<-done
			requests.Wait()
		})
	}
	return stop, nil
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

func serveReverseHTTPRequest(ctx context.Context, handler http.Handler, sendFrame func(*pb.ReverseHTTPFrame) error, request *pb.ReverseHTTPRequest) {
	response, err := handleReverseHTTPRequest(ctx, handler, request)
	if err != nil {
		_ = sendFrame(&pb.ReverseHTTPFrame{
			Frame: &pb.ReverseHTTPFrame_Error{
				Error: &pb.ReverseHTTPError{
					Id:    request.GetId(),
					Error: err.Error(),
				},
			},
		})
		return
	}
	_ = sendFrame(&pb.ReverseHTTPFrame{
		Frame: &pb.ReverseHTTPFrame_Response{Response: response},
	})
}

func handleReverseHTTPRequest(ctx context.Context, handler http.Handler, request *pb.ReverseHTTPRequest) (*pb.ReverseHTTPResponse, error) {
	method := strings.TrimSpace(request.GetMethod())
	if method == "" {
		method = http.MethodGet
	}
	target := strings.TrimSpace(request.GetUrl())
	if target == "" {
		target = "/"
	}
	if !strings.HasPrefix(target, "/") {
		target = "/" + target
	}
	// Echo rejects synthetic host names in some hosted-server paths.
	httpReq, err := http.NewRequestWithContext(ctx, method, "http://127.0.0.1"+target, bytes.NewReader(request.GetBody()))
	if err != nil {
		return nil, err
	}
	copyReverseHTTPHeaders(httpReq.Header, request.GetHeaders())

	recorder := &reverseHTTPResponseRecorder{
		header: http.Header{},
	}
	handler.ServeHTTP(recorder, httpReq)
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return &pb.ReverseHTTPResponse{
		Id:         request.GetId(),
		StatusCode: int32(recorder.status), //nolint:gosec // status is constrained to HTTP status code range.
		Headers:    reverseHTTPHeadersToProto(recorder.header),
		Body:       recorder.body.Bytes(),
	}, nil
}

type reverseHTTPResponseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *reverseHTTPResponseRecorder) Header() http.Header {
	return r.header
}

func (r *reverseHTTPResponseRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(data)
}

func (r *reverseHTTPResponseRecorder) WriteHeader(statusCode int) {
	if r.status != 0 {
		return
	}
	r.status = statusCode
}

func copyReverseHTTPHeaders(dst http.Header, headers []*pb.HTTPHeader) {
	for _, header := range headers {
		name := strings.TrimSpace(header.GetName())
		if name == "" {
			continue
		}
		for _, value := range header.GetValues() {
			dst.Add(name, value)
		}
	}
}

func reverseHTTPHeadersToProto(header http.Header) []*pb.HTTPHeader {
	out := make([]*pb.HTTPHeader, 0, len(header))
	for key, values := range header {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		out = append(out, &pb.HTTPHeader{
			Name:   name,
			Values: append([]string(nil), values...),
		})
	}
	return out
}
