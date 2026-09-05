package scout

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// IgnoreFileName is the repo-relative path of the optional user ignore file.
const IgnoreFileName = ".beads/codemap-ignore"

// builtinDirs are directory names skipped wherever they appear as a path
// segment. They hold code that is vendored, fixture-only, or not ours.
var builtinDirs = []string{"vendor", "testdata", "node_modules", ".git", ".beads"}

// builtinGlobs are file-name patterns for machine-written code.
var builtinGlobs = []string{"*.pb.go", "*_generated.go", "*.gen.go"}

// Ignorer decides which repo-relative paths a scout must not index.
type Ignorer struct{ patterns []string }

// LoadIgnorer returns an Ignorer carrying the builtin rules plus any patterns
// in root's .beads/codemap-ignore (gitignore-flavored: one pattern per line,
// '#' comments, blank lines ignored). A missing file is not an error.
func LoadIgnorer(root string) (*Ignorer, error) {
	ig := &Ignorer{}
	// #nosec G304 -- fixed repo-relative name under a caller-supplied repo root
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(IgnoreFileName)))
	if err != nil {
		if os.IsNotExist(err) {
			return ig, nil
		}
		return nil, err
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		ig.patterns = append(ig.patterns, strings.TrimPrefix(line, "/"))
	}
	return ig, sc.Err()
}

// Skip reports whether relPath (repo-relative, forward slashes) is excluded.
// A trailing slash marks the path as a directory; either form works.
func (i *Ignorer) Skip(relPath string) bool {
	rel := strings.Trim(filepath.ToSlash(relPath), "/")
	if rel == "" || rel == "." {
		return false
	}
	segments := strings.Split(rel, "/")
	base := segments[len(segments)-1]
	for _, seg := range segments {
		for _, d := range builtinDirs {
			if seg == d {
				return true
			}
		}
	}
	for _, g := range builtinGlobs {
		if ok, _ := path.Match(g, base); ok {
			return true
		}
	}
	for _, p := range i.patterns {
		if strings.HasSuffix(p, "/") {
			dir := strings.TrimSuffix(p, "/")
			for _, seg := range segments {
				if seg == dir {
					return true
				}
			}
			continue
		}
		if ok, _ := path.Match(p, rel); ok {
			return true
		}
		if ok, _ := path.Match(p, base); ok {
			return true
		}
	}
	return false
}

var generatedRE = regexp.MustCompile(`^// Code generated .* DO NOT EDIT\.$`)

// IsGenerated reports whether head carries the conventional "Code generated
// ... DO NOT EDIT." banner within its first kibibyte.
func IsGenerated(head []byte) bool {
	if len(head) > 1024 {
		head = head[:1024]
	}
	for _, line := range strings.Split(string(head), "\n") {
		if generatedRE.MatchString(strings.TrimRight(line, "\r")) {
			return true
		}
	}
	return false
}
