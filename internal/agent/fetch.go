package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/net/idna"
	"github.com/userreksai/title-agent/internal/protocol"
	"github.com/userreksai/title-agent/internal/settings"
)

// Resolve and validate at dial time, including redirects, to prevent DNS
// rebinding to local services. Private targets can be explicitly enabled.
func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, p := range []string{"100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15", "2001:db8::/32"} {
		if netip.MustParsePrefix(p).Contains(a) {
			return false
		}
	}
	return true
}

func newHTTPClient(cfg settings.Agent) *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{ForceAttemptHTTP2: true, MaxIdleConns: cfg.MaxConcurrent * 2, MaxIdleConnsPerHost: 2, IdleConnTimeout: 30 * time.Second, TLSHandshakeTimeout: 5 * time.Second, MaxResponseHeaderBytes: 64 << 10}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			if !cfg.AllowPrivateTargets && !publicIP(ip.IP) {
				return nil, errors.New("target resolves to a non-public address")
			}
		}
		var last error = errors.New("no address returned by DNS")
		for _, ip := range ips {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return conn, nil
			}
			last = err
		}
		return nil, last
	}
	return &http.Client{Transport: transport, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return validateURL(r.URL)
	}}
}

func validateURL(u *url.URL) error {
	if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return errors.New("expected HTTP(S) URL without credentials")
	}
	if u.Port() != "" && u.Port() != "80" && u.Port() != "443" {
		return errors.New("only ports 80 and 443 are supported")
	}
	return nil
}

func targets(domain string) ([]string, error) {
	value := strings.TrimSpace(domain)
	if value == "" || len(value) > 2048 {
		return nil, errors.New("invalid domain")
	}
	explicit := strings.Contains(value, "://")
	if !explicit {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil {
		return nil, err
	}
	if err = validateURL(u); err != nil {
		return nil, err
	}
	host, err := idna.Lookup.ToASCII(u.Hostname())
	if err != nil {
		return nil, err
	}
	if port := u.Port(); port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	result := []string{u.String()}
	if !explicit {
		u.Scheme = "http"
		result = append(result, u.String())
	}
	return result, nil
}

func fetch(ctx context.Context, client *http.Client, cfg settings.Agent, domain string) protocol.Observation {
	started := time.Now()
	o := protocol.Observation{Kind: "fetch_error", CheckedAt: started.UTC()}
	candidates, err := targets(domain)
	if err != nil {
		o.Detail = err.Error()
		return o
	}
	for i, target := range candidates {
		// HTTPS and HTTP share the one domain deadline. Reserve time for HTTP
		// when HTTPS is unavailable; do not retry a received error page.
		candidateCtx := ctx
		cancel := func() {}
		if deadline, ok := ctx.Deadline(); ok && len(candidates)-i > 1 {
			candidateCtx, cancel = context.WithTimeout(ctx, time.Until(deadline)/time.Duration(len(candidates)-i))
		}
		o = fetchURL(candidateCtx, client, cfg, target)
		cancel()
		if o.Kind != "fetch_error" && o.Kind != "timeout" {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		o.Kind = "timeout"
		o.Title = ""
		o.Detail = "domain deadline exceeded"
	}
	o.CheckedAt = time.Now().UTC()
	o.DurationMS = time.Since(started).Milliseconds()
	return o
}

func fetchURL(ctx context.Context, client *http.Client, cfg settings.Agent, target string) protocol.Observation {
	o := protocol.Observation{Kind: "fetch_error"}
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		o.Detail = err.Error()
		return o
	}
	r.Header.Set("User-Agent", "Mozilla/5.0 (compatible; SEO-Title-Agent/1.0)")
	r.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(r)
	if err != nil {
		var ne net.Error
		if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
			o.Kind = "timeout"
		}
		o.Detail = "site request failed"
		return o
	}
	defer resp.Body.Close()
	o.StatusCode = resp.StatusCode
	o.FinalURL = resp.Request.URL.String()
	// Parse incrementally: a title in the head succeeds even when the rest of
	// the response stalls. Both encoded and decoded streams are bounded.
	limited := io.LimitReader(resp.Body, cfg.MaxResponseBytes)
	reader := io.Reader(limited)
	// charset.NewReader defaults undeclared UTF-8 to Windows-1252 in some
	// pages. Preserve valid UTF-8 when the server has no charset declaration.
	prefix := make([]byte, 0, 1024)
	for len(prefix) < 1024 {
		chunk := make([]byte, 1024-len(prefix))
		n, readErr := limited.Read(chunk)
		prefix = append(prefix, chunk[:n]...)
		// Stop as soon as a complete title arrives, even if the rest stalls.
		if bytes.Contains(bytes.ToLower(prefix), []byte("</title")) || readErr == io.EOF {
			break
		}
		if readErr != nil {
			o.Detail = "read response failed"
			if ctx.Err() != nil {
				o.Kind = "timeout"
			}
			return o
		}
	}
	reader = io.MultiReader(bytes.NewReader(prefix), limited)
	encoding, name, certain := charset.DetermineEncoding(prefix, resp.Header.Get("Content-Type"))
	utf8Prefix := utf8.Valid(prefix)
	// A sniffing boundary may split a UTF-8 code point.
	for cut := 1; !utf8Prefix && cut <= 3 && len(prefix) >= cut; cut++ {
		utf8Prefix = utf8.Valid(prefix[:len(prefix)-cut])
	}
	if certain || !utf8Prefix || (name != "windows-1252" && name != "utf-8") {
		reader = encoding.NewDecoder().Reader(reader)
	}
	z := html.NewTokenizer(io.LimitReader(reader, cfg.MaxResponseBytes))
	z.SetMaxBuf(int(cfg.MaxResponseBytes))
	var title strings.Builder
	inside := false
	for {
		switch z.Next() {
		case html.StartTagToken:
			t := z.Token()
			if t.Data == "title" {
				inside = true
			}
		case html.TextToken:
			if inside {
				title.Write(z.Text())
				if title.Len() > 16384 {
					o.Kind = "too_large"
					o.Detail = "title exceeds 16 KiB"
					return o
				}
			}
		case html.EndTagToken:
			if inside && z.Token().Data == "title" {
				o.Title = strings.Join(strings.Fields(title.String()), " ")
				if o.Title != "" {
					o.Kind = "title"
					return o
				}
				inside = false
			}
		case html.ErrorToken:
			if ctx.Err() != nil {
				o.Kind = "timeout"
				o.Detail = "title deadline exceeded"
			} else if z.Err() != nil && z.Err() != io.EOF {
				o.Kind = "fetch_error"
				o.Detail = "read HTML failed"
			} else if resp.StatusCode >= 400 {
				o.Kind = "http_error"
				o.Detail = fmt.Sprintf("HTTP %d without title", resp.StatusCode)
			} else {
				o.Kind = "empty_title"
				o.Detail = "no title found within response limit"
			}
			return o
		}
	}
}
