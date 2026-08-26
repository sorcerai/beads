#!/usr/bin/env bash
set -euo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
cd "$script_dir/.."

if ./scripts/check-workflow-supply-chain.sh scripts/testdata/workflow-input-unsafe.yml; then
    echo "unsafe workflow input interpolation was accepted" >&2
    exit 1
fi

./scripts/check-workflow-supply-chain.sh scripts/testdata/workflow-input-safe.yml
