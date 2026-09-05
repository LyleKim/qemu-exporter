package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/digitalocean/go-libvirt"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/LyleKim/qemu-exporter/internal/collector"
	"github.com/LyleKim/qemu-exporter/internal/libvirtsrc"
)

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	listenAddr := flag.String("web.listen-address", ":9179", "address to listen on for telemetry")
	flag.Parse()

	hostProc := envOr("HOST_PROC", "/host/proc")
	hostSysFsCgroup := envOr("HOST_SYS_FS_CGROUP", "/host/sys/fs/cgroup")
	libvirtSock := envOr("LIBVIRT_SOCK", "/var/run/libvirt/libvirt-sock-ro")
	node := os.Getenv("NODE_NAME")
	if node == "" {
		slog.Warn("qemu-exporter: NODE_NAME is empty; the node label will be blank")
	}

	libvirtRunDir := filepath.Dir(libvirtSock)
	connect := func() (*libvirt.Libvirt, error) { return libvirtsrc.Connect(libvirtSock) }
	cache := libvirtsrc.NewCache(connect, hostProc, libvirtRunDir)
	col := collector.New(cache, hostSysFsCgroup, hostProc, node)
	prometheus.MustRegister(col)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	srv := &http.Server{Addr: *listenAddr, Handler: mux}

	go func() {
		slog.Info("qemu-exporter: listening", "address", *listenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("qemu-exporter: server failed", "error", err)
			os.Exit(1)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	<-ctx.Done()

	slog.Info("qemu-exporter: shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("qemu-exporter: graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}
