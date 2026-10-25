// Package daemon owns the per-user v2 listener and its detached process lifetime.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/kkestell/gnat/internal/v2/core"
	"github.com/kkestell/gnat/internal/v2/harness"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/kkestell/gnat/internal/v2/store"
	"golang.org/x/sys/unix"
)

type Descriptor struct {
	PID        int    `json:"pid"`
	URL        string `json:"url"`
	Generation string `json:"generation"`
	Token      string `json:"token"`
}

func runtimeDir(dir string) string { return filepath.Join(dir, "runtime") }

func prepareRuntime(dir string) error {
	path := runtimeDir(dir)
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

// Run holds the lock until HTTP, SQLite and descriptor cleanup have completed.
func Run(dir string) error {
	if err := prepareRuntime(dir); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(runtimeDir(dir), "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := tryLock(lock); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil
		}
		return err
	}
	defer unlock(lock)
	descriptorPath := filepath.Join(runtimeDir(dir), "daemon.json")
	// Remove readiness only after the listener and database have closed.
	defer os.Remove(descriptorPath)
	db, err := store.Open(dir)
	if err != nil {
		return err
	}
	defer db.Close()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	desc := Descriptor{PID: os.Getpid(), URL: "http://" + listener.Addr().String(), Generation: store.NewID(), Token: store.NewID()}
	service, err := core.New(db, harness.Claude{}, desc.URL+"/mcp")
	if err != nil {
		return err
	}
	defer service.Close()
	stop := make(chan struct{}, 1)
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	server := &http.Server{BaseContext: func(net.Listener) context.Context { return serveCtx }, Handler: Handler(db, service, desc, func() {
		select {
		case stop <- struct{}{}:
		default:
		}
	}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	data, err := json.Marshal(desc)
	if err != nil {
		return err
	}
	temp := descriptorPath + ".tmp"
	if err := os.WriteFile(temp, data, 0600); err != nil {
		return err
	}
	if err := os.Rename(temp, descriptorPath); err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-stop:
	case <-ctx.Done():
	}
	cancelServe()
	service.Close()
	shutdown, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelShutdown()
	if err := server.Shutdown(shutdown); err != nil {
		server.Close()
		return fmt.Errorf("daemon shutdown: %w", err)
	}
	return nil
}

func tryLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func unlock(f *os.File)        { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
