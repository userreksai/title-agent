package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type Agent struct {
	Listen                    string   `json:"listen"`
	Name                      string   `json:"name"`
	Token                     string   `json:"token"`
	MaxConcurrent             int      `json:"max_concurrent"`
	MaxTimeoutSeconds         int      `json:"max_timeout_seconds"`
	MaxResponseBytes          int64    `json:"max_response_bytes"`
	AllowPrivateTargets       bool     `json:"allow_private_targets"`
	HealthProbeURLs           []string `json:"health_probe_urls"`
	HealthProbeTimeoutSeconds int      `json:"health_probe_timeout_seconds"`
}

type Node struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

type SEO struct {
	BaseURL        string `json:"base_url"`
	Token          string `json:"token"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type Webhooks struct {
	Format               string   `json:"format"`
	DomainURLs           []string `json:"domain_urls"`
	MachineURLs          []string `json:"machine_urls"`
	TimeoutSeconds       int      `json:"timeout_seconds"`
	RetryIntervalSeconds int      `json:"retry_interval_seconds"`
	Workers              int      `json:"workers"`
}

type Master struct {
	SEO                   SEO      `json:"seo"`
	Agents                []Node   `json:"agents"`
	IntervalSeconds       int      `json:"interval_seconds"`
	PerAgentConcurrency   int      `json:"per_agent_concurrency"`
	TitleTimeoutSeconds   int      `json:"title_timeout_seconds"`
	RPCTimeoutSeconds     int      `json:"rpc_timeout_seconds"`
	HealthTimeoutSeconds  int      `json:"health_timeout_seconds"`
	HealthIntervalSeconds int      `json:"health_interval_seconds"`
	FailurePercent        float64  `json:"failure_percent"`
	MinHealthyAgents      int      `json:"min_healthy_agents"`
	AlertOnInitialFailure bool     `json:"alert_on_initial_failure"`
	StateFile             string   `json:"state_file"`
	Webhooks              Webhooks `json:"webhooks"`
}

func DefaultAgent() Agent {
	return Agent{Listen: ":8003", Name: "agent-1", MaxConcurrent: 20, MaxTimeoutSeconds: 15,
		MaxResponseBytes: 2 << 20, HealthProbeTimeoutSeconds: 3}
}

func DefaultMaster() Master {
	return Master{SEO: SEO{BaseURL: "http://127.0.0.1:10001", TimeoutSeconds: 15}, IntervalSeconds: 600,
		PerAgentConcurrency: 20, TitleTimeoutSeconds: 15, RPCTimeoutSeconds: 20, HealthTimeoutSeconds: 5,
		HealthIntervalSeconds: 30, FailurePercent: 100, MinHealthyAgents: 1, AlertOnInitialFailure: true,
		StateFile: "data/state.json", Webhooks: Webhooks{Format: "feishu", TimeoutSeconds: 10, RetryIntervalSeconds: 30, Workers: 2}}
}

func read(path string, dst any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("config must contain exactly one JSON object")
	}
	return nil
}

func ReadAgent(path string) (Agent, error) {
	c := DefaultAgent()
	if err := read(path, &c); err != nil {
		return c, err
	}
	if _, _, err := net.SplitHostPort(c.Listen); err != nil {
		return c, fmt.Errorf("listen: %w", err)
	}
	if strings.TrimSpace(c.Name) == "" || len(c.Token) < 16 || strings.Contains(c.Token, "CHANGE_ME") {
		return c, errors.New("agent requires name and a real token of at least 16 characters")
	}
	if c.MaxConcurrent < 1 || c.MaxConcurrent > 200 || c.MaxTimeoutSeconds < 1 || c.MaxTimeoutSeconds > 15 || c.MaxResponseBytes < 1024 || c.MaxResponseBytes > 16<<20 || c.HealthProbeTimeoutSeconds < 1 || c.HealthProbeTimeoutSeconds > 10 {
		return c, errors.New("invalid agent concurrency, timeout or response size")
	}
	if len(c.HealthProbeURLs) > 8 {
		return c, errors.New("at most 8 health probe URLs are supported")
	}
	for _, u := range c.HealthProbeURLs {
		if err := ValidURL(u); err != nil {
			return c, err
		}
	}
	return c, nil
}

func ReadMaster(path string) (Master, error) {
	c := DefaultMaster()
	if err := read(path, &c); err != nil {
		return c, err
	}
	if err := validBaseURL(c.SEO.BaseURL); err != nil {
		return c, err
	}
	if len(c.Agents) == 0 || len(c.Agents) > 100 {
		return c, errors.New("agents must contain 1..100 nodes")
	}
	names, urls := map[string]bool{}, map[string]bool{}
	for i := range c.Agents {
		n := &c.Agents[i]
		n.URL = strings.TrimRight(n.URL, "/")
		if err := validBaseURL(n.URL); err != nil {
			return c, err
		}
		if n.Name == "" || names[n.Name] || urls[n.URL] || len(n.Token) < 16 || strings.Contains(n.Token, "CHANGE_ME") {
			return c, errors.New("each agent needs a unique name/URL and real token (16+ characters)")
		}
		names[n.Name] = true
		urls[n.URL] = true
	}
	if c.IntervalSeconds < 30 || c.PerAgentConcurrency < 1 || c.PerAgentConcurrency > 200 || c.TitleTimeoutSeconds < 1 || c.TitleTimeoutSeconds > 15 || c.RPCTimeoutSeconds < c.TitleTimeoutSeconds+2 || c.RPCTimeoutSeconds > 60 || c.HealthTimeoutSeconds < 1 || c.HealthTimeoutSeconds > 30 || c.HealthIntervalSeconds < 5 || c.MinHealthyAgents < 1 || c.MinHealthyAgents > len(c.Agents) || c.FailurePercent <= 0 || c.FailurePercent > 100 || c.SEO.TimeoutSeconds < 1 || c.SEO.TimeoutSeconds > 120 {
		return c, errors.New("invalid master schedule, concurrency, timeout, failure_percent or min_healthy_agents")
	}
	w := c.Webhooks
	if w.Format != "feishu" && w.Format != "wecom" && w.Format != "generic" {
		return c, errors.New("webhook format must be feishu, wecom or generic")
	}
	if w.TimeoutSeconds < 1 || w.TimeoutSeconds > 60 || w.RetryIntervalSeconds < 1 || w.Workers < 1 || w.Workers > 20 {
		return c, errors.New("invalid webhook timeout, retry interval or workers")
	}
	for _, list := range [][]string{w.DomainURLs, w.MachineURLs} {
		seen := map[string]bool{}
		for _, u := range list {
			if err := ValidURL(u); err != nil {
				return c, err
			}
			if seen[u] {
				return c, errors.New("duplicate webhook URL")
			}
			seen[u] = true
		}
	}
	if c.StateFile == "" {
		return c, errors.New("state_file is required")
	}
	if !filepath.IsAbs(c.StateFile) {
		c.StateFile = filepath.Join(filepath.Dir(path), c.StateFile)
	}
	return c, nil
}

func ValidURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid HTTP(S) URL in configuration")
	}
	return nil
}

func validBaseURL(raw string) error {
	if err := ValidURL(raw); err != nil {
		return err
	}
	u, _ := url.Parse(raw)
	if u.RawQuery != "" || u.ForceQuery {
		return errors.New("service base URLs must not contain a query string")
	}
	return nil
}
