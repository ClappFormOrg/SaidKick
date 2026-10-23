#!/usr/bin/env bash
# Deletes the kind clusters created by run.sh and the run state in .work/.
# Locally dropped-in tools under .work/bin are kept unless --purge is given.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
WORK="$HERE/.work"
PATH="$WORK/bin:$PATH"

for name in saidkick-hub saidkick-spoke; do
  if kind get clusters 2>/dev/null | tr -d '\r' | grep -qx "$name"; then
    echo "deleting kind cluster $name"
    kind delete cluster --name "$name"
  else
    echo "kind cluster $name not found"
  fi
done

if [ -d "$WORK" ]; then
  if [ "${1:-}" = "--purge" ]; then
    rm -rf "$WORK"
    echo "removed $WORK"
  else
    find "$WORK" -mindepth 1 -maxdepth 1 ! -name bin -exec rm -rf {} +
    echo "removed run state in $WORK (kept $WORK/bin)"
  fi
fi
