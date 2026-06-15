package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dolthub/dolt/go/libraries/doltcore/servercfg"
	"github.com/dolthub/dolt/go/libraries/utils/filesys"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/configfile"
	"github.com/steveyegge/beads/internal/storage/dbproxy/proxy"
)

const (
	proxiedServerRootName   = "proxieddb"
	proxiedServerConfigName = "server_config.yaml"
	proxiedServerLogName    = "server.log"
)

// proxiedServerCommands lists the top-level commands that have a
// proxied-server dispatch path (a usesProxiedServer() branch in their Run
// func). Every other store-backed command reads the global store, which
// stays nil in proxied-server mode — PersistentPreRun rejects them up front
// so they fail with a clear error instead of a nil-pointer panic.
// doctor and init also work in proxied-server mode, but they skip store
// init entirely and never reach the guard.
var proxiedServerCommands = map[string]bool{
	"create": true,
	"list":   true,
}

// commandSupportsProxiedServer reports whether cmd can run in proxied-server
// mode. Subcommands (e.g. "dep add") are judged by their top-level ancestor.
func commandSupportsProxiedServer(cmd *cobra.Command) bool {
	for cmd.Parent() != nil && cmd.Parent().Parent() != nil {
		cmd = cmd.Parent()
	}
	return proxiedServerCommands[cmd.Name()]
}

// proxiedServerInitUngated reports whether the dark-launch gate on
// `bd init --proxied-server` is bypassed for this process. Test-only: the
// proxied integration suites set it so they can bootstrap real proxied
// workspaces while the user-facing init surface stays gated on the open
// bd-6dnrw.44 P1 decisions (TLS, auth).
func proxiedServerInitUngated() bool {
	return os.Getenv("BEADS_TEST_PROXIED_SERVER_INIT") == "1"
}

func proxiedServerRoot(beadsDir string) string {
	return filepath.Join(beadsDir, proxiedServerRootName)
}

func proxiedServerConfigPath(beadsDir string) string {
	return filepath.Join(proxiedServerRoot(beadsDir), proxiedServerConfigName)
}

func proxiedServerLogPath(beadsDir string) string {
	return filepath.Join(proxiedServerRoot(beadsDir), proxiedServerLogName)
}

func envOrAbsJoin(envName, beadsDir string) string {
	p := os.Getenv(envName)
	if p == "" {
		return ""
	}
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(beadsDir, p)
}

func resolveProxiedServerRootPath(beadsDir string) (string, error) {
	if p := envOrAbsJoin("BEADS_PROXIED_SERVER_ROOT_PATH", beadsDir); p != "" {
		return p, nil
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return "", err
	}
	if p := info.ResolvedRootPath(beadsDir); p != "" {
		return p, nil
	}
	return proxiedServerRoot(beadsDir), nil
}

func resolveProxiedServerConfigPath(beadsDir string) (path string, isCustom bool, err error) {
	if p := envOrAbsJoin("BEADS_PROXIED_SERVER_CONFIG", beadsDir); p != "" {
		return p, true, nil
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return "", false, err
	}
	if p := info.ResolvedConfigPath(beadsDir); p != "" {
		return p, true, nil
	}
	root, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(root, proxiedServerConfigName), false, nil
}

func resolveProxiedServerLogPath(beadsDir string) (path string, isCustom bool, err error) {
	if p := envOrAbsJoin("BEADS_PROXIED_SERVER_LOG", beadsDir); p != "" {
		return p, true, nil
	}
	info, err := configfile.LoadProxiedServerClientInfo(beadsDir)
	if err != nil {
		return "", false, err
	}
	if p := info.ResolvedLogPath(beadsDir); p != "" {
		return p, true, nil
	}
	root, err := resolveProxiedServerRootPath(beadsDir)
	if err != nil {
		return "", false, err
	}
	return filepath.Join(root, proxiedServerLogName), false, nil
}

func ensureProxiedServerConfig(beadsDir string) (string, error) {
	path, isCustom, err := resolveProxiedServerConfigPath(beadsDir)
	if err != nil {
		return "", err
	}

	if isCustom {
		info, err := os.Stat(path)
		if err != nil {
			return "", fmt.Errorf("ensureProxiedServerConfig: custom config %s: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("ensureProxiedServerConfig: custom config %s: not a regular file", path)
		}
		if _, err := servercfg.YamlConfigFromFile(filesys.LocalFS, path); err != nil {
			return "", fmt.Errorf("ensureProxiedServerConfig: custom config %s: parse: %w", path, err)
		}
		return path, nil
	}

	root := filepath.Dir(path)
	if err := os.MkdirAll(root, config.BeadsDirPerm); err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: mkdir %s: %w", root, err)
	}

	switch _, err := os.Stat(path); {
	case err == nil:
		return path, nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("ensureProxiedServerConfig: stat %s: %w", path, err)
	}

	port, err := proxy.PickFreePort()
	if err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: pick free port: %w", err)
	}

	body, err := renderProxiedServerConfig(port)
	if err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: render YAML: %w", err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", fmt.Errorf("ensureProxiedServerConfig: write %s: %w", path, err)
	}
	return path, nil
}

// Validators below emit source-neutral errors. Callers wrap with whichever
// label is meaningful at their site: CLI callers prepend the flag name
// (e.g. "--proxied-server-config-path"); runtime callers (uow factory, etc.)
// prepend whatever label fits — the path may have come from env var or
// the proxied_server_client_info.json sidecar, not necessarily a flag.

func validateProxiedServerConfig(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s: not a regular file", path)
	}
	if _, err := servercfg.YamlConfigFromFile(filesys.LocalFS, path); err != nil {
		return fmt.Errorf("%s: parse: %w", path, err)
	}
	return nil
}

func validateProxiedServerRootPath(path string) error {
	switch info, err := os.Stat(path); {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("%s: not a directory", path)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func validateProxiedServerLogPath(path string) error {
	parent := filepath.Dir(path)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return fmt.Errorf("%s: parent directory: %w", path, err)
	}
	if !parentInfo.IsDir() {
		return fmt.Errorf("%s: parent %s is not a directory", path, parent)
	}
	switch info, err := os.Stat(path); {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%s: not a regular file", path)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func renderProxiedServerConfig(port int) ([]byte, error) {
	host := proxiedServerListenerHost
	logLevel := string(servercfg.LogLevel_Info)
	yc := &servercfg.YAMLConfig{
		LogLevelStr: &logLevel,
		ListenerConfig: servercfg.ListenerYAMLConfig{
			HostStr:    &host,
			PortNumber: &port,
		},
	}
	return yaml.Marshal(yc)
}

const proxiedServerListenerHost = "127.0.0.1"
