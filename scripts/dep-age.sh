#!/bin/sh
# GAP-INF-07: dependency quarantine. A new/bumped module in go.mod or
# tools/go.mod must be at least 3 days old at proxy.golang.org; the only
# exception is a govulncheck fix carrying the `security-hotfix` label and two
# approvers. The repository does not vendor dependencies, so the go.sum hash
# is the integrity record instead of a vendor diff.
#
# Usage: scripts/dep-age.sh [diff-file]
# Env:   DAMNBOT_DEP_BASE (default origin/main), DAMNBOT_DEP_PROXY,
#        DAMNBOT_DEP_NOW, DAMNBOT_PR_LABELS, DAMNBOT_DEP_APPROVERS.
set -eu

# epoch converts an RFC3339 UTC timestamp to seconds (GNU date first, BSD date second).
epoch() {
	if date -u -d "$1" +%s >/dev/null 2>&1; then
		date -u -d "$1" +%s
	else
		date -u -j -f "%Y-%m-%dT%H:%M:%SZ" "$1" +%s
	fi
}

cd "$(dirname "$0")/.."
proxy=${DAMNBOT_DEP_PROXY:-https://proxy.golang.org}
now=${DAMNBOT_DEP_NOW:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
min_age_days=3

if [ "${1:-}" != "" ]; then
	diff=$(cat "$1")
else
	base=${DAMNBOT_DEP_BASE:-origin/main}
	diff=$(git diff --unified=0 "$base"...HEAD -- go.mod tools/go.mod 2>/dev/null || true)
fi
if [ -z "$diff" ]; then
	echo "dep-age: no dependency changes"
	exit 0
fi

# added module lines: "+ require m vX" or "+ m vX" inside a require block.
added=$(printf '%s\n' "$diff" | awk '
	/^\+\+\+/ { next }
	/^\+/ {
		line = substr($0, 2)
		sub(/^[ \t]+/, "", line)
		sub(/^require[ \t]+/, "", line)
		n = split(line, a, /[ \t]+/)
		if (n >= 2 && a[1] ~ /\./ && a[2] ~ /^v[0-9]/) print a[1], a[2]
	}' | sort -u)
if [ -z "$added" ]; then
	echo "dep-age: no module version changes"
	exit 0
fi

hotfix=0
case ",${DAMNBOT_PR_LABELS:-}," in
*,security-hotfix,*) [ "${DAMNBOT_DEP_APPROVERS:-0}" -ge 2 ] && hotfix=1 ;;
esac

now_epoch=$(epoch "$now")
status=0
while read -r mod ver; do
	[ -z "$mod" ] && continue
	esc=$(printf '%s' "$mod" | awk '{o="";for(i=1;i<=length($0);i++){c=substr($0,i,1);if(c ~ /[A-Z]/)o=o "!" tolower(c);else o=o c}print o}')
	info=$(curl -fsS --max-time 20 "$proxy/$esc/@v/$ver.info") || {
		echo "DEP-AGE GAGAL: $mod $ver: cannot read $proxy/$esc/@v/$ver.info"
		status=1
		continue
	}
	ts=$(printf '%s' "$info" | sed -n 's/.*"Time"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' | sed 's/\.[0-9]*Z$/Z/')
	if [ -z "$ts" ]; then
		echo "DEP-AGE GAGAL: $mod $ver: no Time in proxy response"
		status=1
		continue
	fi
	t_epoch=$(epoch "$ts")
	age=$(( (now_epoch - t_epoch) / 86400 ))
	if [ "$age" -lt "$min_age_days" ] && [ "$hotfix" -ne 1 ]; then
		echo "DEP-AGE GAGAL: $mod $ver umur ${age} hari < ${min_age_days}"
		status=1
	else
		echo "dep-age: $mod $ver umur ${age} hari OK"
	fi
done <<EOF
$added
EOF

exit "$status"
