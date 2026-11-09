package recon

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	xproxy "golang.org/x/net/proxy"
)

// scopeProxy is the local enforcing proxy Chrome points at. It checks every
// request/CONNECT against Scope *before* dialing the upstream egress proxy:
// out-of-scope hosts never leave the machine. The upstream is mandatory; there
// is deliberately no direct-dial fallback.
type scopeProxy struct {
	addr     string
	ln       net.Listener
	upstream *url.URL
	scope    Scope
	srv      *http.Server
	mu       sync.Mutex
	blocked  []BlockedRequest
	seen     int
}

func newScopeProxy(upstreamRaw string, scope Scope) (*scopeProxy, error) {
	up, err := validateDynamicProxy(upstreamRaw)
	if err != nil {
		return nil, fmt.Errorf("dynamic egress proxy: %w", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	p := &scopeProxy{addr: "http://" + ln.Addr().String(), ln: ln, upstream: up, scope: scope}
	p.srv = &http.Server{Handler: p, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := p.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("[recon] dynamic scope proxy stopped: %v", err)
		}
	}()
	return p, nil
}

func (p *scopeProxy) URL() string { return p.addr }

func (p *scopeProxy) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = p.srv.Shutdown(ctx)
}

func (p *scopeProxy) blockedList() []BlockedRequest {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]BlockedRequest(nil), p.blocked...)
}

func (p *scopeProxy) block(rawURL, reason string) {
	if u, err := url.Parse(rawURL); err == nil && u.RawQuery != "" {
		u.RawQuery = ""
		rawURL = u.String()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.blocked) >= 200 {
		return
	}
	for _, b := range p.blocked {
		if b.URL == rawURL {
			return
		}
	}
	p.blocked = append(p.blocked, BlockedRequest{URL: rawURL, Reason: reason})
	log.Printf("[recon] provider=dynamic blocked url=%s reason=%s", rawURL, reason)
}

func (p *scopeProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

func (p *scopeProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host, port := splitHostPortDefault(r.Host, "443")
	if ok, reason := p.scope.Allow(host); !ok {
		p.block("https://"+host, "out_of_scope: "+reason)
		http.Error(w, "blocked by scope", http.StatusForbidden)
		return
	}
	up, err := p.dialUpstream(r.Host, host, port)
	if err != nil {
		http.Error(w, "upstream egress unavailable", http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		up.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	conn, _, err := hj.Hijack()
	if err != nil {
		up.Close()
		return
	}
	_, _ = conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, conn); up.Close(); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, up); conn.Close(); done <- struct{}{} }()
	<-done
}

func (p *scopeProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	host, _ := splitHostPortDefault(r.Host, "80")
	if ok, reason := p.scope.Allow(host); !ok {
		p.block(r.URL.String(), "out_of_scope: "+reason)
		http.Error(w, "blocked by scope", http.StatusForbidden)
		return
	}
	if r.URL == nil || !r.URL.IsAbs() {
		http.Error(w, "proxy expects absolute URL", http.StatusBadRequest)
		return
	}
	tr, err := p.httpTransport()
	if err != nil {
		http.Error(w, "upstream egress unavailable", http.StatusBadGateway)
		return
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.Header = r.Header.Clone()
	out.Header.Del("Proxy-Authorization")
	resp, err := tr.RoundTrip(out)
	if err != nil {
		http.Error(w, "upstream egress unavailable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p *scopeProxy) httpTransport() (*http.Transport, error) {
	scheme := strings.ToLower(p.upstream.Scheme)
	tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	if scheme == "http" || scheme == "https" {
		tr.Proxy = http.ProxyURL(p.upstream)
		return tr, nil
	}
	d, err := p.socksDialer()
	if err != nil {
		return nil, err
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return d.Dial(network, addr)
	}
	return tr, nil
}

func (p *scopeProxy) socksDialer() (xproxy.Dialer, error) {
	u := p.upstream
	var auth *xproxy.Auth
	if u.User != nil {
		pw, _ := u.User.Password()
		auth = &xproxy.Auth{User: u.User.Username(), Password: pw}
	}
	return xproxy.SOCKS5("tcp", u.Host, auth, &net.Dialer{Timeout: 10 * time.Second})
}

// dialUpstream opens a tunnel to target through the configured egress proxy.
// An HTTP upstream gets a CONNECT, a SOCKS upstream gets a SOCKS5 CONNECT.
func (p *scopeProxy) dialUpstream(target, host, port string) (net.Conn, error) {
	up := p.upstream
	switch strings.ToLower(up.Scheme) {
	case "http", "https":
		conn, err := net.DialTimeout("tcp", up.Host, 10*time.Second)
		if err != nil {
			return nil, err
		}
		req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target)
		if up.User != nil {
			pw, _ := up.User.Password()
			token := base64.StdEncoding.EncodeToString([]byte(up.User.Username() + ":" + pw))
			req += "Proxy-Authorization: Basic " + token + "\r\n"
		}
		req += "\r\n"
		if _, err := io.WriteString(conn, req); err != nil {
			conn.Close()
			return nil, err
		}
		br := bufio.NewReader(conn)
		resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			conn.Close()
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			conn.Close()
			return nil, fmt.Errorf("upstream CONNECT status %d", resp.StatusCode)
		}
		return conn, nil
	case "socks5", "socks5h":
		d, err := p.socksDialer()
		if err != nil {
			return nil, err
		}
		return d.Dial("tcp", net.JoinHostPort(host, port))
	}
	return nil, fmt.Errorf("unsupported upstream scheme %q", up.Scheme)
}

func splitHostPortDefault(hostport, defPort string) (string, string) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.TrimSpace(hostport), defPort
	}
	return host, port
}

// sortedBlockedURLs is used by tests/log summaries.
func sortedBlockedURLs(in []BlockedRequest) []string {
	out := make([]string, 0, len(in))
	for _, b := range in {
		out = append(out, b.URL)
	}
	sort.Strings(out)
	return out
}
