package markdownmedia

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/media"
)

const MetadataKey = "markdown_media"

// ErrPartialDelivery means prose was published, but one or more media references failed.
var ErrPartialDelivery = errors.New("media.partial_delivery")

type Binding struct {
	Reference
	Asset     media.Asset `json:"asset"`
	ErrorCode string      `json:"error_code,omitempty"`
}

type Store interface {
	IngestWorkspaceFile(context.Context, string, string) (media.Asset, error)
	Ingest(context.Context, media.IngestInput) (media.Asset, error)
	Open(context.Context, string, string) (io.ReadCloser, media.Asset, error)
}

type Resolver func(context.Context, string, string) []Binding

// Resolve snapshots explicit media before publication. Failures are per-reference
// and carry only stable public codes; storage errors never become message text.
func Resolve(ctx context.Context, store Store, botID, source string) []Binding {
	refs := Parse(source)
	bindings := make([]Binding, 0, len(refs))
	cache := map[string]Binding{}
	for _, ref := range refs {
		cacheKey := strconv.FormatBool(ref.Image) + ":" + ref.Target
		if cached, ok := cache[cacheKey]; ok {
			cached.Reference = ref
			bindings = append(bindings, cached)
			continue
		}
		b := Binding{Reference: ref}
		asset, err := archive(ctx, store, botID, ref.Target)
		if err == nil && ref.Image {
			var reader io.ReadCloser
			reader, _, err = store.Open(ctx, botID, asset.ContentHash)
			if err == nil {
				prefix := make([]byte, 512)
				n, readErr := io.ReadFull(reader, prefix)
				_ = reader.Close()
				if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
					err = readErr
				}
				detected := http.DetectContentType(prefix[:n])
				if !strings.HasPrefix(detected, "image/") {
					err = errors.New("invalid image")
				} else {
					asset.Mime = detected
				}
			}
		}
		if err != nil {
			b.ErrorCode = "media.reference_unavailable"
		} else {
			b.Asset = asset
		}
		cache[cacheKey] = b
		bindings = append(bindings, b)
	}
	return bindings
}

func archive(ctx context.Context, store Store, botID, target string) (media.Asset, error) {
	if store == nil {
		return media.Asset{}, errors.New("media store unavailable")
	}
	if strings.HasPrefix(target, "/") {
		clean := path.Clean(target)
		if !strings.HasPrefix(clean, "/data/") {
			return media.Asset{}, errors.New("outside workspace")
		}
		return store.IngestWorkspaceFile(ctx, botID, clean)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return media.Asset{}, err
	}
	if req.URL.User != nil {
		return media.Asset{}, errors.New("URL credentials are not supported")
	}
	res, err := downloadClient.Do(req) //nolint:gosec // G704: every dial validates the resolved IP; proxies are disabled and redirect schemes are checked.
	if err != nil {
		return media.Asset{}, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return media.Asset{}, errors.New("media download failed")
	}
	return store.Ingest(ctx, media.IngestInput{BotID: botID, Mime: res.Header.Get("Content-Type"), Reader: res.Body, OriginalExt: path.Ext(req.URL.Path)})
}

// Disable proxies so DNS/IP validation applies to the actual destination,
// including redirect hops, rather than merely to a configured proxy host.
var downloadClient = &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{
	DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, candidate := range ips {
			ip := candidate.IP
			if !publicMediaIP(ip) {
				continue
			}
			return (&net.Dialer{}).DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
		return nil, errors.New("media URL resolves to a restricted address")
	},
}, CheckRedirect: func(req *http.Request, via []*http.Request) error {
	if len(via) >= 5 || req.URL.User != nil || (req.URL.Scheme != "https" && req.URL.Scheme != "http") {
		return errors.New("unsupported media redirect")
	}
	return nil
}}

func Bindings(metadata map[string]any) []Binding {
	if metadata == nil {
		return nil
	}
	if bindings, ok := metadata[MetadataKey].([]Binding); ok {
		return bindings
	}
	data, err := json.Marshal(metadata[MetadataKey])
	if err != nil {
		return nil
	}
	var result []Binding
	_ = json.Unmarshal(data, &result)
	return result
}

// Render replaces only validated source ranges, preserving all other Markdown.
func Render(source string, bindings []Binding, replace func(Binding) string) string {
	var out strings.Builder
	Visit(source, bindings, func(value string) { out.WriteString(value) }, func(b Binding) { out.WriteString(replace(b)) })
	return out.String()
}

func AssetURL(b Binding) string {
	return "/bots/" + url.PathEscape(b.Asset.BotID) + "/media/" + url.PathEscape(b.Asset.ContentHash)
}

// Filename excludes URL credentials and query parameters from attachment names.
func Filename(target string) string {
	if parsed, err := url.Parse(target); err == nil && parsed.Host != "" {
		return path.Base(parsed.Path)
	}
	return path.Base(target)
}

func publicMediaIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	// Shared carrier networks, protocol assignments, documentation and benchmark
	// ranges are not public download destinations, despite being unicast.
	for _, prefix := range restrictedMediaPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return address.Is4() || netip.MustParsePrefix("2000::/3").Contains(address)
}

var restrictedMediaPrefixes = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"), netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"), netip.MustParsePrefix("2001::/32"),
	netip.MustParsePrefix("2001:db8::/32"),
}
