package scanner

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	mathrand "math/rand"
	"net"
	"net/http"
	"net/netip"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

type Location struct {
	IATA   string  `json:"iata"`
	Lat    float64 `json:"lat"`
	Lon    float64 `json:"lon"`
	CCA2   string  `json:"cca2"`
	Region string  `json:"region"`
	City   string  `json:"city"`
}

type ScanOptions struct {
	CandidatesPath string
	Domain         string
	Path           string
	Port           int
	UseTLS         bool
	ExpectedStatus int
	Timeout        time.Duration
	MaxLatency     time.Duration
	Concurrency    int
	SampleSize     int
	SamplesPerCIDR int
	AllowedColos   []string
}

type CandidateResult struct {
	IP         string
	Port       int
	Domain     string
	HTTPStatus int
	Latency    time.Duration
	Colo       string
	City       string
	Region     string
	Active     bool
	LastError  string
}

type Progress struct {
	Total     int
	Processed int
	Success   int
}

type Scanner struct {
	locations map[string]Location
}

func LoadLocations(path string) (map[string]Location, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read locations: %w", err)
	}
	var list []Location
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("parse locations: %w", err)
	}
	out := make(map[string]Location, len(list))
	for _, loc := range list {
		out[strings.ToUpper(loc.IATA)] = loc
	}
	return out, nil
}

func New(locations map[string]Location) *Scanner {
	return &Scanner{locations: locations}
}

func (s *Scanner) Scan(ctx context.Context, opts ScanOptions, onResult func(CandidateResult), onProgress func(Progress)) error {
	candidates, err := loadCandidates(opts.CandidatesPath, opts.SampleSize, opts.SamplesPerCIDR)
	if err != nil {
		return err
	}
	if len(candidates) == 0 {
		return fmt.Errorf("no candidates loaded from %s", opts.CandidatesPath)
	}

	coloFilter := map[string]struct{}{}
	for _, colo := range opts.AllowedColos {
		colo = strings.TrimSpace(strings.ToUpper(colo))
		if colo != "" {
			coloFilter[colo] = struct{}{}
		}
	}

	type item struct{ ip string }
	jobs := make(chan item)
	results := make(chan CandidateResult)

	workers := opts.Concurrency
	if workers < 1 {
		workers = 1
	}

	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for candidate := range jobs {
				result := s.probe(ctx, candidate.ip, opts)
				if len(coloFilter) > 0 && result.Colo != "" {
					if _, ok := coloFilter[strings.ToUpper(result.Colo)]; !ok {
						result.Active = false
						if result.LastError == "" {
							result.LastError = "colo filtered out"
						}
						result.City = ""
						result.Region = ""
					}
				}
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, ip := range candidates {
			select {
			case jobs <- item{ip: ip}:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(results)
	}()

	progress := Progress{Total: len(candidates)}
	for result := range results {
		progress.Processed++
		if result.Active {
			progress.Success++
		}
		if onResult != nil {
			onResult(result)
		}
		if onProgress != nil {
			onProgress(progress)
		}
	}

	return ctx.Err()
}

func (s *Scanner) probe(ctx context.Context, ip string, opts ScanOptions) CandidateResult {
	result := CandidateResult{
		IP:     ip,
		Port:   opts.Port,
		Domain: opts.Domain,
	}

	target := net.JoinHostPort(ip, fmt.Sprintf("%d", opts.Port))
	dialer := &net.Dialer{Timeout: opts.Timeout}
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		result.LastError = err.Error()
		return result
	}
	result.Latency = time.Since(start)
	_ = conn.Close()
	if opts.MaxLatency > 0 && result.Latency > opts.MaxLatency {
		result.LastError = "latency exceeded"
		return result
	}

	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, target)
		},
		TLSClientConfig: &tls.Config{
			ServerName:         opts.Domain,
			InsecureSkipVerify: false,
		},
		ForceAttemptHTTP2: false,
	}
	defer transport.CloseIdleConnections()

	scheme := "http"
	if opts.UseTLS {
		scheme = "https"
	}
	url := fmt.Sprintf("%s://%s%s", scheme, opts.Domain, opts.Path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		result.LastError = err.Error()
		return result
	}
	req.Host = opts.Domain
	req.Header.Set("User-Agent", "mycfnet/0.1")

	client := &http.Client{
		Transport: transport,
		Timeout:   opts.Timeout,
	}
	resp, err := client.Do(req)
	if err != nil {
		result.LastError = err.Error()
		return result
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	result.HTTPStatus = resp.StatusCode
	if opts.ExpectedStatus > 0 && resp.StatusCode != opts.ExpectedStatus {
		result.LastError = fmt.Sprintf("unexpected status %d", resp.StatusCode)
		return result
	}

	if cfRay := resp.Header.Get("CF-RAY"); cfRay != "" {
		parts := strings.Split(cfRay, "-")
		result.Colo = strings.ToUpper(parts[len(parts)-1])
		if loc, ok := s.locations[result.Colo]; ok {
			result.City = loc.City
			result.Region = loc.Region
		}
	}
	result.Active = true
	return result
}

