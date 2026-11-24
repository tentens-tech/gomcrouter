package cmd

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tentens-tech/gomcrouter/version"
)

const ioTimeout = 5 * time.Second

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

// startFakeMemcached serves "version" and "get" (always a miss) on a
// loopback port and returns its address.
func startFakeMemcached(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	var (
		mu     sync.Mutex
		conns  []net.Conn
		closed bool // set by cleanup; later conns are closed on accept
		wg     sync.WaitGroup
	)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			if closed {
				// accepted after cleanup closed the tracked conns: nobody
				// would close it, so do not start a reader on it
				mu.Unlock()
				_ = c.Close()
				continue
			}
			conns = append(conns, c)
			mu.Unlock()
			wg.Add(1)
			go func() {
				defer wg.Done()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					resp := "ERROR\r\n"
					switch {
					case strings.HasPrefix(line, "version"):
						resp = "VERSION 1.6.0\r\n"
					case strings.HasPrefix(line, "get "):
						resp = "END\r\n"
					}
					if _, err := c.Write([]byte(resp)); err != nil {
						return
					}
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		closed = true
		for _, c := range conns {
			_ = c.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return ln.Addr().String()
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, []byte(content), 0o600))
	return p
}

// execute runs a fresh root command with args and returns its output.
func execute(ctx context.Context, args ...string) (string, error) {
	c := NewRootCmd()
	var out bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&out)
	c.SetArgs(args)
	err := c.ExecuteContext(ctx)
	return out.String(), err
}

func TestNewRootCmd_FlagDefaults(t *testing.T) {
	c := NewRootCmd()

	assert.Equal(t, RootCmdName, c.Use)
	assert.Equal(t, version.Version, c.Version)

	tests := []struct {
		name      string
		shorthand string
		def       string
	}{
		{name: "log-level", shorthand: "", def: "info"},
		{name: "router-config", shorthand: "f", def: "router.yaml"},
		{name: "listen", shorthand: "l", def: "tcp://:8080"},
		{name: "metrics-listen-addr", shorthand: "L", def: ":9090"},
		{name: "healthcheck-interval", shorthand: "H", def: "10s"},
		{name: "upstream-connections", shorthand: "C", def: "2"},
		{name: "timeout-ms", shorthand: "r", def: "1000"},
		{name: "server-event-loops", shorthand: "n", def: "1"},
		{name: "dns-cache-ttl", shorthand: "t", def: "30s"},
		{name: "buffer-size", shorthand: "b", def: "1048576"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := c.PersistentFlags().Lookup(tt.name)
			require.NotNil(t, f)
			assert.Equal(t, tt.shorthand, f.Shorthand)
			assert.Equal(t, tt.def, f.DefValue)
		})
	}
	assert.Equal(t, len(tests), countFlags(c), "every flag must be covered by this test")
}

func countFlags(c *cobra.Command) int {
	n := 0
	c.PersistentFlags().VisitAll(func(*pflag.Flag) { n++ })
	return n
}

func TestRootCmd_Version(t *testing.T) {
	out, err := execute(t.Context(), "--version")
	require.NoError(t, err)
	assert.Equal(t, RootCmdName+" version "+version.Version+"\n", out)
}

func TestRootCmd_Help(t *testing.T) {
	out, err := execute(t.Context(), "--help")
	require.NoError(t, err)

	assert.Contains(t, out, "High-performance memcached router")
	for _, flag := range []string{
		"--log-level", "--router-config", "--listen", "--metrics-listen-addr",
		"--healthcheck-interval", "--upstream-connections", "--timeout-ms",
		"--server-event-loops", "--dns-cache-ttl", "--buffer-size",
	} {
		assert.Contains(t, out, flag)
	}
}

func TestRootCmd_Errors(t *testing.T) {
	validConfig := writeFile(t, "router.yaml", "orderedPool: []\nroute:\n  type: DefaultRoute\n")

	tests := []struct {
		name    string
		args    []string
		wantErr func(t *testing.T, err error)
	}{
		{
			name: "router config does not exist",
			args: []string{"--router-config", filepath.Join(t.TempDir(), "missing.yaml")},
			wantErr: func(t *testing.T, err error) {
				require.ErrorIs(t, err, fs.ErrNotExist)
			},
		},
		{
			name: "router config is not valid yaml",
			args: []string{"--router-config", writeFile(t, "bad.yaml", "route: [unterminated\n")},
			wantErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "yaml")
			},
		},
		{
			name: "invalid log level",
			args: []string{"--router-config", validConfig, "--log-level", "loud"},
			wantErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "loud")
			},
		},
		{
			name: "unknown flag",
			args: []string{"--no-such-flag"},
			wantErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "unknown flag")
			},
		},
		{
			name: "invalid duration",
			args: []string{"--healthcheck-interval", "soon"},
			wantErr: func(t *testing.T, err error) {
				require.ErrorContains(t, err, "soon")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// the metrics port is never bound on these paths: a fixed busy
			// port would make the test fail if it were
			args := append([]string{
				"--log-level", "error",
				"--listen", "tcp://" + freeAddr(t),
				"--metrics-listen-addr", freeAddr(t),
			}, tt.args...)

			_, err := execute(t.Context(), args...)
			tt.wantErr(t, err)
		})
	}
}

