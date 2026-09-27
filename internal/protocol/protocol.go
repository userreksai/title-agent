package protocol

import "time"

const Version = 1

type Request struct {
	Version        int    `json:"version"`
	ID             string `json:"id"`
	Domain         string `json:"domain"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Site failures are valid observations. HTTP/API/transport failures between
// master and agent are never represented by this type.
type Observation struct {
	Version    int       `json:"version"`
	ID         string    `json:"id"`
	Domain     string    `json:"domain"`
	Kind       string    `json:"kind"`
	Title      string    `json:"title,omitempty"`
	StatusCode int       `json:"status_code,omitempty"`
	FinalURL   string    `json:"final_url,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	CheckedAt  time.Time `json:"checked_at"`
	DurationMS int64     `json:"duration_ms"`
}

func (o Observation) Failed() bool { return o.Kind != "title" || o.StatusCode >= 400 }

type Health struct {
	Version           int    `json:"version"`
	Name              string `json:"name"`
	Ready             bool   `json:"ready"`
	MaxConcurrent     int    `json:"max_concurrent"`
	MaxTimeoutSeconds int    `json:"max_timeout_seconds"`
	Detail            string `json:"detail,omitempty"`
}
