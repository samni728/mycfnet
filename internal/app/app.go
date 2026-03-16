package app

import (
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samni728/mycfnet/internal/scanner"
	"github.com/samni728/mycfnet/internal/store"
	"github.com/samni728/mycfnet/internal/web"
)

//go:embed templates/*.html static/*
var assets embed.FS

type Config struct {
	ListenAddr            string
	DBPath                string
	LocationsPath         string
	DefaultCandidatesPath string
	DefaultDomain         string
	DefaultPath           string
	DefaultPort           int
	DefaultConcurrency    int
	DefaultTimeoutMS      int
	DefaultMaxLatencyMS   int
	DefaultSampleSize     int
	DefaultSamplesPerCIDR int
	DefaultUseTLS         bool
	AdminUser             string
	AdminPass             string
}

type App struct {
	cfg      Config
	store    *store.Store
	scanner  *scanner.Scanner
	handlers *web.Handler
}

func New(cfg Config) (*App, error) {
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o755); err != nil {
		return nil, fmt.Errorf("create db dir: %w", err)
	}

	db, err := store.Open(cfg.DBPath)
	if err != nil {
		return nil, err
	}

	locations := map[string]scanner.Location{}
	if _, err := os.Stat(cfg.LocationsPath); err == nil {
		locations, err = scanner.LoadLocations(cfg.LocationsPath)
		if err != nil {
			return nil, err
		}
	}

	tpl := template.New("").Funcs(template.FuncMap{
		"join": strings.Join,
		"since": func(ts time.Time) string {
			if ts.IsZero() {
				return "-"
			}
			return ts.Format("2006-01-02 15:04:05")
		},
	})
	tpl, err = tpl.ParseFS(assets, "templates/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}
	staticFS, err := fs.Sub(assets, "static")
	if err != nil {
		return nil, fmt.Errorf("sub static fs: %w", err)
	}

	s := scanner.New(locations)
	h := web.New(web.Dependencies{
		Store:   db,
		Scanner: s,
		Config: web.Config{
			DefaultCandidatesPath: cfg.DefaultCandidatesPath,
			DefaultDomain:         cfg.DefaultDomain,
			DefaultPath:           cfg.DefaultPath,
			DefaultPort:           cfg.DefaultPort,
			DefaultConcurrency:    cfg.DefaultConcurrency,
			DefaultTimeoutMS:      cfg.DefaultTimeoutMS,
			DefaultMaxLatencyMS:   cfg.DefaultMaxLatencyMS,
			DefaultSampleSize:     cfg.DefaultSampleSize,
			DefaultSamplesPerCIDR: cfg.DefaultSamplesPerCIDR,
			DefaultUseTLS:         cfg.DefaultUseTLS,
			AdminUser:             cfg.AdminUser,
			AdminPass:             cfg.AdminPass,
		},
		Templates: tpl,
		StaticFS:  staticFS,
	})

	return &App{cfg: cfg, store: db, scanner: s, handlers: h}, nil
}

func (a *App) Routes() http.Handler {
	return a.handlers.Routes()
}