type running struct {
	routerAddr  string
	metricsAddr string
	done        <-chan error
}

// startRouter runs the full command (gnet server, upstream engine, metrics
// server) against a fake memcached until ctx is cancelled.
func startRouter(t *testing.T, ctx context.Context) running {
	t.Helper()
	upstream := startFakeMemcached(t)
	cfg := writeFile(t, "router.yaml", "orderedPool:\n  - "+upstream+"\nroute:\n  type: DefaultRoute\n")

	r := running{routerAddr: freeAddr(t), metricsAddr: freeAddr(t)}
	done := make(chan error, 1)
	r.done = done
	go func() {
		_, err := execute(ctx,
			"--router-config", cfg,
			"--listen", "tcp://"+r.routerAddr,
			"--metrics-listen-addr", r.metricsAddr,
			"--log-level", "error",
			"--upstream-connections", "1",
			"--server-event-loops", "1",
			"--healthcheck-interval", "1h",
			"--dns-cache-ttl", "1h",
		)
		done <- err
	}()
	return r
}

func (r running) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case err := <-r.done:
		require.NoError(t, err)
	case <-time.After(3 * ioTimeout):
		t.Fatal("command did not shut down")
	}
}

func versionRoundTrip(addr string) (string, error) {
	c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond)
	if err != nil {
		return "", err
	}
	defer func() { _ = c.Close() }()
	if err := c.SetDeadline(time.Now().Add(ioTimeout)); err != nil {
		return "", err
	}
	if _, err := c.Write([]byte("version\r\n")); err != nil {
		return "", err
	}
	return bufio.NewReader(c).ReadString('\n')
}

func scrape(t *testing.T, addr string) (string, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/metrics", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", errors.New(resp.Status)
	}
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func TestRootCmd_ServesProxiesAndShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := startRouter(t, ctx)

	var resp string
	require.Eventually(t, func() bool {
		var err error
		resp, err = versionRoundTrip(r.routerAddr)
		return err == nil
	}, ioTimeout, 10*time.Millisecond)
	assert.Equal(t, "VERSION 1.6.0\r\n", resp, "version is proxied to the upstream by DefaultRoute")

	// the metrics server is up and the async collector consumes events
	require.Eventually(t, func() bool {
		body, err := scrape(t, r.metricsAddr)
		return err == nil &&
			strings.Contains(body, "gomcrouter_version{") &&
			strings.Contains(body, `gomcrouter_requests_total{cmd="version"}`) &&
			strings.Contains(body, `gomcrouter_upstream_requests_total{cmd="version"`)
	}, ioTimeout, 20*time.Millisecond)

	cancel()
	r.waitStopped(t)

	_, err := versionRoundTrip(r.routerAddr)
	require.Error(t, err, "router must not accept connections after shutdown")
	_, err = scrape(t, r.metricsAddr)
	require.Error(t, err, "metrics server must be stopped after shutdown")
}

func TestRootCmd_ShutsDownOnSignal(t *testing.T) {
	for _, sig := range []os.Signal{os.Interrupt, syscall.SIGTERM} {
		t.Run(sig.String(), func(t *testing.T) {
			r := startRouter(t, t.Context())

			// Once the router answers, the signal handler is installed (it
			// is registered before anything is started), so the signal
			// cannot kill the test binary.
			require.Eventually(t, func() bool {
				_, err := versionRoundTrip(r.routerAddr)
				return err == nil
			}, ioTimeout, 10*time.Millisecond)

			p, err := os.FindProcess(os.Getpid())
			require.NoError(t, err)
			require.NoError(t, p.Signal(sig))

			r.waitStopped(t)
		})
	}
}

func TestExecute_Version(t *testing.T) {
	out := captureStdout(t, func() {
		withArgs(t, "--version")
		Execute()
	})
	assert.Equal(t, RootCmdName+" version "+version.Version+"\n", out)
}

const executeChildEnv = "GOMCROUTER_TEST_EXECUTE_CHILD"

// TestExecute_ExitsNonZeroOnError re-runs the test binary so that the
// os.Exit(1) in Execute can be observed.
func TestExecute_ExitsNonZeroOnError(t *testing.T) {
	if os.Getenv(executeChildEnv) == "1" {
		os.Args = []string{RootCmdName, "--no-such-flag"}
		Execute()
		return // unreachable when Execute exits
	}

	child := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestExecute_ExitsNonZeroOnError$") // #nosec G204,G702 -- re-runs this test binary
	child.Env = append(os.Environ(), executeChildEnv+"=1")
	var stderr bytes.Buffer
	child.Stderr = &stderr

	err := child.Run()

	var exitErr *exec.ExitError
	require.ErrorAs(t, err, &exitErr)
	assert.Equal(t, 1, exitErr.ExitCode())
	assert.Contains(t, stderr.String(), "unknown flag: --no-such-flag")
}

func withArgs(t *testing.T, args ...string) {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{RootCmdName}, args...)
	t.Cleanup(func() { os.Args = orig })
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	require.NoError(t, r.Close())
	return string(out)
}
