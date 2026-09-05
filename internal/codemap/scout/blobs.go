package scout

import (
	"bytes"
	"fmt"
	"os/exec"
	"path"
	"sort"
)

// BlobHashes returns the staged git blob sha for every tracked file in root,
// keyed by repo-relative path. One `git ls-files` call serves a whole scan.
func BlobHashes(root string) (map[string]string, error) {
	cmd := exec.Command("git", "ls-files", "-s", "-z")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	hashes := map[string]string{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		// "<mode> <sha> <stage>\t<path>"
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		fields := bytes.Fields(rec[:tab])
		if len(fields) < 2 {
			continue
		}
		hashes[string(rec[tab+1:])] = string(fields[1])
	}
	return hashes, nil
}

// PackageDirsFor returns the sorted, unique parent directories of changed —
// the coarsest scan scope that still covers every changed file. The result is
// repo-relative directories, which is what a scout's Scan accepts for `only`
// alongside that language's own package identifiers.
func PackageDirsFor(changed []string) []string {
	set := map[string]struct{}{}
	for _, f := range changed {
		set[path.Dir(f)] = struct{}{}
	}
	dirs := make([]string, 0, len(set))
	for d := range set {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	return dirs
}
