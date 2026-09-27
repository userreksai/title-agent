#!/bin/sh
# Download the remote default branch and build into the caller's directory.
set -eu
umask 077

fail() { printf 'ERROR: %s\n' "$*" >&2; exit 1; }
require() { command -v "$1" >/dev/null 2>&1 || fail "Required command not found: $1"; }
for command in git go mktemp cp mv chmod; do require "$command"; done

REPO_URL=${TITLE_AGENT_REPO_URL:-https://github.com/userreksai/title-agent.git}
REF=${TITLE_AGENT_REF:-}
OUTPUT_DIR=$(pwd -P)
work_dir=$(mktemp -d "$OUTPUT_DIR/.title-agent-build.XXXXXX")
cleanup() { [ ! -d "$work_dir" ] || rm -rf -- "$work_dir"; }
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

printf 'Downloading latest title-agent source...\n'
if [ -n "$REF" ]; then
    git clone --quiet --depth 1 --no-tags --branch "$REF" -- "$REPO_URL" "$work_dir/source"
else
    git clone --quiet --depth 1 --no-tags -- "$REPO_URL" "$work_dir/source"
fi
revision=$(git -C "$work_dir/source" rev-parse HEAD)
(
    cd "$work_dir/source"
    printf 'Building commit %s with %s\n' "$revision" "$(go version)"
    CGO_ENABLED=0 go build -trimpath -o "$work_dir/title-agent" ./cmd/title-agent
)
chmod 755 "$work_dir/title-agent"

# A failed clone/build never replaces the currently installed program.
if [ -e "$OUTPUT_DIR/title-agent" ]; then
    cp -p -- "$OUTPUT_DIR/title-agent" "$OUTPUT_DIR/title-agent.previous"
fi
if [ ! -e "$OUTPUT_DIR/agent.json" ]; then
    cp -- "$work_dir/source/configs/agent.example.json" "$OUTPUT_DIR/agent.json"
    chmod 600 "$OUTPUT_DIR/agent.json"
fi
mv -f -- "$work_dir/title-agent" "$OUTPUT_DIR/title-agent"
printf '%s\n' "$revision" > "$OUTPUT_DIR/title-agent.version"
printf '\nBuilt: %s/title-agent\nCommit: %s\n' "$OUTPUT_DIR" "$revision"
printf 'Existing agent.json was preserved. Set name/token on first install.\n'
printf 'Start manually: ./title-agent -config ./agent.json\n'
printf 'An already-running agent must be restarted manually to use this build.\n'
