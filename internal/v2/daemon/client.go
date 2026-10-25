package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/kkestell/gnat/internal/v2/store"
)

var client = &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}

func ReadDescriptor(dir string) (Descriptor, error) {
	var desc Descriptor
	data, err := os.ReadFile(filepath.Join(runtimeDir(dir), "daemon.json"))
	if err != nil {
		return desc, err
	}
	if err := json.Unmarshal(data, &desc); err != nil {
		return desc, err
	}
	u, err := url.Parse(desc.URL)
	if err != nil {
		return desc, err
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		return desc, err
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 || host != "127.0.0.1" || u.Scheme != "http" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || desc.Generation == "" || desc.Token == "" {
		return desc, fmt.Errorf("invalid daemon descriptor")
	}
	return desc, nil
}

func request(ctx context.Context, desc Descriptor, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, desc.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+desc.Token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return fmt.Errorf("daemon: %s", bytes.TrimSpace(data))
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return err
}

func ready(ctx context.Context, dir string) (Descriptor, error) {
	desc, err := ReadDescriptor(dir)
	if err != nil {
		return desc, err
	}
	var generation string
	if err := request(ctx, desc, "GET", "/api/ready", nil, &generation); err != nil {
		return desc, err
	}
	if generation != desc.Generation {
		return desc, fmt.Errorf("daemon generation mismatch")
	}
	return desc, nil
}

// Ensure starts a detached copy of executable. The daemon's lifetime lock decides
// ownership when several launchers race; all launchers wait for the same handshake.
func Ensure(ctx context.Context, dir, executable string) (Descriptor, error) {
	if desc, err := ready(ctx, dir); err == nil {
		return desc, nil
	}
	if err := prepareRuntime(dir); err != nil {
		return Descriptor{}, err
	}
	logPath := filepath.Join(runtimeDir(dir), "daemon.log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return Descriptor{}, err
	}
	cmd := exec.Command(executable, "--internal-daemon-run")
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	err = cmd.Start()
	log.Close()
	if err != nil {
		return Descriptor{}, err
	}
	if err := cmd.Process.Release(); err != nil {
		return Descriptor{}, err
	}
	timeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if desc, err := ready(timeout, dir); err == nil {
			return desc, nil
		} else {
			last = err
		}
		select {
		case <-timeout.Done():
			return Descriptor{}, fmt.Errorf("daemon startup: %w (see %s; last readiness error: %v)", timeout.Err(), logPath, last)
		case <-ticker.C:
		}
	}
}

func OpenWorkspace(ctx context.Context, desc Descriptor, dir string) (store.Snapshot, error) {
	var result store.Snapshot
	err := request(ctx, desc, "POST", "/api/workspaces/open", map[string]string{"directory": dir}, &result)
	return result, err
}

func Stop(ctx context.Context, dir string) error {
	desc, err := ready(ctx, dir)
	if err != nil {
		// A stale descriptor is harmless, but an unresponsive lock owner must not be
		// reported as stopped. Do not signal a PID read from a descriptor.
		lock, openErr := os.OpenFile(filepath.Join(runtimeDir(dir), "daemon.lock"), os.O_RDWR, 0600)
		if os.IsNotExist(openErr) {
			return nil
		}
		if openErr != nil {
			return openErr
		}
		defer lock.Close()
		if lockErr := tryLock(lock); lockErr != nil {
			return fmt.Errorf("daemon is not responding: %w", err)
		}
		defer unlock(lock)
		return nil
	}
	if err := request(ctx, desc, "POST", "/api/daemon/stop", nil, nil); err != nil {
		return err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		current, err := ReadDescriptor(dir)
		if os.IsNotExist(err) || err == nil && current.Generation != desc.Generation {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("waiting for daemon shutdown: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
