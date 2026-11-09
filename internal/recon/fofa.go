package recon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// FOFAProvider is the P0 external intel source (FOFA). It is DISABLED by
// default; enabling requires ARTEX_REDTEAM_RECON_FOFA_ENABLED=1 plus a valid
// email/key. Any failure (auth, quota, timeout, malformed response) is returned
// as an error and degrades only this source.
type FOFAProvider struct {
	cfg    FOFAConfig
	client *http.Client
}

// NewFOFAProvider builds the external provider. The URL is validated to be HTTPS
// in production (loopback HTTP is allowed so tests can use httptest).
func NewFOFAProvider(cfg FOFAConfig, client *http.Client) *FOFAProvider {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &FOFAProvider{cfg: cfg, client: client}
}

func (p *FOFAProvider) Name() string { return ProviderFOFA }

func (p *FOFAProvider) Run(ctx context.Context, req Request) ([]Record, error) {
	if strings.TrimSpace(p.cfg.Key) == "" {
		return nil, fmt.Errorf("FOFA 未配置密钥（ARTEX_REDTEAM_RECON_FOFA_KEY）")
	}
	if strings.TrimSpace(p.cfg.Email) == "" {
		return nil, fmt.Errorf("FOFA 未配置账号（ARTEX_REDTEAM_RECON_FOFA_EMAIL）")
	}
	base, err := url.Parse(p.cfg.BaseURL)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("FOFA 地址无效: %q", p.cfg.BaseURL)
	}
	if base.Scheme != "https" && !isLoopbackHost(base.Hostname()) {
		return nil, fmt.Errorf("FOFA 必须使用 HTTPS（当前 %s）", base.Scheme)
	}
	seed := req.Scope.Seed
	if seed == "" {
		seed = HostOf(req.Seed)
	}
	maxHits := p.cfg.MaxResults
	if req.MaxHits > 0 {
		maxHits = req.MaxHits
	}
	if maxHits <= 0 || maxHits > 1000 {
		maxHits = 200
	}
	q := fmt.Sprintf(`domain="%s"`, seed)
	u := *base
	qv := u.Query()
	qv.Set("email", p.cfg.Email)
	qv.Set("key", p.cfg.Key)
	qv.Set("q", q)
	qv.Set("fields", "host,domain,ip,server,title")
	qv.Set("size", fmt.Sprint(maxHits))
	u.RawQuery = qv.Encode()

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("User-Agent", "artex-redteam-recon/1.0")
	resp, err := p.client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("FOFA 请求失败: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("FOFA HTTP %d: %s", resp.StatusCode, truncate(string(body), 200))
	}
	var payload struct {
		Error   bool   `json:"error"`
		ErrMsg  string `json:"errmsg"`
		Size    int    `json:"size"`
		Results []any  `json:"results"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("FOFA JSON: %w", err)
	}
	if payload.Error {
		return nil, fmt.Errorf("FOFA 返回错误: %s", payload.ErrMsg)
	}
	results := payload.Results
	if len(results) > maxHits {
		results = results[:maxHits]
	}
	now := req.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rawRef := fmt.Sprintf("fofa:%s", q)
	out := make([]Record, 0, len(results)*2)
	for _, item := range results {
		fields := asStringSlice(item)
		if len(fields) < 4 {
			continue
		}
		hostRaw, domain, ip, server := fields[0], fields[1], fields[2], fields[3]
		title := ""
		if len(fields) > 4 {
			title = fields[4]
		}
		host := HostOf(hostRaw)
		if host == "" {
			host = HostOf(domain)
		}
		if host == "" {
			continue
		}
		// Provider-side scope pre-check: never emit (and the orchestrator will
		// never persist) out-of-scope values.
		if ok, reason := req.Scope.Allow(host); !ok {
			log.Printf("[recon] 丢弃越界 FOFA 记录 provider=fofa value=%q reason=%s at=%s", host, reason, now.Format(time.RFC3339))
			continue
		}
		parent := seed
		if host != seed {
			out = append(out, Record{Type: TypeSubdomain, Value: host, Parent: parent, Source: "fofa", ObservedAt: now, RawRef: rawRef})
		}
		if validIP(ip) {
			out = append(out, Record{Type: TypeIP, Value: ip, Parent: host, Source: "fofa", ObservedAt: now, RawRef: rawRef})
		}
		if strings.Contains(hostRaw, "://") {
			techs := []string{}
			if server = strings.TrimSpace(server); server != "" {
				techs = append(techs, server)
			}
			out = append(out, Record{
				Type: TypeService, Value: hostRaw, Parent: parent, Source: "fofa", ObservedAt: now, RawRef: rawRef,
				IP: ip, Title: title, Technologies: techs,
			})
		}
	}
	return out, nil
}

func asStringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		out[i] = fmt.Sprint(x)
	}
	return out
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func init() {
	defaultFactories = append(defaultFactories, func(cfg Config, client *http.Client) Provider {
		// The external source is registered but disabled unless explicitly on
		// (DefaultRegistry checks Config.Enabled / cfg.FOFA.Enabled).
		return NewFOFAProvider(cfg.FOFA, client)
	})
}
