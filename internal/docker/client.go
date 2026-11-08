package docker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Client is a small Docker Engine API client that speaks HTTP over a unix
// socket, TCP (optionally TLS) or an ssh connection running
// `docker system dial-stdio` on the remote side.
type Client struct {
	Host       string
	dial       func(ctx context.Context) (net.Conn, error)
	hc         *http.Client
	base       string
	hostHeader string
	sshArgs    []string // for ssh hosts: args up to (not including) the remote command
	detached   atomic.Bool
}

// Options tweak client construction.
type Options struct {
	// NoSSHMux disables the ssh ControlMaster connection sharing ctop sets up.
	NoSSHMux bool
}

// APIError is an error response from the daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

// IsNotFound reports whether err is a 404 from the daemon.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// New creates a client for the given host spec.
func New(spec HostSpec, opts Options) (*Client, error) {
	u, err := url.Parse(spec.Host)
	if err != nil {
		return nil, fmt.Errorf("invalid docker host %q: %w", spec.Host, err)
	}
	c := &Client{Host: spec.Host, base: "http://docker", hostHeader: "docker"}

	switch u.Scheme {
	case "unix":
		path := u.Path
		if path == "" {
			path = u.Host
		}
		c.dial = func(ctx context.Context) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		}
	case "tcp", "http", "https":
		addr := u.Host
		useTLS := spec.TLS != nil || u.Scheme == "https"
		if u.Port() == "" {
			if useTLS {
				addr = net.JoinHostPort(u.Hostname(), "2376")
			} else {
				addr = net.JoinHostPort(u.Hostname(), "2375")
			}
		}
		var tlsCfg *tls.Config
		if useTLS {
			tlsCfg, err = loadTLS(spec.TLS, u.Hostname())
			if err != nil {
				return nil, err
			}
		}
		c.base = "http://" + addr
		c.hostHeader = addr
		c.dial = func(ctx context.Context) (net.Conn, error) {
			d := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
			conn, err := d.DialContext(ctx, "tcp", addr)
			if err != nil || tlsCfg == nil {
				return conn, err
			}
			tc := tls.Client(conn, tlsCfg)
			if err := tc.HandshakeContext(ctx); err != nil {
				conn.Close()
				return nil, err
			}
			return tc, nil
		}
	case "ssh":
		if u.Hostname() == "" {
			return nil, fmt.Errorf("invalid ssh host %q", spec.Host)
		}
		c.sshArgs = sshArgs(u, !opts.NoSSHMux)
		args := append(append([]string{}, c.sshArgs...), "docker", "system", "dial-stdio")
		c.dial = func(ctx context.Context) (net.Conn, error) { return dialCmd(ctx, "ssh", args, c.detached.Load()) }
	default:
		return nil, fmt.Errorf("unsupported docker host scheme %q (use unix://, tcp:// or ssh://)", u.Scheme)
	}

	c.hc = &http.Client{Transport: &http.Transport{
		DialContext:         func(ctx context.Context, _, _ string) (net.Conn, error) { return c.dial(ctx) },
		MaxIdleConns:        16,
		MaxIdleConnsPerHost: 16,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true,
	}}
	return c, nil
}

// Detach makes subsequent ssh subprocesses run in their own session, so
// they can't be hit by terminal signals but also can't prompt for
// passwords. Call it once the first (interactive) connection succeeded;
// idle interactive connections are dropped.
func (c *Client) Detach() {
	c.detached.Store(true)
	c.Close()
}

// IsSSH reports whether the client tunnels through ssh.
func (c *Client) IsSSH() bool { return c.sshArgs != nil }

// Close releases idle connections (and with them idle ssh processes).
func (c *Client) Close() {
	if t, ok := c.hc.Transport.(*http.Transport); ok {
		t.CloseIdleConnections()
	}
}

