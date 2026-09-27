package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"github.com/userreksai/title-agent/internal/protocol"
	"github.com/userreksai/title-agent/internal/settings"
)

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchErrorPageTitleEncodingRedirectAndLimits(t *testing.T) {
	gbk, _ := simplifiedchinese.GBK.NewEncoder().Bytes([]byte("<meta charset=gbk><title>中文标题</title>"))
	cases := []struct {
		name, body, ct, kind, title string
		status                      int
	}{
		{"normal", "<title>  标题 &amp; 123\n </title>", "text/html", "title", "标题 & 123", 200},
		{"500 title", "<title>500服务失败</title>", "text/html", "title", "500服务失败", 500},
		{"HTTP error without title", "failed", "text/html", "http_error", "", 503},
		{"empty", "<html>empty</html>", "text/html", "empty_title", "", 200},
		{"gbk", string(gbk), "text/html; charset=gbk", "title", "中文标题", 200},
		{"gbk meta", string(gbk), "text/html", "title", "中文标题", 200},
		{"huge title", "<title>" + strings.Repeat("a", 17000) + "</title>", "text/html", "too_large", "", 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ct)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer site.Close()
			cfg := settings.DefaultAgent()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			o := fetchURL(ctx, site.Client(), cfg, site.URL)
			if o.Kind != tc.kind || o.Title != tc.title || o.StatusCode != tc.status {
				t.Fatalf("got %+v", o)
			}
		})
	}
}

func TestTitleReturnsBeforeStalledBodyAndEnforcesDeadline(t *testing.T) {
	for _, withTitle := range []bool{true, false} {
		t.Run(map[bool]string{true: "title in head", false: "timeout"}[withTitle], func(t *testing.T) {
			site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				if withTitle {
					_, _ = io.WriteString(w, "<title>123</title>")
				} else {
					_, _ = io.WriteString(w, "<html>")
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
			}))
			defer site.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			start := time.Now()
			o := fetchURL(ctx, site.Client(), settings.DefaultAgent(), site.URL)
			if withTitle && (o.Title != "123" || time.Since(start) > 150*time.Millisecond) {
				t.Fatalf("waited for unused body: %+v %v", o, time.Since(start))
			}
			if !withTitle && (o.Kind != "timeout" || time.Since(start) > time.Second) {
				t.Fatalf("deadline failed: %+v", o)
			}
		})
	}
}

func TestHTTPSHTTPShareDeadlineAndDontHideErrorTitle(t *testing.T) {
	var calls int
	client := &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Scheme == "https" {
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 500, Header: http.Header{"Content-Type": []string{"text/html"}}, Body: io.NopCloser(strings.NewReader("<title>500服务失败</title>")), Request: r}, nil
	})}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	o := fetch(ctx, client, settings.DefaultAgent(), "example.com")
	if o.Title != "500服务失败" || o.StatusCode != 500 || calls != 2 || time.Since(start) > 350*time.Millisecond {
		t.Fatalf("fallback %+v calls=%d", o, calls)
	}
	calls = 0
	client.Transport = roundTrip(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 500, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<title>500服务失败</title>")), Request: r}, nil
	})
	o = fetch(ctx, client, settings.DefaultAgent(), "example.com")
	if calls != 1 || o.Title != "500服务失败" {
		t.Fatal("error page was hidden by fallback")
	}
}

func TestAuthCapacityAndProtocol(t *testing.T) {
	cfg := settings.DefaultAgent()
	cfg.Token = "test-token-1234567890"
	cfg.MaxConcurrent = 1
	s := New(cfg)
	s.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("<title>123</title>")), Request: r}, nil
	})}
	payload, _ := json.Marshal(protocol.Request{Version: 1, ID: "request-1", Domain: "example.com", TimeoutSeconds: 15})
	call := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/v1/title", bytes.NewReader(payload))
		req.Header.Set("Authorization", token)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, req)
		return w
	}
	if w := call(""); w.Code != 401 {
		t.Fatalf("auth=%d", w.Code)
	}
	w := call("Bearer " + cfg.Token)
	var o protocol.Observation
	_ = json.Unmarshal(w.Body.Bytes(), &o)
	if w.Code != 200 || o.ID != "request-1" || o.Domain != "example.com" || o.Title != "123" {
		t.Fatalf("protocol %d %+v", w.Code, o)
	}
	s.slots <- struct{}{}
	if w = call("Bearer " + cfg.Token); w.Code != 429 {
		t.Fatalf("capacity=%d", w.Code)
	}
	<-s.slots
	payload, _ = json.Marshal(protocol.Request{Version: 1, ID: "x", Domain: "example.com", TimeoutSeconds: 16})
	if w = call("Bearer " + cfg.Token); w.Code != 400 {
		t.Fatal("timeout above 15 seconds accepted")
	}
}

func TestHealthProbesAndPrivateTargetProtection(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.100.100.200", "::1", "::ffff:127.0.0.1", "fc00::1", "0.0.0.0"} {
		if publicIP(net.ParseIP(ip)) {
			t.Errorf("allowed private/reserved IP %s", ip)
		}
	}
	cfg := settings.DefaultAgent()
	cfg.Token = "test-token-1234567890"
	cfg.HealthProbeURLs = []string{"https://probe.example/"}
	s := New(cfg)
	s.client = &http.Client{Transport: roundTrip(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("failed")), Request: r}, nil
	})}
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	r.Header.Set("Authorization", "Bearer "+cfg.Token)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	var h protocol.Health
	_ = json.Unmarshal(w.Body.Bytes(), &h)
	if w.Code != 503 || h.Ready {
		t.Fatalf("bad network remained healthy: %+v", h)
	}
	client := newHTTPClient(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1/", nil)
	resp, err := client.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("private target allowed")
	}
}
