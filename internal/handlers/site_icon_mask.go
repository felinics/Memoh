package handlers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Some sites ship one single-color SVG favicon and swap in a variant for the
// other color scheme only from page script, which discovery never runs. On the
// scheme where that color has too little contrast the icon disappears, so the
// SVG is returned inline and the client paints its shape in the link color.
// Icons that are multicolor, raster, or already legible are left untouched.
const (
	// Masks are kept in the icon cache, so only small glyph-like SVGs qualify.
	siteIconMaskMaxBytes = 8 << 10
	// WCAG 1.4.11 minimum for non-text graphics. The server does not know the
	// client palette, so light and dark schemes are approximated as white and
	// black backgrounds.
	siteIconMinContrast = 3.0
)

// addMasks sets the mask for each scheme whose icon is an illegible one-color
// SVG. It reports whether an SVG fetch failed transiently, so the caller can
// retry sooner instead of caching the missing mask for a day.
func (h *SiteIconHandler) addMasks(ctx context.Context, icons *SiteIconResponse) (transient bool) {
	type svg struct {
		mask      string
		luminance float64
		ok        bool
	}
	seen := map[string]svg{}
	for _, scheme := range []struct {
		src        string
		mask       *string
		background float64
	}{
		{icons.Light, &icons.LightMask, 1},
		{icons.Dark, &icons.DarkMask, 0},
	} {
		u, err := url.Parse(scheme.src)
		// Only paths that name an SVG are fetched; probing every favicon.ico
		// would double the outbound requests for icons that never qualify.
		if err != nil || !strings.HasSuffix(strings.ToLower(u.Path), ".svg") {
			continue
		}
		result, ok := seen[scheme.src]
		if !ok {
			body, err := h.fetchSVG(ctx, scheme.src)
			if errors.Is(err, errSiteIconTransient) {
				transient = true
			}
			if luminance, single := svgSingleColorLuminance(body); err == nil && single {
				result = svg{mask: "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString(body), luminance: luminance, ok: true}
			}
			seen[scheme.src] = result
		}
		if result.ok && contrastRatio(result.luminance, scheme.background) < siteIconMinContrast {
			*scheme.mask = result.mask
		}
	}
	return transient
}

var errSiteIconTransient = errors.New("transient icon fetch failure")

func (h *SiteIconHandler) fetchSVG(ctx context.Context, src string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, src, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "image/svg+xml")
	response, err := h.client.Do(req) //nolint:gosec // dialIconHost prevents SSRF to private networks.
	if err != nil {
		return nil, errSiteIconTransient
	}
	defer func() { _ = response.Body.Close() }()
	switch {
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError:
		return nil, errSiteIconTransient
	case response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "svg"):
		return nil, errors.New("not an svg icon")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, siteIconMaskMaxBytes+1))
	if err != nil {
		return nil, errSiteIconTransient
	}
	if len(body) > siteIconMaskMaxBytes {
		return nil, errors.New("svg icon too large")
	}
	return body, nil
}

// Elements whose rendering cannot be judged from presentation attributes:
// stylesheets may hold their own color-scheme rules, and embedded images or
// HTML carry colors of their own.
var svgOpaqueElements = map[string]bool{"style": true, "image": true, "foreignObject": true, "script": true}

// Elements painted with their effective fill, which defaults to black.
var svgFilledElements = map[string]bool{"path": true, "rect": true, "circle": true, "ellipse": true, "polygon": true, "polyline": true, "text": true, "use": true}

