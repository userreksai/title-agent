package agent

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/userreksai/title-agent/internal/protocol"
	"github.com/userreksai/title-agent/internal/settings"
)

type Server struct {
	cfg      settings.Agent
	client   *http.Client
	slots    chan struct{}
	healthMu sync.Mutex
	health   protocol.Health
	healthAt time.Time
}

func New(cfg settings.Agent) *Server {
	return &Server{cfg: cfg, client: newHTTPClient(cfg), slots: make(chan struct{}, cfg.MaxConcurrent)}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("POST /v1/title", s.title)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.cfg.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) title(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	var req protocol.Request
	if d.Decode(&req) != nil || d.Decode(new(any)) != io.EOF || req.Version != protocol.Version || req.ID == "" || len(req.ID) > 128 || req.TimeoutSeconds < 1 || req.TimeoutSeconds > s.cfg.MaxTimeoutSeconds {
		http.Error(w, "invalid request or timeout", http.StatusBadRequest)
		return
	}
	if _, err := targets(req.Domain); err != nil {
		http.Error(w, "invalid domain", http.StatusBadRequest)
		return
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	default:
		http.Error(w, "agent capacity exceeded", http.StatusTooManyRequests)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(req.TimeoutSeconds)*time.Second)
	defer cancel()
	o := fetch(ctx, s.client, s.cfg, req.Domain)
	o.Version = protocol.Version
	o.ID = req.ID
	o.Domain = req.Domain
	write(w, http.StatusOK, o)
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	s.healthMu.Lock()
	defer s.healthMu.Unlock()
	if r.URL.Query().Get("fresh") == "1" || time.Since(s.healthAt) > 2*time.Second {
		h := protocol.Health{Version: protocol.Version, Name: s.cfg.Name, Ready: true, MaxConcurrent: s.cfg.MaxConcurrent, MaxTimeoutSeconds: s.cfg.MaxTimeoutSeconds}
		if len(s.cfg.HealthProbeURLs) > 0 {
			ctx, cancel := context.WithTimeout(r.Context(), time.Duration(s.cfg.HealthProbeTimeoutSeconds)*time.Second)
			results := make(chan bool, len(s.cfg.HealthProbeURLs))
			for _, u := range s.cfg.HealthProbeURLs {
				go func(target string) {
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
					if err != nil {
						results <- false
						return
					}
					resp, err := s.client.Do(req)
					if err != nil {
						results <- false
						return
					}
					resp.Body.Close()
					results <- resp.StatusCode >= 200 && resp.StatusCode < 400
				}(u)
			}
			h.Ready = false
			for range s.cfg.HealthProbeURLs {
				if <-results {
					h.Ready = true
					cancel()
					break
				}
			}
			cancel()
			if !h.Ready {
				h.Detail = "all network health probes failed"
			}
		}
		s.health = h
		s.healthAt = time.Now()
	}
	status := http.StatusOK
	if !s.health.Ready {
		status = http.StatusServiceUnavailable
	}
	write(w, status, s.health)
}
