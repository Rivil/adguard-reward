#!/usr/bin/env bash
# Install adguard-reward on nora. Run as root, from the repo, via:
#
#   ssh nora sudo -n env VERSION=$(git describe --tags) bash -s < deploy/install-nora.sh
#
# It expects `make deploy-nora` to have already copied three files into $STAGE
# (/tmp by default): adguard-reward.new, adguard-reward.service and
# config.nora.yaml.
#
# On the first run there is no config and no credential yet, so it writes
# both, tells you what to fill in, and exits 3 *without* starting the service —
# a service started with a placeholder password would hammer AdGuard with bad
# logins. Fill them in on nora and run `make deploy-nora` again.
#
# Exit codes:
#   0  installed, service healthy and reporting $VERSION
#   3  config or credential still needs editing (nothing was started)
#   4  the service came up reporting a different version than was deployed
#   1  anything else, including a service that never became healthy
#
# It never reads the credential's value or echoes it anywhere: the only
# question it asks of that file is whether it is still the placeholder.
set -euo pipefail

: "${VERSION:?VERSION must be set (the version the deployed binary was stamped with)}"

# Staging area `make deploy-nora` scp'd into, and an install prefix. Both exist
# so the test suite can run this script against a temp tree.
STAGE="${STAGE:-/tmp}"
DESTDIR="${DESTDIR:-}"

# How long to wait for /healthz after the restart, and how often to ask.
HEALTH_TIMEOUT="${HEALTH_TIMEOUT:-45}"
HEALTH_INTERVAL="${HEALTH_INTERVAL:-1}"

# Resolved from PATH rather than hard-coded so the tests can stub them.
SYSTEMCTL="$(command -v systemctl)"
CURL="$(command -v curl)"
TAILSCALE="$(command -v tailscale)"
JOURNALCTL="$(command -v journalctl)"

UNIT_NAME="adguard-reward.service"
SERVICE="adguard-reward"
BIN="$DESTDIR/usr/local/bin/adguard-reward"
UNIT="$DESTDIR/etc/systemd/system/$UNIT_NAME"
CONF_DIR="$DESTDIR/etc/adguard-reward"
CONF="$CONF_DIR/config.yaml"
CRED="$CONF_DIR/adguard_password"

# The app listens on the loopback; tailscale serve owns 443 and proxies here.
LISTEN_URL="http://127.0.0.1:8080"
PLACEHOLDER="CHANGE-ME"

say() { printf '%s\n' "$*"; }
die() { printf '%s\n' "$*" >&2; exit 1; }

install -d -m 0755 "$DESTDIR/usr/local/bin" "$DESTDIR/etc/systemd/system" "$CONF_DIR"

# Unlink before installing: overwriting a running executable in place fails
# with ETXTBSY, and `install` truncates rather than replacing.
rm -f "$BIN"
install -m 0755 "$STAGE/adguard-reward.new" "$BIN"
install -m 0644 "$STAGE/$UNIT_NAME" "$UNIT"
say "installed $BIN and $UNIT"

# magicdns_suffix prints the tailnet's MagicDNS suffix (e.g. tail1234.ts.net),
# or nothing if tailscale cannot say. No jq on a fresh host, so this reads the
# one field it needs out of the JSON.
magicdns_suffix() {
	"$TAILSCALE" status --json 2>/dev/null |
		tr -d ' \t' |
		sed -n 's/.*"MagicDNSSuffix":"\([^"]*\)".*/\1/p' |
		head -n 1
}

# First run only. An existing config is never overwritten — it holds the
# operator's edits, and a deploy that silently reverted them would be worse
# than one that failed.
if [ ! -e "$CONF" ]; then
	tailnet="$(magicdns_suffix)"
	tailnet="${tailnet%.ts.net}"
	staged="$STAGE/config.nora.yaml"
	# BSD mktemp needs an explicit template, so spell one out rather than
	# relying on the GNU no-argument form.
	tmp="$(mktemp "${TMPDIR:-/tmp}/adguard-reward.XXXXXX")"
	if [ -n "$tailnet" ]; then
		sed "s/TAILNET/$tailnet/g" "$staged" >"$tmp"
	else
		# Leave the placeholder in: the guard below turns it into a clear
		# "edit this" rather than a config pointing at nora..ts.net.
		cat "$staged" >"$tmp"
	fi
	install -m 0644 "$tmp" "$CONF"
	rm -f "$tmp"
	say "wrote $CONF"
fi

if [ ! -e "$CRED" ]; then
	(
		umask 077
		printf '%s\n' "$PLACEHOLDER" >"$CRED"
	)
	chmod 0600 "$CRED"
	say "wrote $CRED (placeholder)"
fi

# Nothing below this point runs until the config is real. Checked with grep -q
# so the credential's value is never rendered, here or anywhere else.
needs_edit=0
if grep -q "$PLACEHOLDER" "$CONF"; then
	printf '%s\n' "$CONF: adguard.username is still $PLACEHOLDER" >&2
	needs_edit=1
fi
if grep -q 'TAILNET' "$CONF"; then
	printf '%s\n' "$CONF: base_url still says TAILNET — tailscale reported no MagicDNS suffix; put the tailnet name in by hand" >&2
	needs_edit=1
fi
if grep -qx "$PLACEHOLDER" "$CRED"; then
	printf '%s\n' "$CRED: still the placeholder. On nora, write the AdGuard password into it:" >&2
	printf '%s\n' "    sudo sh -c 'umask 077; cat > $CRED'" >&2
	needs_edit=1
fi
if [ "$needs_edit" -ne 0 ]; then
	printf '%s\n' "not starting $SERVICE with placeholder credentials; edit the above and re-run \`make deploy-nora\`" >&2
	exit 3
fi

"$SYSTEMCTL" daemon-reload
"$SYSTEMCTL" enable "$SERVICE"
"$SYSTEMCTL" restart "$SERVICE"
say "restarted $SERVICE"

# tailscale serve terminates HTTPS on 443 with the tailnet cert and proxies to
# the loopback listener. The literal 127.0.0.1 matters: the config trusts that
# address for X-Forwarded-For, and "localhost" can resolve to ::1.
"$TAILSCALE" serve --bg --https=443 "$LISTEN_URL"
say "tailscale serve --bg --https=443 $LISTEN_URL"

# Health gate. The deploy has not succeeded until the service answers with the
# version that was just built — a restart that silently kept the old binary
# looks identical otherwise.
health_dump() {
	"$JOURNALCTL" -u "$SERVICE" -n 20 >&2 || true
}

deadline=$(($(date +%s) + HEALTH_TIMEOUT))
body=""
while :; do
	if body="$("$CURL" -fsS "$LISTEN_URL/healthz" 2>/dev/null)"; then
		break
	fi
	body=""
	if [ "$(date +%s)" -ge "$deadline" ]; then
		printf '%s\n' "$SERVICE did not answer $LISTEN_URL/healthz within ${HEALTH_TIMEOUT}s" >&2
		health_dump
		die "deploy failed"
	fi
	sleep "$HEALTH_INTERVAL"
done

if ! printf '%s' "$body" | grep -qF "$VERSION"; then
	got="$(printf '%s' "$body" | tr -d ' \t' | sed -n 's/.*"version":"\([^"]*\)".*/\1/p' | head -n 1)"
	printf '%s\n' "$SERVICE is healthy but reports version ${got:-<none>}, expected $VERSION" >&2
	health_dump
	exit 4
fi

say "$SERVICE healthy, reporting $VERSION"