// svgSingleColorLuminance reports the relative luminance of the one color an
// SVG is painted with. Anything it cannot evaluate with certainty, such as
// stylesheets or unknown color syntax, counts as not single-color, so such
// icons keep rendering as they are.
func svgSingleColorLuminance(body []byte) (float64, bool) {
	if len(body) == 0 {
		return 0, false
	}
	decoder := xml.NewDecoder(bytes.NewReader(body))
	var color string
	record := func(value string) bool {
		switch value {
		case "none", "transparent":
			return true
		case "", "currentcolor":
			// An SVG loaded as an image paints unset fill and currentColor black.
			value = "#000"
		}
		if strings.HasPrefix(value, "url(") {
			return true // gradient stops are recorded where they are declared
		}
		if color == "" {
			color = value
		}
		return value == color
	}
	fills := []string{""}
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return 0, false
		}
		switch t := token.(type) {
		case xml.StartElement:
			if svgOpaqueElements[t.Name.Local] {
				return 0, false
			}
			properties := svgPresentation(t.Attr)
			fill, set := properties["fill"]
			if !set {
				fill = fills[len(fills)-1]
			}
			fills = append(fills, fill)
			if svgFilledElements[t.Name.Local] && !record(fill) {
				return 0, false
			}
			for _, name := range []string{"stroke", "stop-color"} {
				if value, set := properties[name]; set && !record(value) {
					return 0, false
				}
			}
		case xml.EndElement:
			fills = fills[:len(fills)-1]
		}
	}
	if color == "" {
		return 0, false
	}
	r, g, b, ok := parseSVGColor(color)
	if !ok {
		return 0, false
	}
	return relativeLuminance(r, g, b), true
}

// svgPresentation merges color presentation attributes with the inline style
// attribute, which takes precedence in SVG.
func svgPresentation(attrs []xml.Attr) map[string]string {
	properties := map[string]string{}
	for _, a := range attrs {
		switch a.Name.Local {
		case "fill", "stroke", "stop-color":
			properties[a.Name.Local] = strings.ToLower(strings.TrimSpace(a.Value))
		}
	}
	for _, a := range attrs {
		if a.Name.Local != "style" {
			continue
		}
		for _, declaration := range strings.Split(a.Value, ";") {
			name, value, found := strings.Cut(declaration, ":")
			name = strings.ToLower(strings.TrimSpace(name))
			if found && (name == "fill" || name == "stroke" || name == "stop-color") {
				properties[name] = strings.ToLower(strings.TrimSpace(value))
			}
		}
	}
	return properties
}

// parseSVGColor accepts hex and rgb() colors plus black and white; other
// syntax is reported as unknown rather than guessed.
func parseSVGColor(value string) (r, g, b float64, ok bool) {
	switch value {
	case "black":
		return 0, 0, 0, true
	case "white":
		return 255, 255, 255, true
	}
	if hex, found := strings.CutPrefix(value, "#"); found {
		switch len(hex) {
		case 3, 4:
			hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
		case 6, 8:
			hex = hex[:6]
		default:
			return 0, 0, 0, false
		}
		n, err := strconv.ParseUint(hex, 16, 32)
		if err != nil {
			return 0, 0, 0, false
		}
		return float64(n >> 16 & 0xff), float64(n >> 8 & 0xff), float64(n & 0xff), true
	}
	for _, prefix := range []string{"rgb(", "rgba("} {
		inner, found := strings.CutPrefix(value, prefix)
		if !found {
			continue
		}
		parts := strings.FieldsFunc(strings.TrimSuffix(inner, ")"), func(c rune) bool { return c == ',' || c == ' ' || c == '/' })
		if len(parts) < 3 {
			return 0, 0, 0, false
		}
		channels := make([]float64, 0, 3)
		for _, part := range parts[:3] {
			v, err := strconv.ParseFloat(part, 64)
			if err != nil || v < 0 || v > 255 {
				return 0, 0, 0, false
			}
			channels = append(channels, v)
		}
		return channels[0], channels[1], channels[2], true
	}
	return 0, 0, 0, false
}

// relativeLuminance follows the WCAG definition for sRGB channels in 0-255.
func relativeLuminance(r, g, b float64) float64 {
	linear := func(c float64) float64 {
		c /= 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}

func contrastRatio(a, b float64) float64 {
	return (max(a, b) + 0.05) / (min(a, b) + 0.05)
}
