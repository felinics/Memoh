//go:build integration

package acceptance

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	enableEnv        = "MEMOH_SESSION_RUNTIME_ACCEPTANCE"
	requiredEnv      = "MEMOH_SESSION_RUNTIME_ACCEPTANCE_REQUIRED"
	crashEnv         = "MEMOH_SESSION_RUNTIME_ACCEPTANCE_CRASH"
	primaryURLEnv    = "MEMOH_SESSION_RUNTIME_PRIMARY_URL"
	secondaryURLEnv  = "MEMOH_SESSION_RUNTIME_SECONDARY_URL"
	usernameEnv      = "MEMOH_SESSION_RUNTIME_USERNAME"
	passwordEnv      = "MEMOH_SESSION_RUNTIME_PASSWORD" //nolint:gosec // environment variable name, not a credential
	containerEnv     = "MEMOH_SESSION_RUNTIME_PRIMARY_CONTAINER"
	fakeModelPortEnv = "MEMOH_SESSION_RUNTIME_FAKE_MODEL_PORT"
)

type acceptanceEnvironment struct {
	primaryURL       string
	secondaryURL     string
	username         string
	password         string
	primaryContainer string
}

type acceptanceFixture struct {
	api   *apiClient
	botID string
}

var (
	globalFakeModel  *fakeModel
	fixtureOnce      sync.Once
	globalFixture    acceptanceFixture
	globalFixtureErr error
	secondaryOnce    sync.Once
	secondaryErr     error
)

func TestMain(m *testing.M) {
	if !envBool(enableEnv) {
		os.Exit(m.Run())
	}

	model, err := startFakeModel()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start Session Runtime fake model: %v\n", err)
		os.Exit(2)
	}
	globalFakeModel = model
	code := m.Run()
	model.Close()
	os.Exit(code)
}

func requireFixture(t *testing.T, needsSecondary bool) acceptanceFixture {
	t.Helper()
	if !envBool(enableEnv) {
		t.Skipf("set %s=1 to run Session Runtime acceptance tests", enableEnv)
	}

	env := loadEnvironment()
	requireHealthy(t, env.primaryURL, "primary")
	if needsSecondary {
		requireHealthy(t, env.secondaryURL, "secondary")
	}
	if globalFakeModel == nil {
		t.Fatal("fake model was not started")
	}

	fixtureOnce.Do(func() {
		api := newAPIClient(env.primaryURL, env.username, env.password)
		botID, err := api.ensureFixture(globalFakeModel.ContainerBaseURL())
		if err != nil {
			globalFixtureErr = err
			return
		}
		globalFixture = acceptanceFixture{api: api, botID: botID}
	})
	if globalFixtureErr != nil {
		t.Fatalf("prepare acceptance fixture: %v", globalFixtureErr)
	}
	if needsSecondary {
		secondaryOnce.Do(func() {
			secondaryAPI := globalFixture.api.forBaseURL(env.secondaryURL)
			var response map[string]any
			secondaryErr = secondaryAPI.request(
				http.MethodPost,
				"/bots/"+globalFixture.botID+"/container/start",
				nil,
				&response,
				http.StatusOK,
			)
		})
		if secondaryErr != nil {
			t.Fatalf("prepare acceptance workspace on secondary Server: %v", secondaryErr)
		}
	}
	return globalFixture
}

func requireHealthy(t *testing.T, baseURL, label string) {
	t.Helper()
	client := &http.Client{Timeout: 2 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	request, requestErr := http.NewRequestWithContext(
		ctx,
		http.MethodHead,
		strings.TrimRight(baseURL, "/")+"/health",
		nil,
	)
	if requestErr != nil {
		t.Fatalf("build %s Server health request: %v", label, requestErr)
	}
	response, err := client.Do(request) //nolint:gosec // explicit acceptance-test Server URL
	if err == nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err == nil && response.StatusCode == http.StatusOK {
		return
	}

	message := fmt.Sprintf("%s Server is unavailable at %s", label, baseURL)
	if err != nil {
		message += ": " + err.Error()
	} else {
		message += fmt.Sprintf(": HTTP %d", response.StatusCode)
	}
	if envBool(requiredEnv) {
		t.Fatal(message)
	}
	t.Skip(message)
}

func loadEnvironment() acceptanceEnvironment {
	return acceptanceEnvironment{
		primaryURL:       envOr(primaryURLEnv, "http://127.0.0.1:18080"),
		secondaryURL:     envOr(secondaryURLEnv, "http://127.0.0.1:18083"),
		username:         envOr(usernameEnv, "admin"),
		password:         envOr(passwordEnv, "admin123"),
		primaryContainer: envOr(containerEnv, "memoh-dev-server"),
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes":
		return true
	default:
		return false
	}
}
