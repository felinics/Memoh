package execution

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
)

var testPNGBytes = []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}

func TestGenerateDashScopeImageUsesResolvedCredentialAndDownloadsImage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/services/aigc/image-generation/generation":
			if got := request.Header.Get("Authorization"); got != "Bearer dashscope-key" {
				t.Fatalf("authorization = %q, want bearer key", got)
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["model"] != "wan2.7-image-pro" {
				t.Fatalf("model = %v", body["model"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":{"task_id":"task-1","task_status":"SUCCEEDED","choices":[{"message":{"content":[{"image":"` + publicTestURL("/image.png") + `","type":"image"}]}}]}}`))
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNGBytes)
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)

	config := imageModelConfig{
		modelID:    "wan2.7-image-pro",
		clientType: string(modeldomain.ClientTypeOpenAICompletions),
		apiKey:     "dashscope-key",
		baseURL:    server.URL + "/compatible-mode/v1",
	}
	image, err := generateDashScopeImage(context.Background(), imageTestClient(t, server), config, "a red cube", "1024x1024")
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if string(image.Data) != string(testPNGBytes) || image.MediaType != "image/png" {
		t.Fatalf("image = %+v, want PNG bytes", image)
	}
}

func TestGenerateDashScopeQwenImageUsesProviderDefaultSize(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v1/services/aigc/text2image/image-synthesis":
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			parameters := body["parameters"].(map[string]any)
			if _, ok := parameters["size"]; ok {
				t.Fatalf("unspecified size was sent: %v", parameters["size"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"output":{"task_status":"SUCCEEDED","results":[{"url":"` + publicTestURL("/qwen.png") + `"}]}}`))
		case "/qwen.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNGBytes)
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	image, err := generateDashScopeImage(context.Background(), imageTestClient(t, server), imageModelConfig{
		modelID: "qwen-image-plus", apiKey: "dashscope-key", baseURL: server.URL + "/compatible-mode/v1",
	}, "a red cube", "")
	if err != nil || string(image.Data) != string(testPNGBytes) {
		t.Fatalf("image = %+v error = %v", image, err)
	}
}

func TestGenerateOpenAIImageUsesImagesEndpoint(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/api/v3/images/generations":
			if got := request.Header.Get("Authorization"); got != "Bearer openai-key" {
				t.Fatalf("authorization = %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			if body["model"] != "gpt-image-1" || body["prompt"] != "a blue sphere" || body["size"] != "1024x1024" {
				t.Fatalf("request body = %v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"created":1,"data":[{"url":"` + publicTestURL("/openai.png") + `"}]}`))
		case "/openai.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(testPNGBytes)
		default:
			t.Fatalf("unexpected path: %s", request.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	image, err := generateOpenAIImagesImage(context.Background(), imageTestClient(t, server), imageModelConfig{
		modelID: "gpt-image-1", apiKey: "openai-key", baseURL: server.URL + "/api/v3",
	}, "a blue sphere", "1024x1024")
	if err != nil || string(image.Data) != string(testPNGBytes) || image.MediaType != "image/png" {
		t.Fatalf("image = %+v error = %v", image, err)
	}
}

func TestImageResultDecodesBase64AndRejectsNonImageDownload(t *testing.T) {
	image, err := imageResultToGeneratedImage(context.Background(), http.DefaultClient, &sdk.ImageResult{
		Data: []sdk.ImageData{{B64JSON: base64.StdEncoding.EncodeToString(testPNGBytes)}},
	})
	if err != nil || string(image.Data) != string(testPNGBytes) || image.MediaType != "image/png" {
		t.Fatalf("image = %+v error = %v", image, err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte(`{"not":"an image"}`))
	}))
	t.Cleanup(server.Close)
	_, err = imageResultToGeneratedImage(context.Background(), imageTestClient(t, server), &sdk.ImageResult{
		Data: []sdk.ImageData{{URL: publicTestURL("/")}},
	})
	if err == nil || !strings.Contains(err.Error(), "not an image") {
		t.Fatalf("error = %v, want non-image rejection", err)
	}
}