func loadCandidates(path string, sampleSize, samplesPerCIDR int) ([]string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read candidates: %w", err)
	}
	lines := strings.Split(string(b), "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.Contains(line, "/") {
			sampled, err := sampleCIDR(line, samplesPerCIDR)
			if err != nil {
				continue
			}
			out = append(out, sampled...)
			continue
		}
		if addr, err := netip.ParseAddr(line); err == nil {
			out = append(out, addr.String())
		}
	}
	shuffle(out)
	if sampleSize > 0 && len(out) > sampleSize {
		out = out[:sampleSize]
	}
	sort.Strings(out)
	return dedupe(out), nil
}

func sampleCIDR(raw string, samples int) ([]string, error) {
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return nil, err
	}
	if samples < 1 {
		samples = 1
	}
	base := prefix.Masked().Addr()
	if !base.IsValid() {
		return nil, fmt.Errorf("invalid prefix %s", raw)
	}

	ones := prefix.Bits()
	bits := 128
	if base.Is4() {
		bits = 32
	}
	hostBits := bits - ones
	if hostBits <= 0 {
		return []string{base.String()}, nil
	}

	maxExact := 1 << min(hostBits, 10)
	if base.Is4() && hostBits <= 10 && samples >= maxExact {
		out := make([]string, 0, maxExact)
		b := base.As4()
		start := binary.BigEndian.Uint32(b[:])
		for i := 0; i < maxExact; i++ {
			var b [4]byte
			binary.BigEndian.PutUint32(b[:], start+uint32(i))
			out = append(out, netip.AddrFrom4(b).String())
		}
		return out, nil
	}

	out := make([]string, 0, samples)
	for i := 0; i < samples; i++ {
		addr, err := randomAddrInPrefix(prefix)
		if err != nil {
			return nil, err
		}
		out = append(out, addr.String())
	}
	return out, nil
}

func randomAddrInPrefix(prefix netip.Prefix) (netip.Addr, error) {
	base := prefix.Masked().Addr()
	hostBits := 128 - prefix.Bits()
	if base.Is4() {
		hostBits = 32 - prefix.Bits()
		var b [4]byte
		b = base.As4()
		if hostBits > 0 {
			mask := uint32(1<<hostBits) - 1
			var rnd [4]byte
			if _, err := rand.Read(rnd[:]); err != nil {
				return netip.Addr{}, err
			}
			cur := binary.BigEndian.Uint32(b[:])
			cur |= binary.BigEndian.Uint32(rnd[:]) & mask
			binary.BigEndian.PutUint32(b[:], cur)
		}
		return netip.AddrFrom4(b), nil
	}

	b := base.As16()
	if hostBits > 0 {
		var rnd [16]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return netip.Addr{}, err
		}
		hostBytes := hostBits / 8
		hostRemainder := hostBits % 8
		for i := 15; i >= 16-hostBytes; i-- {
			b[i] = rnd[i]
		}
		if hostRemainder > 0 {
			idx := 16 - hostBytes - 1
			mask := byte((1 << hostRemainder) - 1)
			b[idx] = (b[idx] & ^mask) | (rnd[idx] & mask)
		}
	}
	return netip.AddrFrom16(b), nil
}

func shuffle(items []string) {
	r := mathrand.New(mathrand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(items), func(i, j int) {
		items[i], items[j] = items[j], items[i]
	})
}

func dedupe(items []string) []string {
	if len(items) == 0 {
		return items
	}
	out := items[:1]
	for _, item := range items[1:] {
		if item != out[len(out)-1] {
			out = append(out, item)
		}
	}
	return out
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
