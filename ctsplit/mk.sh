#!/usr/bin/env bash
# mk.sh K: build branch f1-ct-K on top of the current HEAD (must be f1-ct-(K-1) or base).
set -euo pipefail
K=$1
NN=$(printf %02d "$K")
W=/tmp/ct-split2
S=/tmp/ctsplit
cd "$W"
git checkout -q -B "f1-ct-$NN"
# codetools Go files from the projection
mkdir -p internal/codetools
find internal/codetools -maxdepth 1 -name '*.go' -delete
"$S/ctsplit" emit /tmp/damnbot-final/internal/codetools "$S/stages.txt" "$K" internal/codetools 2> >(grep -v '^$' >&2 || true)
# gofumpt/goimports only the partial files (final-identical files stay byte-identical)
part=()
for f in internal/codetools/*.go; do
	[[ -e $f ]] || continue
	cmp -s "$f" "/tmp/damnbot-final/$f" || part+=("$f")
done
if ((${#part[@]})); then go tool -modfile=tools/go.mod golangci-lint fmt "${part[@]}"; fi
rmdir internal/codetools 2>/dev/null || true
# stage assets
if [[ -f $S/st/$K.sh ]]; then
	S=$S K=$K bash -e "$S/st/$K.sh"
fi
git add -A
git -c user.email=jelangkung@users.noreply.github.com -c user.name="$(git config user.name || echo jelangkung)" \
	commit -q -m "$(cat "$S/st/$K.title")" -m "$(cat "$S/st/$K.body")"
git log --oneline -1
