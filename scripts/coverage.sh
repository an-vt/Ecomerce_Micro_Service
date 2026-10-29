#!/usr/bin/env bash
# Unit-test coverage per module; fails when a module is below 80% (docs/10-testing.md §1.1).
#   COVER_INTEGRATION=1  also runs *_integration_test.go (needs Docker)
# Excluded from the profile: cmd/, internal/app/, migrations, seeders, testenv, fakes, generated code.
set -uo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

THRESHOLD="${COVER_THRESHOLD:-80}"
TAGS=""
[ "${COVER_INTEGRATION:-0}" = "1" ] && TAGS="-tags=integration"
EXCLUDE='/cmd/|/internal/app/|/internal/database/(migrations|seeders)/|/internal/testenv/|/fake/|/repotest/|/gen/|\.pb\.go|/doc\.go'

# percent of statements for the packages whose path matches $1 (extended regex), from profile $2
pct_of() {
  awk -v re="$1" 'NR>1 { split($1,a,":"); if (a[1] ~ re) { t+=$2; if ($3>0) c+=$2 } } END { if (t==0) print "-"; else printf "%.1f", 100*c/t }' "$2"
}

fail=0
printf '\n%-24s %8s %8s %8s   %s\n' MODULE DOMAIN USECASE TOTAL RESULT
for m in platform-go "${SERVICES[@]}"; do
  [ -d "$ROOT/$m" ] || continue
  if ! find "$ROOT/$m" -name '*_test.go' -print -quit | grep -q .; then continue; fi
  prof="$(mktemp)"
  (cd "$ROOT/$m" && go test -race -count=1 $TAGS -coverprofile="$prof.raw" ./... >/tmp/cover-"$m".log 2>&1)
  rc=$?
  grep -Ev "$EXCLUDE" "$prof.raw" > "$prof" 2>/dev/null || true
  total="$(pct_of '.' "$prof")"; dom="$(pct_of '/internal/domain/' "$prof")"; uc="$(pct_of '/internal/usecase/' "$prof")"
  res=ok
  [ $rc -ne 0 ] && res="TESTS FAILED (see /tmp/cover-$m.log)"
  for v in "$total" "$dom" "$uc"; do
    [ "$v" = "-" ] && continue
    awk -v v="$v" -v t="$THRESHOLD" 'BEGIN{exit !(v+0 < t+0)}' && res="BELOW ${THRESHOLD}%"
  done
  [ "$res" != ok ] && fail=1
  printf '%-24s %8s %8s %8s   %s\n' "$m" "$dom" "$uc" "$total" "$res"
  rm -f "$prof" "$prof.raw"
done
echo
exit $fail
