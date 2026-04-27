#!/bin/bash
set -euo pipefail

# install-workflow.sh
# Installs the daily worklog GitHub Actions workflow into a repository.
#
# Usage:
#   ./scripts/install-workflow.sh
#
# This script copies the canonical workflow file at
#   cmd/wrklogr/wrklogr-daily.yaml
# into the target repository's .github/workflows/ directory, then prints
# next-step instructions.
#
# Prerequisites:
#   - gh CLI installed and authenticated (https://cli.github.com/)
#   - Working directory must be inside a git repo tracked on GitHub

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

CANONICAL="$REPO_ROOT/cmd/wrklogr/wrklogr-daily.yaml"
WORKFLOW_DIR="$REPO_ROOT/.github/workflows"
WORKFLOW_FILE="$WORKFLOW_DIR/wrklogr-daily.yaml"

echo "🔧 Installing wrklogr daily workflow..."

# ---- checks ----

if [ ! -f "$CANONICAL" ]; then
  echo "❌ Canonical workflow not found at $CANONICAL"
  exit 1
fi

if ! command -v gh &>/dev/null; then
  echo "❌ gh CLI is required but not found."
  echo "   Install it from https://cli.github.com/"
  exit 1
fi

if ! gh auth status &>/dev/null; then
  echo "❌ gh CLI is not authenticated."
  echo "   Run 'gh auth login' first."
  exit 1
fi

if ! git rev-parse --is-inside-work-tree &>/dev/null; then
  echo "❌ Not inside a git repository."
  exit 1
fi

# ---- derive repo info ----
REMOTE=$(git config --get remote.origin.url || true)
if [[ -z "$REMOTE" ]]; then
  echo "❌ No remote 'origin' found."
  exit 1
fi

# Normalise SSH and HTTPS remote URLs to owner/repo
OWNER_REPO=$(echo "$REMOTE" \
  | sed -n 's|.*github.com[:/]\(.*\)\.git|\1|p' \
  | sed -n 's|.*github.com[:/]\(.*\)|\1|p')
if [[ -z "$OWNER_REPO" ]]; then
  echo "❌ Could not parse owner/repo from remote '$REMOTE'."
  exit 1
fi

OWNER="${OWNER_REPO%/*}"
REPO="${OWNER_REPO#*/}"

# ---- copy workflow file ----
mkdir -p "$WORKFLOW_DIR"

if [ -f "$WORKFLOW_FILE" ]; then
  cp "$CANONICAL" "$WORKFLOW_FILE"
  echo "✅ Updated existing workflow at $WORKFLOW_FILE"
else
  cp "$CANONICAL" "$WORKFLOW_FILE"
  echo "✅ Created workflow at $WORKFLOW_FILE"
fi

echo ""
echo "─── Next steps ──────────────────────────────────────────────"
echo ""
echo "  1. Make sure wrklogr.toml lists all the repos you want to"
echo "     scan (uses GitHub API mode)."
echo ""
echo "  2. If you need to scan repos outside this one, create a"
echo "     fine-grained Personal Access Token (PAT) with"
echo "     'Contents: Read' on each target repo and save it as:"
echo ""
echo "       https://github.com/${OWNER}/${REPO}/settings/secrets/actions"
echo ""
echo "     Secret name: WORKLOG_TOKEN"
echo ""
echo "     Otherwise the built-in GITHUB_TOKEN only grants access to"
echo "     ${OWNER}/${REPO}."
echo ""
echo "  3. Enable the repo wiki if it's off:"
echo ""
echo "       https://github.com/${OWNER}/${REPO}/settings"
echo ""
echo "  4. Commit and push the workflow:"
echo ""
echo "       git add .github/workflows/wrklogr-daily.yaml"
echo "       git commit -m \"ci: add daily worklog workflow\""
echo "       git push"
echo ""
echo "  5. Test it manually:"
echo ""
echo "       gh workflow run wrklogr-daily.yaml"
