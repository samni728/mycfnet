package web

import (
	"context"
	"encoding/csv"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/samni728/mycfnet/internal/scanner"
	"github.com/samni728/mycfnet/internal/store"
)

type Config struct {
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
}

type Dependencies struct {
	Store     *store.Store
	Scanner   *scanner.Scanner
	Config    Config
	Templates *template.Template
	StaticFS  fs.FS
}

type Handler struct {
	store     *store.Store
	scanner   *scanner.Scanner
	cfg       Config
	templates *template.Template
	staticFS  fs.FS

	mu     sync.Mutex
	active map[int64]context.CancelFunc
}

type pageData struct {
	Config      Config
	Results     []store.Result
	Jobs        []store.Job
	Candidates  []string
	Notice      string
	ActiveCount int
}

func New(dep Dependencies) *Handler {
	return &Handler{
		store:     dep.Store,
		scanner:   dep.Scanner,
		cfg:       dep.Config,
		templates: dep.Templates,
		staticFS:  dep.StaticFS,
		active:    make(map[int64]context.CancelFunc),
	}
}

func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServerFS(h.staticFS)))
	mux.HandleFunc("/", h.handleIndex)
	mux.HandleFunc("/scan", h.handleStartScan)
	mux.HandleFunc("/export.csv", h.handleExportCSV)
	mux.HandleFunc("/export.txt", h.handleExportTXT)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return mux
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	results, err := h.store.ListResults(ctx, store.ResultFilter{Limit: 200})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jobs, err := h.store.ListJobs(ctx, 20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	activeCount := 0
	for _, item := range results {
		if item.Active {
			activeCount++
		}
	}

	candidates, _ := guessCandidateFiles(filepath.Dir(h.cfg.DefaultCandidatesPath))
	data := pageData{
		Config:      h.cfg,
		Results:     results,
		Jobs:        jobs,
		Candidates:  candidates,
		Notice:      r.URL.Query().Get("notice"),
		ActiveCount: activeCount,
	}
	if err := h.templates.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func (h *Handler) handleStartScan(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	opts := scanner.ScanOptions{
		CandidatesPath: mustString(r.FormValue("candidates_path"), h.cfg.DefaultCandidatesPath),
		Domain:         mustString(r.FormValue("domain"), h.cfg.DefaultDomain),
		Path:           mustString(r.FormValue("path"), h.cfg.DefaultPath),
		Port:           mustInt(r.FormValue("port"), h.cfg.DefaultPort),
		UseTLS:         mustBool(r.FormValue("use_tls"), h.cfg.DefaultUseTLS),
		ExpectedStatus: mustInt(r.FormValue("expected_status"), http.StatusOK),
		Timeout:        time.Duration(mustInt(r.FormValue("timeout_ms"), h.cfg.DefaultTimeoutMS)) * time.Millisecond,
		MaxLatency:     time.Duration(mustInt(r.FormValue("max_latency_ms"), h.cfg.DefaultMaxLatencyMS)) * time.Millisecond,
		Concurrency:    mustInt(r.FormValue("concurrency"), h.cfg.DefaultConcurrency),
		SampleSize:     mustInt(r.FormValue("sample_size"), h.cfg.DefaultSampleSize),
		SamplesPerCIDR: mustInt(r.FormValue("samples_per_cidr"), h.cfg.DefaultSamplesPerCIDR),
		AllowedColos:   splitCSV(r.FormValue("allowed_colos")),
	}

	if _, err := os.Stat(opts.CandidatesPath); err != nil {
		http.Error(w, fmt.Sprintf("candidate file not found: %s", opts.CandidatesPath), http.StatusBadRequest)
		return
	}

	job := store.Job{
		Status:         "running",
		CandidatesPath: opts.CandidatesPath,
		Domain:         opts.Domain,
		Path:           opts.Path,
		Port:           opts.Port,
		UseTLS:         opts.UseTLS,
		AllowedColos:   opts.AllowedColos,
		StartedAt:      time.Now(),
	}
	jobID, err := h.store.CreateJob(r.Context(), job)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	h.mu.Lock()
	h.active[jobID] = cancel
	h.mu.Unlock()

	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.active, jobID)
			h.mu.Unlock()
		}()

		var finalErr error
		err := h.scanner.Scan(ctx, opts, func(item scanner.CandidateResult) {
			upsertErr := h.store.UpsertResult(context.Background(), store.Result{
				IP:         item.IP,
				Port:       item.Port,
				Domain:     item.Domain,
				Colo:       item.Colo,
				City:       item.City,
				Region:     item.Region,
				LatencyMS:  int(item.Latency / time.Millisecond),
				HTTPStatus: item.HTTPStatus,
				Active:     item.Active,
				LastError:  item.LastError,
				LastSeenAt: time.Now(),
			})
			if upsertErr != nil && finalErr == nil {
				finalErr = upsertErr
			}
		}, func(progress scanner.Progress) {
			if finalErr == nil {
				finalErr = h.store.UpdateJobProgress(context.Background(), jobID, progress.Processed, progress.Success, progress.Total)
			}
		})

		status := "completed"
		errText := ""
		switch {
		case ctx.Err() != nil:
			status = "cancelled"
			errText = ctx.Err().Error()
		case err != nil && err != context.Canceled:
			status = "failed"
			errText = err.Error()
		case finalErr != nil:
			status = "failed"
			errText = finalErr.Error()
		}
		_ = h.store.FinishJob(context.Background(), jobID, status, errText)
	}()

	http.Redirect(w, r, "/?notice=scan+started", http.StatusSeeOther)
}

func (h *Handler) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	results, err := h.store.ListResults(r.Context(), buildFilter(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=mycfnet-results.csv")
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"ip", "port", "domain", "colo", "city", "region", "latency_ms", "status", "active", "last_seen_at"})
	for _, item := range results {
		_ = cw.Write([]string{
			item.IP,
			strconv.Itoa(item.Port),
			item.Domain,
			item.Colo,
			item.City,
			item.Region,
			strconv.Itoa(item.LatencyMS),
			strconv.Itoa(item.HTTPStatus),
			strconv.FormatBool(item.Active),
			item.LastSeenAt.Format(time.RFC3339),
		})
	}
	cw.Flush()
}

func (h *Handler) handleExportTXT(w http.ResponseWriter, r *http.Request) {
	results, err := h.store.ListResults(r.Context(), buildFilter(r))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=mycfnet-results.txt")
	for _, item := range results {
		if !item.Active {
			continue
		}
		_, _ = fmt.Fprintf(w, "%s:%d\n", item.IP, item.Port)
	}
}

func buildFilter(r *http.Request) store.ResultFilter {
	filter := store.ResultFilter{
		Colo:  strings.TrimSpace(r.URL.Query().Get("colo")),
		City:  strings.TrimSpace(r.URL.Query().Get("city")),
		Limit: mustInt(r.URL.Query().Get("limit"), 500),
	}
	if raw := r.URL.Query().Get("active"); raw != "" {
		v := mustBool(raw, true)
		filter.Active = &v
	}
	return filter
}

func guessCandidateFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if strings.Contains(name, "ips") && strings.HasSuffix(name, ".txt") {
			out = append(out, filepath.Join(dir, entry.Name()))
		}
	}
	return out, nil
}

func mustString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func mustInt(v string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
		return n
	}
	return def
}

func mustBool(v string, def bool) bool {
	if v == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	default:
		return def
	}
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, strings.ToUpper(part))
		}
	}
	return out
}
