#!/usr/bin/env bash
# Reject unpinned or privilege-escalating dependency installation in CI workflows.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

fail=0

report() {
    printf 'error: %s:%s: %s\n' "$1" "$2" "$3" >&2
    fail=1
}

for workflow in .github/workflows/*.yml .github/workflows/*.yaml; do
    [[ -f "$workflow" ]] || continue
    line_no=0
    while IFS= read -r line || [[ -n "$line" ]]; do
        line_no=$((line_no + 1))
        [[ "$line" =~ ^[[:space:]]*# ]] && continue

        if [[ "$line" == *"dolthub/dolt/releases/latest"* && "$line" == *"|"* && \
              "$line" =~ sudo[[:space:]]+(ba)?sh ]]; then
            report "$workflow" "$line_no" "Dolt releases/latest must not be piped to a privileged shell"
        fi
        if [[ "$line" == *"github.com/tc-hib/go-winres@latest"* ]]; then
            report "$workflow" "$line_no" "go-winres must be pinned to an immutable version"
        fi
        if [[ "$line" == *"npm@latest"* ]]; then
            report "$workflow" "$line_no" "npm must be pinned to an explicit version"
        fi
        if [[ "$line" =~ uv[[:space:]]+tool[[:space:]]+run[[:space:]]+twine([[:space:]]|$) ]]; then
            report "$workflow" "$line_no" "twine must be pinned with an explicit package version"
        fi
        if [[ "$line" =~ pip([0-9.]*)?[[:space:]]+install[[:space:]]+uv([=[:space:]]|$) ]]; then
            report "$workflow" "$line_no" "uv must be installed through the checksum-verifying pinned setup-uv action"
        fi
        if [[ "$line" =~ (^|[[:space:]-])uses:[[:space:]]*([^[:space:]#]+) ]]; then
            action_ref="${BASH_REMATCH[2]}"
            if [[ "$action_ref" != ./* && ! "$action_ref" =~ @[0-9a-f]{40}$ ]]; then
                report "$workflow" "$line_no" "third-party actions must be pinned to a full commit SHA"
            fi
        fi
        if [[ "$line" =~ ^[[:space:]]*version: ]]; then
            version_value="${line#*:}"
            version_value="${version_value// /}"
            version_value="${version_value//$'\t'/}"
            version_value="${version_value//\"/}"
            version_value="${version_value//\'/}"
            case "$version_value" in
                latest|*'~'*|*'^'*|\**|*'<'*|*'>'*)
                    report "$workflow" "$line_no" "downloaded CI tools must use an exact version"
                    ;;
            esac
        fi
    done < "$workflow"
done


check_pr_permissions() {
    local workflow="$1"
    local line
    local line_no=0
    local permissions_block=no
    local contents_read=no
    local value

    while IFS= read -r line || [[ -n "$line" ]]; do
        line_no=$((line_no + 1))
        [[ "$line" =~ ^[[:space:]]*# ]] && continue

        if [[ "$line" == "permissions:" ]]; then
            permissions_block=yes
            continue
        fi

        if [[ "$permissions_block" == yes ]]; then
            if [[ "$line" =~ ^[^[:space:]] && "$line" != "permissions:" ]]; then
                permissions_block=no
            elif [[ "$line" =~ ^[[:space:]]{2}contents:[[:space:]] ]]; then
                value="${line#*:}"
                value="${value%%#*}"
                value="${value// /}"
                value="${value//$'\t'/}"
                value="${value//\"/}"
                value="${value//\'/}"
                if [[ "$value" == read ]]; then
                    contents_read=yes
                fi
            fi
        fi
    done < "$workflow"

    if [[ "$contents_read" != yes ]]; then
        report "$workflow" 1 "PR workflows must declare top-level permissions: contents: read"
    fi
}

check_run_input_interpolation() {
    local workflow="$1"
    local line_no
    local matches

    if ! matches=$(ruby -ryaml - "$workflow" <<'RUBY'
INPUT_CONTEXT = /(?:^|[^[:alnum:]_.'"])(?:inputs(?:\s*(?:\.|\[)|(?=\s*(?:[,)]|$)))|github\s*(?:\.\s*event|\[\s*['"]event['"]\s*\])\s*(?:\.\s*inputs|\[\s*['"]inputs['"]\s*\])(?=\s*(?:\.|\[|[,)]|$)))/

def interpolates_input?(value)
    value.scan(/\$\{\{(.*?)\}\}/m) do
        return true if Regexp.last_match(1).match?(INPUT_CONTEXT)
    end
    false
end

def visit(node, path = [], &block)
    if node.is_a?(Psych::Nodes::Alias) && path.first == "jobs" && path.include?("steps")
        yield node.start_line + 1
        return
    end

    case node
    when Psych::Nodes::Mapping
        node.children.each_slice(2) do |key, value|
            key_name = key.is_a?(Psych::Nodes::Scalar) ? key.value : nil
            value_path = path + [key_name]
            if value_path.length == 4 && value_path[0] == "jobs" &&
                    value_path[2] == "steps" && key_name == "run" &&
                    value.is_a?(Psych::Nodes::Scalar) && interpolates_input?(value.value)
                yield value.start_line + 1
            end
            visit(key, path, &block)
            visit(value, value_path, &block)
        end
    when Psych::Nodes::Sequence, Psych::Nodes::Document, Psych::Nodes::Stream
        node.children.each { |child| visit(child, path, &block) }
    end
end

Psych.parse_stream(File.read(ARGV.fetch(0))).children.each { |document| visit(document) { |line| puts line } }
RUBY
    ); then
        report "$workflow" 0 "unable to parse workflow YAML"
        return 1
    fi

    while IFS= read -r line_no; do
        [[ -z "$line_no" ]] && continue
        report "$workflow" "$line_no" "workflow-dispatch inputs must be passed through env, not interpolated in run blocks"
    done <<< "$matches"
}

for pr_workflow in .github/workflows/pr.yml .github/workflows/pr-risk.yml; do
    if [[ -f "$pr_workflow" ]]; then
        check_pr_permissions "$pr_workflow"
    fi
done

if (( $# > 0 )); then
    workflows=("$@")
else
    workflows=(.github/workflows/*.yml .github/workflows/*.yaml)
fi

for workflow in "${workflows[@]}"; do
    [[ -f "$workflow" ]] || continue
    check_run_input_interpolation "$workflow"
done


flake_workflow=.github/workflows/update-flake-lock.yml
canonical_guard=no
if [[ -f "$flake_workflow" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        if [[ "$line" == "    if:"* ]] && \
           { [[ "$line" == *"github.repository == 'gastownhall/beads'"* ]] || \
             [[ "$line" == *'github.repository == "gastownhall/beads"'* ]]; }; then
            canonical_guard=yes
            break
        fi
    done < "$flake_workflow"
fi
if [[ "$canonical_guard" != yes ]]; then
    report "$flake_workflow" 12 "privileged update-flake-lock job needs a canonical-repository guard"
fi

installer=scripts/install-dolt-ci.sh
if [[ ! -f "$installer" ]]; then
    report "$installer" 1 "missing pinned Dolt CI installer"
else
    if ! grep -Ev '^[[:space:]]*#' "$installer" | \
         grep -Eq "DOLT_VERSION[[:space:]]*=[[:space:]]*['\"]?v?[0-9]+\.[0-9]+\.[0-9]+['\"]?([[:space:]]|$)"; then
        report "$installer" 1 "DOLT_VERSION must be a literal semantic version"
    fi

    has_checksum() {
        local os_name="$1"
        local arch_pattern="$2"
        local line
        local in_platform=no
        shopt -s nocasematch
        while IFS= read -r line || [[ -n "$line" ]]; do
            [[ "$line" =~ ^[[:space:]]*# ]] && continue
            if [[ "$line" =~ $os_name ]] && [[ "$line" =~ $arch_pattern ]]; then
                in_platform=yes
            fi
            if [[ "$in_platform" == yes && "$line" =~ [0-9a-f]{64} ]]; then
                shopt -u nocasematch
                return 0
            fi
            if [[ "$in_platform" == yes && "$line" == *";;"* ]]; then
                break
            fi
        done < "$installer"
        shopt -u nocasematch
        return 1
    }

    if ! has_checksum linux 'amd64|x86_64'; then
        report "$installer" 1 "missing Linux amd64 SHA256 pin"
    fi
    if ! has_checksum linux 'arm64|aarch64'; then
        report "$installer" 1 "missing Linux arm64 SHA256 pin"
    fi
    if ! has_checksum darwin 'amd64|x86_64'; then
        report "$installer" 1 "missing Darwin amd64 SHA256 pin"
    fi
    if ! has_checksum darwin 'arm64|aarch64'; then
        report "$installer" 1 "missing Darwin arm64 SHA256 pin"
    fi

    line_no=0
    while IFS= read -r line || [[ -n "$line" ]]; do
        line_no=$((line_no + 1))
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        if [[ "$line" =~ (curl|wget) ]] && [[ "$line" == *"|"* ]] && \
           [[ "$line" =~ (^|[[:space:]])(ba)?sh([[:space:]]|$) ]]; then
            report "$installer" "$line_no" "network responses must never be piped to a shell"
        fi
    done < "$installer"
fi

# First-party install guidance must never execute unreviewed network content and
# must not follow a mutable branch. Release-tagged staged downloads remain valid.
raw_installer_re='(raw\.githubusercontent\.com/(gastownhall|steveyegge)/beads/(main|master)/(scripts/[^[:space:])]+\.sh|install\.ps1)|github\.com/(gastownhall|steveyegge)/beads/raw/(main|master)/(scripts/[^[:space:])]+\.sh|install\.ps1))'
shopt -s nocasematch
while IFS= read -r -d '' file; do
    case "$file" in
        website/versioned_docs/version-1.0.*|docs/staged-for-removal/*|.beads/*|*/.beads/*|scripts/check-workflow-supply-chain.sh)
            continue
            ;;
    esac
    [[ -f "$file" ]] || continue
    grep -Iq . "$file" || continue

    line_no=0
    community_tools=no
    while IFS= read -r line || [[ -n "$line" ]]; do
        line_no=$((line_no + 1))
        if [[ "$line" =~ ^#[[:space:]]+(Beads[[:space:]]+)?Community[[:space:]]+Tools ]]; then
            community_tools=yes
            continue
        fi
        if [[ "$community_tools" == yes && "$line" =~ ^#[[:space:]]+ ]]; then
            community_tools=no
        fi
        [[ "$line" =~ ^[[:space:]]*# ]] && continue

        if [[ "$community_tools" == yes && ! "$line" =~ (gastownhall|steveyegge)/beads ]]; then
            continue
        fi
        case "$file" in
            */COMMUNITY_TOOLS.md|*/community-tools.md)
                if [[ ! "$line" =~ (gastownhall|steveyegge)/beads ]]; then
                    continue
                fi
                ;;
        esac

        if [[ "$line" =~ (^|[[:space:]-])uses:[[:space:]]*([^[:space:]#]+) ]]; then
            action_ref="${BASH_REMATCH[2]}"
            if [[ "$action_ref" != ./* && ! "$action_ref" =~ @[0-9a-f]{40}$ ]]; then
                report "$file" "$line_no" "documented third-party actions must be pinned to a full commit SHA"
            fi
        fi
        if [[ "$line" =~ ^[[:space:]]*version: ]]; then
            version_value="${line#*:}"
            version_value="${version_value// /}"
            version_value="${version_value//$'\t'/}"
            version_value="${version_value//\"/}"
            version_value="${version_value//\'/}"
            case "$version_value" in
                latest|*'~'*|*'^'*|\**|*'<'*|*'>'*)
                    report "$file" "$line_no" "documented downloaded tools must use an exact version"
                    ;;
            esac
        fi
        if [[ "$file" == RELEASING.md || "$file" == */PYPI.md ]] &&
           [[ "$line" =~ (uv[[:space:]]+)?pip([0-9.]*)?[[:space:]]+install([[:space:]]+[^#]*)?[[:space:]]+(build|twine)([[:space:]]|$) ]]; then
            report "$file" "$line_no" "release tooling must use the locked uv environment, not unpinned pip installs"
        fi
        if [[ "$line" =~ (curl|wget)[^|]*\|[[:space:]]*(sudo[[:space:]]+)?(sh|bash) ]]; then
            report "$file" "$line_no" "first-party guidance must not pipe network content to sh/bash"
        fi
        if [[ "$line" =~ (irm|iwr|Invoke-WebRequest)[^|]*\|[[:space:]]*(iex|Invoke-Expression) ]]; then
            report "$file" "$line_no" "first-party guidance must not pipe network content to Invoke-Expression"
        fi
        if [[ "$line" =~ $raw_installer_re ]]; then
            report "$file" "$line_no" "first-party installer guidance must use an immutable release, not main/master"
        fi
    done < "$file"
done < <(
    git ls-files -z -- \
        ':(top,glob)*.md' \
        ':(top,glob)docs/**' \
        ':(top,glob)*.yml' \
        ':(top,glob)*.yaml' \
        ':(top,glob)cmd/**' \
        ':(top,glob)internal/**' \
        ':(top,glob)integrations/**' \
        ':(top,glob)scripts/**' \
        ':(top,glob)examples/**' \
        ':(top,glob)npm-package/**' \
        ':(top,glob)plugins/**' \
        ':(top,glob)tests/**' \
        ':(top,glob)website/docs/**' \
        ':(top,glob)website/versioned_docs/version-1.1.0/**' \
        ':(top,glob)website/static/**'
)
shopt -u nocasematch

policy_wrapper=scripts/ci/pr-policy.sh
policy_wired=no
if [[ -f "$policy_wrapper" ]]; then
    while IFS= read -r line || [[ -n "$line" ]]; do
        [[ "$line" =~ ^[[:space:]]*# ]] && continue
        if [[ "$line" == *"./scripts/check-workflow-supply-chain.sh"* ]]; then
            policy_wired=yes
            break
        fi
    done < "$policy_wrapper"
fi
if [[ "$policy_wired" != yes ]]; then
    report "$policy_wrapper" 1 "PR policy wrapper must invoke ./scripts/check-workflow-supply-chain.sh"
fi

if (( fail != 0 )); then
    exit "$fail"
fi

printf 'Workflow supply-chain policy passed.\n'
