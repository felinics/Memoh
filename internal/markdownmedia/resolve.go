package markdownmedia

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
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

// EarlierMetadataKey carries the snapshots published by a run's earlier
// steps on its final channel message, in publication order.
const EarlierMetadataKey = "markdown_media_earlier"

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
}

type Resolver func(context.Context, string, string, Options) []Binding

// WorkspaceRoot is the only directory model-authored references may publish.
const WorkspaceRoot = "/data"

// MaxReferences bounds the distinct targets archived for one message. Later
// references fail individually instead of extending the publication.
const MaxReferences = 20

// Persistence publishes without the turn's cancellation, so one message's
// archive work, including remote downloads, has to stop on its own.
const resolveBudget = 60 * time.Second

// Remote images are previews, not file transfers.
const maxRemoteImageBytes int64 = 20 << 20

type Options struct {
	// Links also archives file links. Web and Desktop open linked workspace
	// files in place, so only IM delivery needs their contents.
	Links bool
}

var errReferenceLimit = errors.New("too many media references")

// Resolve snapshots explicit media before publication. Failures are per-reference
// and carry only stable public codes; storage errors never become message text.
func Resolve(ctx context.Context, store Store, botID, source string, opts Options) []Binding {
	refs := Parse(source)
	if len(refs) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, resolveBudget)
	defer cancel()
	bindings := make([]Binding, 0, len(refs))
	cache := map[string]Binding{}
	for _, ref := range refs {
		if !ref.Image && !opts.Links {
			continue
		}
		cacheKey := strconv.FormatBool(ref.Image) + ":" + ref.Target
		if cached, ok := cache[cacheKey]; ok {
			cached.Reference = ref
			bindings = append(bindings, cached)
			continue
		}
		b := Binding{Reference: ref}
		asset, err := media.Asset{}, errReferenceLimit
		if len(cache) < MaxReferences {
			asset, err = archive(ctx, store, botID, ref.Target, ref.Image)
		}
		// The archive records the sniffed type for images, so the stored
		// asset is served as the image it was validated to be.
		if err == nil && ref.Image && !IsImageMIME(asset.Mime) {
			err = errors.New("invalid image")
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

// IsImageMIME excludes SVG: it is active content when opened directly.
func IsImageMIME(mime string) bool {
	return strings.HasPrefix(mime, "image/") && mime != "image/svg+xml"
}

func archive(ctx context.Context, store Store, botID, target string, image bool) (media.Asset, error) {
	if store == nil {
		return media.Asset{}, errors.New("media store unavailable")
	}
	if strings.HasPrefix(target, "/") {
		clean := path.Clean(target)
		if !strings.HasPrefix(clean, WorkspaceRoot+"/") {
			return media.Asset{}, errors.New("outside workspace")
		}
		return store.IngestWorkspaceFile(ctx, botID, clean)
	}
	if !image {
		return media.Asset{}, errors.New("remote files are not published")
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
	// Trust the bytes, not the remote Content-Type or URL extension.
	body := bufio.NewReaderSize(res.Body, 512)
	prefix, _ := body.Peek(512)
	detected := http.DetectContentType(prefix)
	if !IsImageMIME(detected) {
		return media.Asset{}, errors.New("remote reference is not an image")
	}
	return store.Ingest(ctx, media.IngestInput{BotID: botID, Mime: detected, Reader: body, MaxBytes: maxRemoteImageBytes})
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

// Rebind binds references in text that was split off a published message to
// that message's snapshot. Matching consumes earlier bindings in order, so a
// path referenced again after its file changed gets the later snapshot. A
// reference without a snapshot fails rather than rereading the file.
func Rebind(text string, earlier *[]Binding) []Binding {
	refs := Parse(text)
	bindings := make([]Binding, 0, len(refs))
	for _, ref := range refs {
		b := Binding{Reference: ref, ErrorCode: "media.reference_unavailable"}
		for i, candidate := range *earlier {
			if candidate.Image == ref.Image && candidate.Target == ref.Target {
				b.Asset, b.ErrorCode = candidate.Asset, candidate.ErrorCode
				*earlier = append((*earlier)[:i:i], (*earlier)[i+1:]...)
				break
			}
		}
		bindings = append(bindings, b)
	}
	return bindings
}

// IsWorkspaceSurface reports platforms that render Markdown from history
// and open linked workspace files in place (Web, Desktop, CLI). Only other
// platforms archive linked files and receive published media as messages.
func IsWorkspaceSurface(platform string) bool {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "", "web", "cli", "local":
		return true
	}
	return false
}

// BindFinal binds a run's final reply to the snapshots its steps published,
// in publication order. The final reply is the run's last text, so its
// references take the latest matching snapshots, matched from the end; this
// does not depend on the reply text equalling any one persisted message. The
// snapshots left over belong to earlier steps and are returned in order.
func BindFinal(text string, published []Binding) (final []Binding, earlier []Binding) {
	refs := Parse(text)
	used := make([]bool, len(published))
	final = make([]Binding, len(refs))
	next := len(published)
	for i := len(refs) - 1; i >= 0; i-- {
		ref := refs[i]
		final[i] = Binding{Reference: ref, ErrorCode: "media.reference_unavailable"}
		for j := next - 1; j >= 0; j-- {
			if !used[j] && published[j].Image == ref.Image && published[j].Target == ref.Target {
				final[i].Asset, final[i].ErrorCode = published[j].Asset, published[j].ErrorCode
				used[j], next = true, j
				break
			}
		}
	}
	for j, b := range published {
		if !used[j] {
			earlier = append(earlier, b)
		}
	}
	return final, earlier
}
