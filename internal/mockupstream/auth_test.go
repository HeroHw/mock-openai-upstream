package mockupstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newAuthServer(key string) *httptest.Server {
	cfg := defaults()
	cfg.APIKey = key
	return httptest.NewServer(NewServer(cfg).Handler())
}

func chatReq(t *testing.T, url string, setHeader func(*http.Request)) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url+"/v1/chat/completions",
		strings.NewReader(`{"model":"m","messages":[]}`))
	req.Header.Set("Content-Type", "application/json")
	if setHeader != nil {
		setHeader(req)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestFixedKeyRejectsMissing(t *testing.T) {
	ts := newAuthServer("secret-123")
	defer ts.Close()
	if code := chatReq(t, ts.URL, nil); code != http.StatusUnauthorized {
		t.Fatalf("missing credential should be 401, got %d", code)
	}
}

func TestFixedKeyRejectsWrong(t *testing.T) {
	ts := newAuthServer("secret-123")
	defer ts.Close()
	code := chatReq(t, ts.URL, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer wrong-key")
	})
	if code != http.StatusUnauthorized {
		t.Fatalf("wrong key should be 401, got %d", code)
	}
}

func TestFixedKeyAcceptsBearer(t *testing.T) {
	ts := newAuthServer("secret-123")
	defer ts.Close()
	code := chatReq(t, ts.URL, func(r *http.Request) {
		r.Header.Set("Authorization", "Bearer secret-123")
	})
	if code != http.StatusOK {
		t.Fatalf("correct Bearer key should be 200, got %d", code)
	}
}

func TestFixedKeyAcceptsXApiKeyAndQuery(t *testing.T) {
	ts := newAuthServer("secret-123")
	defer ts.Close()

	if code := chatReq(t, ts.URL, func(r *http.Request) {
		r.Header.Set("x-api-key", "secret-123")
	}); code != http.StatusOK {
		t.Fatalf("x-api-key should be accepted, got %d", code)
	}

	if code := chatReq(t, ts.URL, func(r *http.Request) {
		r.URL.RawQuery = "key=secret-123"
	}); code != http.StatusOK {
		t.Fatalf("?key= should be accepted, got %d", code)
	}
}

func TestFixedKeyAllowsHealthz(t *testing.T) {
	ts := newAuthServer("secret-123")
	defer ts.Close()
	// Internal endpoints bypass auth so compose healthchecks still work.
	resp, _ := mustGet(t, ts.URL+"/__mock/healthz")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz should bypass auth, got %d", resp.StatusCode)
	}
}

func TestNoAuthByDefault(t *testing.T) {
	ts := newTestServer() // no MOCK_API_KEY, no MOCK_REQUIRE_KEY
	defer ts.Close()
	if code := chatReq(t, ts.URL, nil); code != http.StatusOK {
		t.Fatalf("default should require no auth, got %d", code)
	}
}
