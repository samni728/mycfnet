package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/samni728/mycfnet/internal/app"
)

func main() {
	var cfg app.Config

	_ = godotenv.Load()

	flag.StringVar(&cfg.ListenAddr, "listen", envString("MYCFNET_LISTEN_ADDR", ":8080"), "HTTP listen address")
	flag.StringVar(&cfg.DBPath, "db", "data/mycfnet.db", "SQLite database path")
	flag.StringVar(&cfg.LocationsPath, "locations", "data/locations.json", "locations metadata file")
	flag.StringVar(&cfg.DefaultCandidatesPath, "candidates", "data/ips-v4.txt", "default candidate list file")
	flag.StringVar(&cfg.DefaultDomain, "domain", "cloudflaremirrors.com", "default Cloudflare-proxied domain")
	flag.StringVar(&cfg.DefaultPath, "path", "/debian", "default request path")
	flag.IntVar(&cfg.DefaultPort, "port", 443, "default probe port")
	flag.IntVar(&cfg.DefaultConcurrency, "concurrency", 32, "default scan worker concurrency")
	flag.IntVar(&cfg.DefaultTimeoutMS, "timeout-ms", 3000, "default network timeout in milliseconds")
	flag.IntVar(&cfg.DefaultMaxLatencyMS, "max-latency-ms", 300, "default max latency in milliseconds")
	flag.IntVar(&cfg.DefaultSampleSize, "sample-size", 256, "default candidate sample size")
	flag.IntVar(&cfg.DefaultSamplesPerCIDR, "samples-per-cidr", 32, "default random samples per CIDR entry")
	flag.BoolVar(&cfg.DefaultUseTLS, "tls", true, "default TLS probing")
	flag.StringVar(&cfg.AdminUser, "admin-user", envString("MYCFNET_ADMIN_USER", ""), "admin username for web ui")
	flag.StringVar(&cfg.AdminPass, "admin-pass", envString("MYCFNET_ADMIN_PASS", ""), "admin password for web ui")
	flag.Parse()

	svc, err := app.New(cfg)
	if err != nil {
		log.Fatalf("init app: %v", err)
	}

	server := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           svc.Routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("mycfnet listening on %s", cfg.ListenAddr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
