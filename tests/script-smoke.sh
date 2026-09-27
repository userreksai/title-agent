#!/bin/sh
# Isolated deployment logic checks using fake git/go/systemctl; no real service
# is installed and no repository is fetched. Pass: master|agent script-path.
set -eu
role=${1:?usage: test-shell.sh master|agent script-path}
script=$(cd "$(dirname "$2")" && pwd -P)/$(basename "$2")
root=$(mktemp -d)
trap 'rm -rf -- "$root"' EXIT
export TEST_ROOT=$root
export REAL_CP=$(command -v cp)
export REAL_MKDIR=$(command -v mkdir)
export REAL_CHMOD=$(command -v chmod || command -v true)
mkdir -p "$root/fakebin" "$root/source/configs" "$root/units" "$root/run"
export PATH=$root/fakebin:$PATH
export INSTALL_DIR=$root/run
export SYSTEMD_UNIT_DIR=$root/units
export TITLE_MASTER_REPO_URL=fixture
export TITLE_AGENT_REPO_URL=fixture
export MOCK_VERSION=version-one

cat > "$root/fakebin/git" <<'EOF'
#!/bin/sh
if [ "$1" = clone ]; then
    for arg do destination=$arg; done
    "$REAL_CP" -R "$TEST_ROOT/source" "$destination"
else
    printf 'test-commit\n'
fi
EOF
cat > "$root/fakebin/go" <<'EOF'
#!/bin/sh
if [ "$1" = version ]; then printf 'go version go1.23.0 linux/amd64\n'; exit 0; fi
[ "${FAIL_BUILD:-0}" -eq 0 ] || exit 19
while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then shift; output=$1; fi
    shift
done
printf '#!/bin/sh\n# %s\n' "$MOCK_VERSION" > "$output"
cat >> "$output" <<'PROGRAM'
config=
while [ "$#" -gt 0 ]; do
    case "$1" in
        -config) shift; config=$1 ;;
        -check-config) grep -q CHANGE_ME "$config" && exit 2; exit 0 ;;
        -print-state-path) printf '%s/data/state.json\n' "$(dirname "$config")"; exit 0 ;;
    esac
    shift
done
PROGRAM
"$REAL_CHMOD" 755 "$output"
EOF
cat > "$root/fakebin/id" <<'EOF'
#!/bin/sh
case "$1" in -gn) printf 'testgroup\n';; *) printf '0\n';; esac
EOF
cat > "$root/fakebin/uname" <<'EOF'
#!/bin/sh
printf 'Linux\n'
EOF
cat > "$root/fakebin/install" <<'EOF'
#!/bin/sh
directory=0
while [ "$#" -gt 0 ]; do
    case "$1" in
        -d) directory=1; shift ;;
        -o|-g|-m) shift 2 ;;
        *) break ;;
    esac
done
if [ "$directory" -eq 1 ]; then "$REAL_MKDIR" -p "$@"; else "$REAL_CP" "$@"; fi
EOF
cat > "$root/fakebin/systemctl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$TEST_ROOT/systemctl.log"
case "$1" in
    is-active) [ -e "$TEST_ROOT/active" ] ;;
    is-enabled) [ -e "$TEST_ROOT/enabled" ] ;;
    enable) touch "$TEST_ROOT/enabled" ;;
    disable) rm -f "$TEST_ROOT/enabled" ;;
    stop) rm -f "$TEST_ROOT/active" ;;
    restart)
        if [ -e "$TEST_ROOT/fail-restart" ]; then rm "$TEST_ROOT/fail-restart"; exit 9; fi
        touch "$TEST_ROOT/active" ;;
    *) exit 0 ;;
esac
EOF
for command in chown useradd sleep getent; do printf '#!/bin/sh\nexit 0\n' > "$root/fakebin/$command"; done
if ! command -v chmod >/dev/null 2>&1; then printf '#!/bin/sh\nexit 0\n' > "$root/fakebin/chmod"; fi
"$REAL_CHMOD" +x "$root/fakebin/"*
printf '{"token":"CHANGE_ME"}\n' > "$root/source/configs/master.example.json"
printf '{"token":"CHANGE_ME"}\n' > "$root/source/configs/agent.example.json"

cd "$root/run"
if [ "$role" = master ]; then
    sh "$script"
    test -f title-master
    test -f "$root/units/title-master.service"
    test ! -e "$root/active"
    printf '{"token":"private-token","custom":"keep-me"}\n' > master.json
    printf 'baseline-and-outbox\n' > data/state.json
    sh "$script"
    test -e "$root/active"
    test -e "$root/enabled"
    grep -q keep-me master.json
    grep -q baseline-and-outbox data/state.json
    cp title-master "$root/expected-binary"
    if FAIL_BUILD=1 sh "$script"; then echo 'Build failure was ignored' >&2; exit 1; fi
    cmp title-master "$root/expected-binary"
    touch "$root/fail-restart"
    if MOCK_VERSION=version-two sh "$script"; then echo 'Restart failure was ignored' >&2; exit 1; fi
    cmp title-master "$root/expected-binary"
    test -e "$root/active"
    grep -q keep-me master.json
    grep -q baseline-and-outbox data/state.json
    printf '{"token":"CHANGE_ME"}\n' > master.json
    if sh "$script"; then echo 'Invalid update config was accepted' >&2; exit 1; fi
    cmp title-master "$root/expected-binary"
    test -e "$root/active"
else
    sh "$script"
    test -f title-agent
    test -f agent.json
    printf 'private-config\n' > agent.json
    cp title-agent "$root/expected-binary"
    if FAIL_BUILD=1 sh "$script"; then echo 'Build failure was ignored' >&2; exit 1; fi
    cmp title-agent "$root/expected-binary"
    MOCK_VERSION=version-two sh "$script"
    grep -q version-two title-agent
    cmp title-agent.previous "$root/expected-binary"
    grep -q private-config agent.json
fi
printf 'PASS: %s deployment script scenarios\n' "$role"
