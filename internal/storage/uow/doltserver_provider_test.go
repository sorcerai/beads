package uow

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
	"github.com/steveyegge/beads/internal/storage/doltutil"
	"github.com/steveyegge/beads/internal/storage/schema"
	"github.com/steveyegge/beads/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func shutdownOnInterrupt(t *testing.T, rootDir string) {
	t.Helper()
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		select {
		case <-ch:
			_ = proxy.Shutdown(rootDir)
			os.Exit(1)
		case <-done:
		}
	}()
	t.Cleanup(func() {
		signal.Stop(ch)
		close(done)
	})
}

func TestNewDoltServerUOWProvider_ValidationErrors(t *testing.T) {
	cases := []struct {
		name     string
		database string
		rootUser string
		doltBin  string
		backend  proxy.Backend
		want     string
	}{
		{"empty database", "", "root", "/usr/bin/true", proxy.BackendLocalServer, "database name must not be empty"},
		{"invalid backend", "beads", "root", "/usr/bin/true", proxy.Backend("nope"), "unknown backend"},
		{"empty rootUser", "beads", "", "/usr/bin/true", proxy.BackendLocalServer, "rootUser must not be empty"},
		{"empty doltBin", "beads", "root", "", proxy.BackendLocalServer, "doltBinExec must not be empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewDoltServerUOWProvider(
				context.Background(),
				t.TempDir(),
				tc.database,
				"", "", tc.backend,
				tc.rootUser, "", tc.doltBin,
			)
			assert.Nil(t, p)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestNewDoltServerUOWProvider_HappyPath(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	provider, err := NewDoltServerUOWProvider(
		context.Background(),
		storeRootDir,
		"beads",
		logPath,
		cfgPath,
		proxy.BackendLocalServer,
		"root",
		"",
		bin,
	)

	require.NoError(t, err)
	require.NotNil(t, provider)
	t.Cleanup(func() { _ = provider.Close(context.Background()) })
}

func TestNewDoltServerUOWProvider_ConcurrentInstantiation(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	const concurrency = 10
	type result struct {
		provider UnitOfWorkProvider
		err      error
	}
	results := make([]result, concurrency)

	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		i := i
		go func() {
			defer wg.Done()
			p, err := NewDoltServerUOWProvider(
				context.Background(),
				storeRootDir,
				"beads",
				logPath,
				cfgPath,
				proxy.BackendLocalServer,
				"root",
				"",
				bin,
			)
			results[i] = result{provider: p, err: err}
		}()
	}
	wg.Wait()

	t.Cleanup(func() {
		for _, r := range results {
			if r.provider != nil {
				_ = r.provider.Close(context.Background())
			}
		}
	})

	for i, r := range results {
		assert.NoErrorf(t, r.err, "provider %d", i)
		assert.NotNilf(t, r.provider, "provider %d", i)
	}
}

// TestNewDoltServerUOWProvider_RemoteMigrateGate_BlocksReopen mirrors
// TestDoltNew_RemoteMigrateGate_BlocksReopen for the proxied-server store-open
// path (bd-6dnrw.28): opening a proxied workspace whose database is behind the
// binary AND has a remote persisted on disk must refuse to auto-migrate with a
// *schema.RemoteMigrateGateError instead of silently forking the schema
// (gastownhall/beads#4259/#4268). The proxy/child server is shut down between
// opens so the reopen exercises the cold-start state where dolt_remotes can
// read empty and only the on-disk probe sees the remote (GH#2315).
func TestNewDoltServerUOWProvider_RemoteMigrateGate_BlocksReopen(t *testing.T) {
	testutil.RequireDoltBinary(t)
	bin, err := exec.LookPath("dolt")
	require.NoError(t, err)
	t.Setenv(schema.AllowRemoteMigrateEnv, "0")

	bdBin := buildBDBinary(t)
	prev := proxy.ResolveExecutable
	proxy.ResolveExecutable = func() (string, error) { return bdBin, nil }
	t.Cleanup(func() { proxy.ResolveExecutable = prev })

	t.Setenv("HOME", t.TempDir())

	port, err := proxy.PickFreePort()
	require.NoError(t, err)
	storeRootDir := t.TempDir()
	shutdownOnInterrupt(t, storeRootDir)
	t.Cleanup(func() {
		if err := proxy.Shutdown(storeRootDir); err != nil {
			t.Logf("proxy.Shutdown(%s): %v", storeRootDir, err)
		}
	})
	cfgPath := writeServerConfig(t, port)
	logPath := filepath.Join(t.TempDir(), "server.log")

	ctx := context.Background()
	open := func() (UnitOfWorkProvider, error) {
		return NewDoltServerUOWProvider(
			ctx, storeRootDir, "beads", logPath, cfgPath,
			proxy.BackendLocalServer, "root", "", bin,
		)
	}

	// Fresh database: the gate must not block initial creation/migration.
	provider, err := open()
	require.NoError(t, err)

	// Simulate a database one migration behind this binary by dropping the
	// latest cursor row (the schema change itself stays applied; only the
	// recorded version regresses, so the gate sees a pending migration).
	p, ok := provider.(*doltSQLProvider)
	require.True(t, ok, "provider type = %T", provider)
	_, err = p.db.ExecContext(ctx,
		"DELETE FROM schema_migrations WHERE version = ?", schema.LatestVersion())
	require.NoError(t, err)
	require.NoError(t, provider.Close(ctx))

	// Stop the proxy + child server so the remote can be persisted on disk and
	// the reopen starts from the cold-start state.
	require.NoError(t, proxy.Shutdown(storeRootDir))

	// Persist a remote in the database's .dolt config. The child server may
	// still be releasing its directory lock, so retry briefly.
	dbDir := filepath.Join(storeRootDir, "beads")
	remoteURL := "file://" + filepath.Join(t.TempDir(), "remote")
	require.Eventually(t, func() bool {
		return doltutil.AddCLIRemote(dbDir, "origin", remoteURL) == nil
	}, 10*time.Second, 200*time.Millisecond, "AddCLIRemote(%s)", dbDir)

	// Reopen: behind + remote-backed must refuse to migrate in place.
	blocked, err := open()
	if err == nil {
		_ = blocked.Close(ctx)
		t.Fatal("reopen = nil error, want *schema.RemoteMigrateGateError")
	}
	require.True(t, schema.IsRemoteMigrateGateError(err),
		"reopen error = %T (%v), want error wrapping *schema.RemoteMigrateGateError", err, err)
}

var (
	bdBinaryOnce sync.Once
	bdBinary     string
	bdBinaryErr  error
)

func buildBDBinary(t *testing.T) string {
	t.Helper()
	bdBinaryOnce.Do(func() {
		if prebuilt := os.Getenv("BEADS_TEST_BD_BINARY"); prebuilt != "" {
			if _, err := os.Stat(prebuilt); err != nil {
				bdBinaryErr = fmt.Errorf("BEADS_TEST_BD_BINARY=%q not found: %w", prebuilt, err)
				return
			}
			bdBinary = prebuilt
			return
		}
		tmpDir, err := os.MkdirTemp("", "bd-uow-test-*")
		if err != nil {
			bdBinaryErr = fmt.Errorf("temp dir: %w", err)
			return
		}
		name := "bd"
		if runtime.GOOS == "windows" {
			name = "bd.exe"
		}
		bdBinary = filepath.Join(tmpDir, name)
		cmd := exec.Command("go", "build", "-tags", "gms_pure_go", "-o", bdBinary, "github.com/steveyegge/beads/cmd/bd")
		if out, err := cmd.CombinedOutput(); err != nil {
			bdBinaryErr = fmt.Errorf("go build bd: %v\n%s", err, out)
		}
	})
	if bdBinaryErr != nil {
		t.Fatalf("build bd: %v", bdBinaryErr)
	}
	return bdBinary
}

func writeServerConfig(t *testing.T, port int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	body := fmt.Sprintf("log_level: debug\nlistener:\n  host: 127.0.0.1\n  port: %d\n", port)
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}
