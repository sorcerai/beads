package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowSupplyChainCheckerRejectsNamedDocumentedAction(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, "npm-package/ACTION_GUIDE.md", "- name: Download artifact\n  uses: actions/download-artifact@v8\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted an unpinned named documented action:\n%s", output)
	}
	if !strings.Contains(string(output), "npm-package/ACTION_GUIDE.md") {
		t.Fatalf("checker did not report the documented action:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsUnverifiedDoltArchiveInstall(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: |\n          curl -fsSL https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz -o /tmp/dolt.tar.gz\n          sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted an unverified Dolt archive install:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsSplitLineDoltArchiveInstall(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: |\n          curl -fsSL \\\n            https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz -o /tmp/dolt.tar.gz\n          sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted a split-line unverified Dolt archive install:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsIntraURLSplitDoltArchiveInstall(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: |\n          curl -fsSL https://github.com/dolthub/dolt/releases/\\\n            download/v2.2.2/dolt-linux-amd64.tar.gz -o /tmp/dolt.tar.gz\n          sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted an intra-URL split Dolt archive install:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsQuotedDoltArchiveInstall(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: |\n          curl -fsSL 'https://github.com/dolthub/dolt/releases/'download/v2.2.2/dolt-linux-amd64.tar.gz -o /tmp/dolt.tar.gz\n          sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted a quoted Dolt archive install:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsMarkdownActionBullets(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, "npm-package/ACTION_GUIDE.md", "* uses: actions/download-artifact@v8\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted an unpinned Markdown action bullet:\n%s", output)
	}
	if !strings.Contains(string(output), "npm-package/ACTION_GUIDE.md") {
		t.Fatalf("checker did not report the documented action:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsFoldedDoltArchiveInstall(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: >-\n          curl -fsSL\n          https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz\n          -o /tmp/dolt.tar.gz\n          && sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted a folded Dolt archive install:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsNamedFoldedDoltArchiveInstall(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - name: Install Dolt\n        run: >-\n          curl -fsSL\n          https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz\n          -o /tmp/dolt.tar.gz\n          && sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted a named folded Dolt archive install:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerChecksFoldedRunBodyLines(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - name: Install npm\n        run: >-\n          npm@latest\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker skipped an unsafe folded run body:\n%s", output)
	}
	if !strings.Contains(string(output), "npm must be pinned to an explicit version") {
		t.Fatalf("checker did not report the folded run body:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsIndentedFoldedDoltArchiveInstall(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - name: Install Dolt\n        run: >2-\n          curl -fsSL\n          https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz\n          -o /tmp/dolt.tar.gz\n          && sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted an indented folded Dolt archive install:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerIgnoresSiblingStepFieldsAfterInlineFoldedRun(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: >-\n          curl -fsSL\n        env:\n          EXAMPLE: 'https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz'\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("checker treated a sibling field as folded shell:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsQuotedSpacedFoldedRunKey(t *testing.T) {
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")

	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - name: Install Dolt\n        \"run\" : >-\n          curl -fsSL\n          https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz\n          -o /tmp/dolt.tar.gz\n          && sudo install /tmp/dolt-linux-amd64/bin/dolt /usr/local/bin/dolt\n")

	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("checker accepted a quoted folded run key:\n%s", output)
	}
	if !strings.Contains(string(output), ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker did not report the unsafe Dolt install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsAdjacentFoldedDoltArchiveInstall(t *testing.T) {
	root := workflowCheckerFixture(t)
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: >-\n          curl -fsSL\n          https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz\n      - run: >-\n          echo safe\n")

	output, err := runWorkflowChecker(t, root)
	if err == nil || !strings.Contains(output, ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker accepted an adjacent folded Dolt archive install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsBlankContinuationDoltArchiveInstall(t *testing.T) {
	root := workflowCheckerFixture(t)
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: >-\n          curl -fsSL \\\n\n          https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz\n")

	output, err := runWorkflowChecker(t, root)
	if err == nil || !strings.Contains(output, ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker accepted a blank continuation Dolt archive install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRejectsRawBlankContinuationDoltArchiveInstall(t *testing.T) {
	root := workflowCheckerFixture(t)
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: |\n          curl -fsSL \\\n\n          https://github.com/dolthub/dolt/releases/download/v2.2.2/dolt-linux-amd64.tar.gz\n")

	output, err := runWorkflowChecker(t, root)
	if err == nil || !strings.Contains(output, ".github/workflows/proxied-local-smoke.yml") {
		t.Fatalf("checker accepted a raw blank continuation Dolt archive install:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRecognizesYAMLUsesKeys(t *testing.T) {
	for _, usesKey := range []string{"uses :", "\"uses\" :"} {
		t.Run(usesKey, func(t *testing.T) {
			root := workflowCheckerFixture(t)
			writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - "+usesKey+" actions/checkout@v6\n")

			output, err := runWorkflowChecker(t, root)
			if err == nil || !strings.Contains(output, ".github/workflows/proxied-local-smoke.yml") {
				t.Fatalf("checker accepted an unpinned YAML uses key:\n%s", output)
			}
		})
	}
}

func TestWorkflowSupplyChainCheckerIgnoresUsesTextInRunValue(t *testing.T) {
	root := workflowCheckerFixture(t)
	writeWorkflowCheckerFixture(t, root, ".github/workflows/proxied-local-smoke.yml", "jobs:\n  smoke:\n    steps:\n      - run: echo uses: actions/checkout@v6\n")

	output, err := runWorkflowChecker(t, root)
	if err != nil {
		t.Fatalf("checker treated a run value as an action:\n%s", output)
	}
}

func TestWorkflowSupplyChainCheckerRecognizesMarkdownActionPrefixes(t *testing.T) {
	for _, prefix := range []string{"1)", "> -"} {
		t.Run(prefix, func(t *testing.T) {
			root := workflowCheckerFixture(t)
			writeWorkflowCheckerFixture(t, root, "npm-package/ACTION_GUIDE.md", prefix+" uses: actions/download-artifact@v8\n")

			output, err := runWorkflowChecker(t, root)
			if err == nil || !strings.Contains(output, "npm-package/ACTION_GUIDE.md") {
				t.Fatalf("checker accepted an unpinned documented action:\n%s", output)
			}
		})
	}
}

func workflowCheckerFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	copyWorkflowCheckerFixture(t, root, "scripts/check-workflow-supply-chain.sh")
	copyWorkflowCheckerFixture(t, root, "scripts/install-dolt-ci.sh")
	writeWorkflowCheckerFixture(t, root, ".github/workflows/update-flake-lock.yml", "jobs:\n  update:\n    if: github.repository == 'gastownhall/beads'\n")
	writeWorkflowCheckerFixture(t, root, "scripts/ci/pr-policy.sh", "./scripts/check-workflow-supply-chain.sh\n")
	return root
}

func runWorkflowChecker(t *testing.T, root string) (string, error) {
	t.Helper()
	for _, args := range [][]string{{"init"}, {"add", "."}} {
		command := exec.Command("git", args...)
		command.Dir = root
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
		}
	}

	command := exec.Command("bash", "scripts/check-workflow-supply-chain.sh")
	command.Dir = root
	output, err := command.CombinedOutput()
	return string(output), err
}

func copyWorkflowCheckerFixture(t *testing.T, root, relativePath string) {
	t.Helper()
	contents, err := os.ReadFile(filepath.Join(sourceRepoRoot(t), relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	writeWorkflowCheckerFixture(t, root, relativePath, string(contents))
	if err := os.Chmod(filepath.Join(root, relativePath), 0o755); err != nil {
		t.Fatalf("chmod %s: %v", relativePath, err)
	}
}

func writeWorkflowCheckerFixture(t *testing.T, root, relativePath, contents string) {
	t.Helper()
	path := filepath.Join(root, relativePath)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create %s directory: %v", relativePath, err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", relativePath, err)
	}
}