func TestImageDownloadRejectsRestrictedTargetsAndBoundsErrorBody(t *testing.T) {
	for _, rawURL := range []string{"file:///tmp/image.png", "http://127.0.0.1/image.png", "http://169.254.169.254/image.png"} {
		_, err := imageResultToGeneratedImage(context.Background(), http.DefaultClient, &sdk.ImageResult{
			Data: []sdk.ImageData{{URL: rawURL}},
		})
		if err == nil {
			t.Fatalf("URL %q was not rejected", rawURL)
		}
	}
	redirectServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, "http://127.0.0.1/image.png", http.StatusFound)
	}))
	t.Cleanup(redirectServer.Close)
	_, err := imageResultToGeneratedImage(context.Background(), imageTestClient(t, redirectServer), &sdk.ImageResult{
		Data: []sdk.ImageData{{URL: publicTestURL("/")}},
	})
	if err == nil || !strings.Contains(err.Error(), "restricted address") {
		t.Fatalf("redirect error = %v, want restricted target rejection", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(strings.Repeat("x", 200_000)))
	}))
	t.Cleanup(server.Close)
	_, err = imageResultToGeneratedImage(context.Background(), imageTestClient(t, server), &sdk.ImageResult{
		Data: []sdk.ImageData{{URL: publicTestURL("/")}},
	})
	if err == nil || len(err.Error()) > maxImageErrorBodyBytes+200 || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("error = %v, want bounded response body", err)
	}
	emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
	}))
	t.Cleanup(emptyServer.Close)
	_, err = imageResultToGeneratedImage(context.Background(), imageTestClient(t, emptyServer), &sdk.ImageResult{
		Data: []sdk.ImageData{{URL: publicTestURL("/")}},
	})
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty response error = %v", err)
	}
}

func TestImageModelSanitizesProviderErrorsAndPreservesTextClassification(t *testing.T) {
	secret := "secret-1234567890"
	raw := errors.New("request failed with " + secret + " and " + secret[:8])
	clean := SanitizeError(raw, secret)
	if strings.Contains(clean.Error(), secret) || strings.Contains(clean.Error(), secret[:8]) {
		t.Fatalf("sanitized error leaked secret: %v", clean)
	}
	if !errors.Is(clean, raw) {
		t.Fatal("sanitized error must preserve the original cause")
	}

	textErr := &ImageTextResponseError{text: "please clarify"}
	wrapped := SanitizeError(fmt.Errorf("provider: %w", textErr), secret)
	var got *ImageTextResponseError
	if !errors.As(wrapped, &got) || got.Text() != "please clarify" {
		t.Fatalf("text classification lost: %v", wrapped)
	}
}

func TestImageGenerationRouting(t *testing.T) {
	t.Parallel()
	openAI := string(modeldomain.ClientTypeOpenAICompletions)
	if !shouldUseDashScopeImageGeneration(openAI, "https://dashscope.aliyuncs.com/compatible-mode/v1") {
		t.Fatal("dashscope URL should select native image generation")
	}
	if shouldUseDashScopeImageGeneration(openAI, "https://some-aggregator.example/v1") {
		t.Fatal("model names must not force DashScope routing on unknown providers")
	}
	if !shouldUseOpenAIImagesGeneration(openAI, "https://api.openai.com/v1") {
		t.Fatal("OpenAI URL should select the images endpoint")
	}
	if shouldUseOpenAIImagesGeneration(openAI, "https://openrouter.ai/api/v1") {
		t.Fatal("OpenRouter should use the chat fallback")
	}
}

func publicTestURL(path string) string { return "http://198.51.100.10" + path }

func imageTestClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}
	previous := imageConnectDialer
	imageConnectDialer = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, target.Host)
	}
	t.Cleanup(func() { imageConnectDialer = previous })
	return http.DefaultClient
}
