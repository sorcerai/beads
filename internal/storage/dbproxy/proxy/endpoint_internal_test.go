package proxy

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/pidfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/server"
	"github.com/steveyegge/beads/internal/storage/dbproxy/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// deadPid returns the pid of a process that has already exited and been
// reaped, i.e. a pid that pidAlive must report dead.
func deadPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	require.NoError(t, cmd.Start())
	pid := cmd.Process.Pid
	require.NoError(t, cmd.Wait())
	return pid
}

// startSleeper starts a long-running child process and arranges for it to be
// reaped as soon as it dies, so pidAlive doesn't see a lingering zombie.
func startSleeper(t *testing.T) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "60")
	require.NoError(t, cmd.Start())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = cmd.Wait()
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})
	return cmd
}

func skipOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("test scaffolding uses unix coreutils (true/sleep)")
	}
}

func TestPidAlive(t *testing.T) {
	skipOnWindows(t)
	assert.True(t, pidAlive(os.Getpid()))
	assert.False(t, pidAlive(deadPid(t)))
}

func TestReadAndDial_DeadPidfileWriterRejected(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()

	// A listener really is up on the recorded port — the bare TCP probe
	// would succeed. The dead writer pid alone must disqualify the pidfile.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{Pid: deadPid(t), Port: port}))
	_, _, ok := readAndDial(root)
	assert.False(t, ok, "stale pidfile (dead writer) must not be trusted even with a live listener")

	require.NoError(t, pidfile.Write(root, PIDFileName, pidfile.PidFile{Pid: os.Getpid(), Port: port}))
	ep, pf, ok := readAndDial(root)
	require.True(t, ok)
	assert.Equal(t, port, ep.Port)
	assert.Equal(t, os.Getpid(), pf.Pid)
}

func TestReapPidfileProcess_KillsLiveOrphan(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()

	orphan := startSleeper(t)
	require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{Pid: orphan.Process.Pid}))

	reapPidfileProcess(root, server.PIDFileName)

	assert.False(t, pidAlive(orphan.Process.Pid), "orphan should be dead after reap")
	pf, err := pidfile.Read(root, server.PIDFileName)
	require.NoError(t, err)
	assert.Nil(t, pf, "pidfile should be removed after reap")
}

func TestReapPidfileProcess_StalePidfileJustRemoved(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()

	require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{Pid: deadPid(t)}))

	start := time.Now()
	reapPidfileProcess(root, server.PIDFileName)
	assert.Less(t, time.Since(start), reapConfirmDeadline, "dead pid must not be waited on")

	pf, err := pidfile.Read(root, server.PIDFileName)
	require.NoError(t, err)
	assert.Nil(t, pf)
}

func TestIntendedUpstreamID_PerBackend(t *testing.T) {
	root := t.TempDir()
	ext := configfile.ExternalDoltConfig{Host: "10.0.0.1", Port: 3306}

	assert.Equal(t, server.LocalDoltServerID(root),
		intendedUpstreamID(root, OpenOpts{Backend: BackendLocalServer}))
	assert.Equal(t, server.ExternalDoltServerID(ext),
		intendedUpstreamID(root, OpenOpts{Backend: BackendExternal, External: ext}))
	assert.Empty(t, intendedUpstreamID(root, OpenOpts{Backend: BackendLocalSharedServer}))
}

// A SIGKILLed proxy-child releases proxy-child.lock (flocks die with their
// holder) but leaves its dolt sql-server running. spawnAndHandoff must reap
// that orphan even though the child flock is acquirable.
func TestSpawnAndHandoff_ReapsOrphanWhenChildLockFree(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()

	orphan := startSleeper(t)
	require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{Pid: orphan.Process.Pid}))

	// Spawn a child that exits immediately so spawnAndHandoff returns fast;
	// the orphan reap happens before the fork.
	falseBin, err := exec.LookPath("false")
	require.NoError(t, err)
	prev := ResolveExecutable
	ResolveExecutable = func() (string, error) { return falseBin, nil }
	t.Cleanup(func() { ResolveExecutable = prev })

	lock, err := util.TryLock(filepath.Join(root, LockFileName))
	require.NoError(t, err)

	_, spawnErr := spawnAndHandoff(root, OpenOpts{
		Backend:     BackendLocalServer,
		LogFilePath: filepath.Join(root, "proxy-spawn.log"),
	}, time.Now().Add(2*time.Second), lock)
	require.Error(t, spawnErr, "child exits immediately, so spawn must fail")

	assert.False(t, pidAlive(orphan.Process.Pid), "orphaned dolt must be killed before respawn")
	pf, err := pidfile.Read(root, server.PIDFileName)
	require.NoError(t, err)
	assert.Nil(t, pf, "orphan pidfile should be removed")
}

// Shutdown promises no surviving processes even when the lock holder died
// without cleaning up its child (the orphaned-dolt case).
func TestShutdown_ReapsOrphanWhenLockFree(t *testing.T) {
	skipOnWindows(t)
	root := t.TempDir()

	orphan := startSleeper(t)
	require.NoError(t, pidfile.Write(root, server.PIDFileName, pidfile.PidFile{Pid: orphan.Process.Pid}))

	require.NoError(t, Shutdown(root))

	assert.False(t, pidAlive(orphan.Process.Pid), "orphan should be dead after Shutdown")
	pf, err := pidfile.Read(root, server.PIDFileName)
	require.NoError(t, err)
	assert.Nil(t, pf)
}