func loadTLS(files *TLSFiles, serverName string) (*tls.Config, error) {
	cfg := &tls.Config{ServerName: serverName, MinVersion: tls.VersionTLS12}
	if files == nil {
		return cfg, nil
	}
	cfg.InsecureSkipVerify = files.SkipVerify
	if b, err := os.ReadFile(files.CA); err == nil {
		pool := x509.NewCertPool()
		pool.AppendCertsFromPEM(b)
		cfg.RootCAs = pool
	}
	if _, err := os.Stat(files.Cert); err == nil {
		cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
		if err != nil {
			return nil, fmt.Errorf("loading TLS client cert: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}

// sshArgs builds the ssh argument list (without remote command). When mux is
// enabled, connections share a ControlMaster so every HTTP connection ctop
// opens doesn't cost a full ssh handshake.
func sshArgs(u *url.URL, mux bool) []string {
	args := []string{"-T", "-o", "ConnectTimeout=15", "-o", "ServerAliveInterval=30"}
	if mux {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPath="+filepath.Join(os.TempDir(), "ctop-ssh-%C"),
			"-o", "ControlPersist=60",
		)
	}
	if u.User != nil && u.User.Username() != "" {
		args = append(args, "-l", u.User.Username())
	}
	if p := u.Port(); p != "" {
		args = append(args, "-p", p)
	}
	return append(args, "--", u.Hostname())
}

// ---------------------------------------------------------------------------
// net.Conn over a subprocess' stdin/stdout

type cmdConn struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout io.ReadCloser
	stderr *syncBuffer
	done   chan struct{}
	once   sync.Once
}

func dialCmd(ctx context.Context, name string, args []string, detached bool) (net.Conn, error) {
	cmd := exec.Command(name, args...)
	if detached {
		detach(cmd)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	errBuf := &syncBuffer{}
	cmd.Stderr = errBuf
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting %s: %w", name, err)
	}
	c := &cmdConn{cmd: cmd, stdin: stdin, stdout: stdout, stderr: errBuf, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		close(c.done)
	}()
	return c, nil
}

func (c *cmdConn) Read(p []byte) (int, error) {
	n, err := c.stdout.Read(p)
	if err == io.EOF && n == 0 {
		select {
		case <-c.done:
		case <-time.After(500 * time.Millisecond):
		}
		if msg := c.stderr.lastLines(3); msg != "" {
			if !strings.HasPrefix(msg, "ssh:") {
				msg = "ssh: " + msg
			}
			return 0, errors.New(msg)
		}
	}
	return n, err
}

func (c *cmdConn) Write(p []byte) (int, error) { return c.stdin.Write(p) }

func (c *cmdConn) CloseWrite() error { return c.stdin.Close() }

func (c *cmdConn) Close() error {
	c.once.Do(func() {
		_ = c.stdin.Close()
		_ = c.stdout.Close() // unblock readers right away
		go func() {
			select {
			case <-c.done:
				return
			case <-time.After(100 * time.Millisecond):
			}
			if c.cmd.Process == nil {
				return
			}
			_ = c.cmd.Process.Signal(syscall.SIGTERM)
			select {
			case <-c.done:
			case <-time.After(3 * time.Second):
				_ = c.cmd.Process.Kill()
			}
		}()
	})
	return nil
}

type dummyAddr struct{}

func (dummyAddr) Network() string { return "cmd" }
func (dummyAddr) String() string  { return "cmd" }

func (c *cmdConn) LocalAddr() net.Addr                { return dummyAddr{} }
func (c *cmdConn) RemoteAddr() net.Addr               { return dummyAddr{} }
func (c *cmdConn) SetDeadline(t time.Time) error      { return nil }
func (c *cmdConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *cmdConn) SetWriteDeadline(t time.Time) error { return nil }

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.buf.Len() > 64<<10 {
		b.buf.Reset()
	}
	return b.buf.Write(p)
}

func (b *syncBuffer) lastLines(n int) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(b.buf.String()), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.TrimSpace(strings.Join(lines, " "))
}

// ---------------------------------------------------------------------------
// HTTP helpers

func (c *Client) do(ctx context.Context, method, path string, q url.Values, body any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Host = c.hostHeader
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, err
	}
	if resp.StatusCode >= 400 {
		defer resp.Body.Close()
		return nil, readAPIError(resp)
	}
	return resp, nil
}

func readAPIError(resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		Message string `json:"message"`
	}
	msg := strings.TrimSpace(string(b))
	if json.Unmarshal(b, &e) == nil && e.Message != "" {
		msg = e.Message
	}
	if msg == "" {
		msg = resp.Status
	}
	return &APIError{Status: resp.StatusCode, Message: msg}
}

func (c *Client) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	resp, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *Client) getRaw(ctx context.Context, path string, q url.Values) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, path, q, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *Client) post(ctx context.Context, path string, q url.Values, body, out any) error {
	resp, err := c.do(ctx, http.MethodPost, path, q, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

func (c *Client) delete(ctx context.Context, path string, q url.Values) error {
	resp, err := c.do(ctx, http.MethodDelete, path, q, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return nil
}

// HijackedConn is a raw bidirectional stream after an HTTP upgrade
// (used for exec and attach).
type HijackedConn struct {
	Conn   net.Conn
	Reader *bufio.Reader
}

// Read reads from the stream (including bytes buffered during the upgrade).
func (h *HijackedConn) Read(p []byte) (int, error) { return h.Reader.Read(p) }

// Write writes to the stream.
func (h *HijackedConn) Write(p []byte) (int, error) { return h.Conn.Write(p) }

// CloseWrite half-closes the stream if the transport supports it.
func (h *HijackedConn) CloseWrite() error {
	if cw, ok := h.Conn.(interface{ CloseWrite() error }); ok {
		return cw.CloseWrite()
	}
	return nil
}

// Close closes the stream.
func (h *HijackedConn) Close() error { return h.Conn.Close() }

func (c *Client) hijack(ctx context.Context, path string, q url.Values, body any) (*HijackedConn, error) {
	var b []byte
	if body != nil {
		var err error
		if b, err = json.Marshal(body); err != nil {
			return nil, err
		}
	}
	u := c.base + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequest(http.MethodPost, u, bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	req.Host = c.hostHeader
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")

	conn, err := c.dial(ctx)
	if err != nil {
		return nil, err
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp.StatusCode >= 400 {
		err := readAPIError(resp)
		conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		conn.Close()
		return nil, fmt.Errorf("unexpected response: %s", resp.Status)
	}
	return &HijackedConn{Conn: conn, Reader: br}, nil
}
