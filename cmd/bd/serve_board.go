package main

import (
	"bytes"
	"context"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/sync/singleflight"
)

const maxBoardJSONBytes = 8 << 20 // 8 MiB stdout cap

type fetchFn func(ctx context.Context) ([]byte, error)

// boardCache: singleflight + TTL + last-good (spec C4/C7).
type boardCache struct {
	ttl   time.Duration
	fetch fetchFn
	sf    singleflight.Group

	mu     sync.Mutex
	good   []byte
	goodAt time.Time
}

func newBoardCache(ttl time.Duration, fetch fetchFn) *boardCache {
	return &boardCache{ttl: ttl, fetch: fetch}
}

// get returns (body, stale, err). stale=true means body is last-good after a
// backend error. err!=nil only when there is no last-good to fall back to.
func (b *boardCache) get(ctx context.Context) ([]byte, bool, error) {
	b.mu.Lock()
	fresh := b.good != nil && time.Since(b.goodAt) < b.ttl
	cached := b.good
	b.mu.Unlock()
	if fresh {
		return cached, false, nil
	}
	// singleflight passes the first caller's ctx to fetch; if that caller
	// cancels, peers share the error. Acceptable for an O(1)-viewer tailnet
	// board (last-good still serves), not worth a detached-fetch rework.
	v, err, _ := b.sf.Do("board", func() (interface{}, error) {
		body, ferr := b.fetch(ctx)
		if ferr != nil {
			return nil, ferr
		}
		b.mu.Lock()
		b.good, b.goodAt = body, time.Now()
		b.mu.Unlock()
		return body, nil
	})
	if err != nil {
		b.mu.Lock()
		good := b.good
		b.mu.Unlock()
		if good != nil {
			return good, true, nil
		}
		return nil, false, err
	}
	return v.([]byte), false, nil
}

// goodTimestamp returns when the last successful fetch completed (zero if
// none yet). The stale banner must show this, not the request time, or it
// defeats spec C7 (operators must not mistake old data for live).
func (b *boardCache) goodTimestamp() time.Time {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.goodAt
}

// execBoardJSON runs `bd board --json` (this same binary), with a hard
// deadline and an output cap. The web process holds no DB credentials.
func execBoardJSON(ctx context.Context, timeout time.Duration) ([]byte, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve self: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, self, "board", "--json")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start bd board: %w", err)
	}
	// Bound memory on the shared host (spec C6): never buffer more than the
	// cap, even if the child misbehaves. Read at most cap+1; if we hit that,
	// the output is over-large — kill the child now rather than wait out the
	// timeout. Read fully before Wait (StdoutPipe contract).
	var out bytes.Buffer
	n, copyErr := io.Copy(&out, io.LimitReader(stdout, maxBoardJSONBytes+1))
	if n > maxBoardJSONBytes {
		cancel()
		_ = cmd.Wait()
		return nil, fmt.Errorf("board json exceeds %d bytes", maxBoardJSONBytes)
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		msg := errBuf.String()
		if len(msg) > 2000 {
			msg = msg[:2000] + "…"
		}
		return nil, fmt.Errorf("bd board --json failed: %w (stderr: %s)", waitErr, msg)
	}
	if copyErr != nil {
		return nil, fmt.Errorf("reading bd board output: %w", copyErr)
	}
	return out.Bytes(), nil
}

var boardPageTmpl = template.Must(template.New("board").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>Beads Board</title>
<meta http-equiv="refresh" content="{{.Refresh}}">
<style>body{background:#0d1117;color:#c9d1d9;font:14px/1.5 system-ui;margin:0;padding:16px}
.banner{background:#7d1d1d;color:#fff;padding:8px 12px;border-radius:6px;margin-bottom:12px}
pre{white-space:pre-wrap;word-break:break-word}</style></head>
<body>
{{if .Stale}}<div class="banner">stale — backend error (last good {{.GoodAt}})</div>{{end}}
<pre>{{.JSON}}</pre>
</body></html>`))

func serveBoard(addr string, refreshSec int, ttl, timeout time.Duration) error {
	cache := newBoardCache(ttl, func(ctx context.Context) ([]byte, error) {
		return execBoardJSON(ctx, timeout)
	})
	sema := make(chan struct{}, 8) // bounded concurrency (spec C4)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		select {
		case sema <- struct{}{}:
			defer func() { <-sema }()
		default:
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		body, stale, err := cache.get(r.Context())
		if err != nil {
			http.Error(w, "board unavailable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = boardPageTmpl.Execute(w, map[string]any{
			"JSON": string(body), "Stale": stale,
			"Refresh": refreshSec, "GoodAt": cache.goodTimestamp().UTC().Format(time.RFC3339),
		})
	})
	srv := &http.Server{
		Addr: addr, Handler: mux,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      30 * time.Second,
	}
	return srv.ListenAndServe()
}

var serveBoardCmd = &cobra.Command{
	Use:   "serve-board",
	Short: "Serve the read-only project board over HTTP (tailnet-only)",
	Long: `Serves a read-only HTML board. Holds NO database credentials: it
execs 'bd board --json' behind a singleflight+TTL cache. Bind to a tailnet
IP only; never a public interface.`,
	RunE: func(cmd *cobra.Command, _ []string) error {
		addr, _ := cmd.Flags().GetString("addr")
		refresh, _ := cmd.Flags().GetInt("refresh")
		ttlSec, _ := cmd.Flags().GetInt("cache-ttl")
		timeoutSec, _ := cmd.Flags().GetInt("exec-timeout")
		if addr == "" {
			return fmt.Errorf("--addr is required (tailnet IP:port, e.g. 100.x.y.z:8099)")
		}
		fmt.Printf("serving board on http://%s (refresh=%ds ttl=%ds)\n", addr, refresh, ttlSec)
		return serveBoard(addr, refresh,
			time.Duration(ttlSec)*time.Second, time.Duration(timeoutSec)*time.Second)
	},
}

func init() {
	serveBoardCmd.Flags().String("addr", "", "Tailnet bind address, e.g. 100.x.y.z:8099 (required)")
	serveBoardCmd.Flags().Int("refresh", 30, "Browser auto-refresh seconds (spec: >=15)")
	serveBoardCmd.Flags().Int("cache-ttl", 20, "Server cache TTL seconds (<= refresh)")
	serveBoardCmd.Flags().Int("exec-timeout", 10, "Hard timeout for 'bd board --json' seconds")
	rootCmd.AddCommand(serveBoardCmd)
}
