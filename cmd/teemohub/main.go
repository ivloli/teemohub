package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"teemohub/internal/api"
	"teemohub/internal/config"
	"teemohub/internal/store"
	"teemohub/internal/upstream"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	st, err := store.Open(cfg)
	if err != nil {
		log.Fatalf("store: %v", err)
	}
	defer st.Close()

	up := upstream.New(cfg)
	gateway := api.NewGateway(cfg, st, up)
	control := api.NewControl(cfg, st, up, gateway.Handler())

	controlSrv := &http.Server{
		Addr:              cfg.ControlAddr,
		Handler:           control.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	gatewaySrv := &http.Server{
		Addr:              cfg.GatewayAddr,
		Handler:           gateway.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	errs := make(chan error, 2)
	go func() {
		log.Printf("control listening on http://%s (new api %s)", cfg.ControlAddr, cfg.NewAPIBaseURL)
		if err := controlSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()
	go func() {
		log.Printf("gateway listening on http://%s/v1", cfg.GatewayAddr)
		if err := gatewaySrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- err
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		log.Printf("received %s, shutting down", s)
	case err := <-errs:
		log.Printf("server error: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = controlSrv.Shutdown(ctx)
	_ = gatewaySrv.Shutdown(ctx)
	log.Printf("bye")
}
