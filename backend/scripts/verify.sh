#!/usr/bin/env bash
#
# verify.sh starts its own sneakdrop server, fires 1000 Buy clicks at it at
# the same moment, then walks through every rule of the drop and checks that
# the server kept it. It exits non-zero if any check fails.
#
# Needs go, curl 7.68 or newer, jq and openssl. Run it from anywhere:
#
#   scripts/verify.sh
#   BUYERS=5000 PARALLEL=300 scripts/verify.sh
#
set -euo pipefail

BUYERS=${BUYERS:-1000}          # different users who each click Buy once
SPAM_CLICKS=${SPAM_CLICKS:-50}  # one more user, "spammer", clicks this many times
PARALLEL=${PARALLEL:-200}       # requests in flight at the same instant (curl allows up to 300)
PORT=${PORT:-18080}
STOCK=20
TTL=8          # seconds a hold lasts in this run, so expiry can be watched
PAID=15        # winners who pay; the other 5 let their hold run out
LINE=7         # people who join the waiting line
SECRET=verify-secret
BASE="http://127.0.0.1:$PORT"

for tool in go curl jq openssl perl; do
  command -v "$tool" >/dev/null || { echo "verify.sh needs $tool" >&2; exit 2; }
done

cd "$(dirname "$0")/.."
WORK=$(mktemp -d)
SERVER_PID=
FAILED=0

cleanup() {
  if [ -n "$SERVER_PID" ]; then kill "$SERVER_PID" 2>/dev/null || true; fi
  if [ "$FAILED" -gt 0 ] && [ -f "$WORK/server.log" ]; then
    echo
    echo "Last lines of the server log:"
    tail -20 "$WORK/server.log"
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

# ---------------------------------------------------------------- helpers

step() { printf '\n\033[1m%s\033[0m\n' "$*"; }
pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$*"; FAILED=$((FAILED + 1)); }
note() { printf '       %s\n' "$*"; }

# expect NAME GOT WANT
expect() {
  if [ "$2" = "$3" ]; then pass "$1"; else fail "$1 (got '$2', want '$3')"; fi
}

now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time * 1000'; }
post() { curl -s -H 'Content-Type: application/json' -d "$2" "$BASE$1"; }
get() { curl -s "$BASE$1"; }
stats_field() { get /api/stats | jq -r ".$1"; }

# seen USER JQ: one field of what USER sees on the page
seen() { get "/api/state?user=$1" | jq -r "$2"; }

# wait_until WHAT SECONDS COMMAND...: retry COMMAND every 0.2s until it succeeds
wait_until() {
  local what=$1 tries=$(($2 * 5))
  shift 2
  until "$@"; do
    tries=$((tries - 1))
    if [ "$tries" -le 0 ]; then
      fail "timed out waiting for $what"
      return 1
    fi
    sleep 0.2
  done
}

# webhook EVENT_ID HOLD_ID KIND [SECRET]: sends a payment message signed the
# way the payment company signs them. Prints "HTTP_STATUS OUTCOME".
webhook() {
  local body sig status
  body=$(jq -cn --arg id "$1" --argjson hold "$2" --arg kind "$3" '{event_id: $id, hold_id: $hold, kind: $kind}')
  sig=$(printf '%s' "$body" | openssl dgst -sha256 -hmac "${4:-$SECRET}" | awk '{print $NF}')
  status=$(curl -s -o "$WORK/webhook.json" -w '%{http_code}' -H 'Content-Type: application/json' \
    -H "X-Signature: $sig" -d "$body" "$BASE/webhooks/payment")
  echo "$status $(jq -r '.outcome // .error' "$WORK/webhook.json")"
}

# ---------------------------------------------------------------- server

step "Starting a fresh server on port $PORT: $STOCK pairs, holds last ${TTL}s"
if curl -s "$BASE/api/stats" >/dev/null 2>&1; then
  echo "Something is already listening on port $PORT. Set PORT to a free one." >&2
  exit 2
fi
go build -o "$WORK/sneakdrop" .
PORT=$PORT STOCK=$STOCK HOLD_TTL=${TTL}s WEBHOOK_SECRET=$SECRET \
  PAY_MAX_DELAY=500ms PAY_DUPLICATE_RATE=1 PAY_FAILURE_RATE=1 \
  "$WORK/sneakdrop" >"$WORK/server.log" 2>&1 &
SERVER_PID=$!
up() { curl -sf "$BASE/api/stats" >/dev/null; }
wait_until "the server to start" 10 up || exit 1
note "The fake payment company is set to send every success twice, plus a stray"
note "failure for every payment, each after a random delay of up to 500ms."

# ---------------------------------------------------------------- 1. rush

TOTAL=$((BUYERS + SPAM_CLICKS))
step "1. The rush: $TOTAL clicks on Buy, $PARALLEL in flight at the same instant"
note "$BUYERS different buyers click once. One more buyer, \"spammer\", clicks $SPAM_CLICKS times."

# One curl config with a block per click; curl runs them $PARALLEL at a time.
# Each answer lands in a file named <click number>_<user>.json.
mkdir "$WORK/clicks"
every=$((SPAM_CLICKS > 0 ? TOTAL / SPAM_CLICKS : TOTAL + 1))
spam=0
buyer=0
for ((click = 1; click <= TOTAL; click++)); do
  if [ $((click % every)) -eq 0 ] && [ "$spam" -lt "$SPAM_CLICKS" ]; then
    user=spammer
    spam=$((spam + 1))
  else
    buyer=$((buyer + 1))
    printf -v user 'buyer-%04d' "$buyer"
  fi
  if [ "$click" -gt 1 ]; then echo next; fi
  printf 'url = "%s/api/hold"\nheader = "Content-Type: application/json"\ndata = "{\\"user\\":\\"%s\\"}"\noutput = "%s/clicks/%05d_%s.json"\n' \
    "$BASE" "$user" "$WORK" "$click" "$user"
done >"$WORK/clicks.cfg"

started=$(now_ms)
curl -s --parallel --parallel-immediate --parallel-max "$PARALLEL" -K "$WORK/clicks.cfg" || true
took=$(($(now_ms) - started))

# results.tsv: click, user, result, hold id. Sorted by click number.
jq -r '(input_filename | split("/") | last | rtrimstr(".json") | split("_")) as [$click, $user]
  | [$click, $user, (if .hold then "held" else (.error // "unknown") end), (.hold.id // "-" | tostring)]
  | @tsv' "$WORK"/clicks/*.json | sort >"$WORK/results.tsv"

count() { awk -F'\t' "$1" "$WORK/results.tsv" | wc -l | tr -d ' '; }
answered=$(count 1)
held=$(count '$3=="held"')
sold_out=$(count '$3=="sold_out"')
already=$(count '$3=="already_holding"')
other=$((answered - held - sold_out - already))
distinct=$(awk -F'\t' '$3=="held" {print $2}' "$WORK/results.tsv" | sort -u | wc -l | tr -d ' ')
spam_held=$(count '$2=="spammer" && $3=="held"')

note "Took ${took}ms: $held held, $sold_out sold_out, $already already_holding, $other other."
expect "every one of the $TOTAL clicks got an answer" "$answered" "$TOTAL"
expect "exactly $STOCK pairs were held, never more" "$held" "$STOCK"
expect "the $STOCK pairs are held by $STOCK different buyers" "$distinct" "$held"
if [ "$spam_held" -le 1 ]; then
  pass "spammer clicked $SPAM_CLICKS times and holds $spam_held pair (at most 1)"
else
  fail "spammer holds $spam_held pairs"
fi
expect "every other click was turned away cleanly, no errors" "$other" 0

if [ "$held" -ne "$STOCK" ]; then
  fail "stopping: the later steps need exactly $STOCK winners"
  exit 1
fi

# Hold ids are handed out under the lock, so they are the exact order in
# which the server served the winners.
awk -F'\t' '$3=="held"' "$WORK/results.tsv" | sort -t$'\t' -k4,4n >"$WORK/winners.tsv"
WIN_USER=()
WIN_HOLD=()
step "Who got the $STOCK pairs, in the order the server granted them"
printf '       %-6s %-6s %-12s %s\n' order hold buyer "click #"
while IFS=$'\t' read -r click user _ hold; do
  WIN_USER+=("$user")
  WIN_HOLD+=("$hold")
  printf '       %-6s %-6s %-12s %s\n' "${#WIN_USER[@]}" "$hold" "$user" "$((10#$click))"
done <"$WORK/winners.tsv"
early=$(awk -F'\t' -v n="$STOCK" '$1+0 <= n' "$WORK/winners.tsv" | wc -l | tr -d ' ')
last=$(awk -F'\t' '{print $1+0}' "$WORK/winners.tsv" | sort -n | tail -1)
note "$early of the winners were among the first $STOCK clicks; the latest winning click was #$last."
note "The first $PARALLEL clicks leave at the same instant, so the network decides which"
note "reaches the server first. The server's lock is the referee: first in, next hold id."

# ---------------------------------------------------------------- 2. books

step "2. The books after the rush"
books=$(get /api/stats)
note "$books"
expect "no pairs left on sale" "$(jq -r .available <<<"$books")" 0
expect "$STOCK pairs held" "$(jq -r .held <<<"$books")" "$STOCK"
expect "available + held + sold = $STOCK" "$(jq -r '.available + .held + .sold' <<<"$books")" "$STOCK"

# ---------------------------------------------------------------- 3. payments

step "3. The first $PAID winners pay; the other $((STOCK - PAID)) never do"
pending=0
FIRST_EVENT=
for ((i = 0; i < PAID; i++)); do
  resp=$(post /api/pay "{\"user\":\"${WIN_USER[$i]}\",\"hold_id\":${WIN_HOLD[$i]}}")
  if [ "$(jq -r .status <<<"$resp")" = pending ]; then pending=$((pending + 1)); fi
  if [ "$i" -eq 0 ]; then
    FIRST_EVENT=$(jq -r .event_id <<<"$resp")
    # A double click on Pay: either the same payment comes back, or the hold
    # is already paid. Never a second charge.
    again=$(post /api/pay "{\"user\":\"${WIN_USER[0]}\",\"hold_id\":${WIN_HOLD[0]}}" | jq -r '.event_id // .error')
    if [ "$again" = "$FIRST_EVENT" ] || [ "$again" = hold_not_active ]; then
      pass "${WIN_USER[0]} clicked Pay twice and was charged once"
    else
      fail "double click on Pay gave '$again'"
    fi
  fi
done
expect "the payment company accepted all $PAID payments" "$pending" "$PAID"
sold_is_paid() { [ "$(stats_field sold)" = "$PAID" ]; }
wait_until "the payments to be confirmed" 10 sold_is_paid || true
sleep 2 # let the duplicate and stray failure messages land too
expect "each payment counted exactly once, despite the repeats" "$(stats_field sold)" "$PAID"
expect "no refunds owed" "$(stats_field refunds_due)" 0
note "Messages the shop received so far: $(grep -o -- '-> [a-z_]*' "$WORK/server.log" | sort | uniq -c | awk '{printf "%s %s, ", $1, $3}' | sed 's/, $//')"
expect "applied once per payment" "$(grep -c -- '-> applied' "$WORK/server.log")" "$PAID"

# ---------------------------------------------------------------- 4. line

step "4. Sold out, so $LINE people join the waiting line"
# First in line is a winner who has already bought one pair: allowed, since
# the limit is two. Then the earliest clickers who missed out.
LINE_USERS=("${WIN_USER[0]}")
while read -r user; do LINE_USERS+=("$user"); done < <(
  awk -F'\t' '$3=="sold_out" && $2!="spammer" {print $2}' "$WORK/results.tsv" | head -$((LINE - 1))
)
for ((i = 0; i < LINE; i++)); do
  pos=$(post /api/queue "{\"user\":\"${LINE_USERS[$i]}\"}" | jq -r '.position // .error')
  expect "${LINE_USERS[$i]} is number $((i + 1)) in line" "$pos" "$((i + 1))"
done
expect "joining again keeps your place" "$(post /api/queue "{\"user\":\"${LINE_USERS[1]}\"}" | jq -r .position)" 2
expect "someone holding a pair cannot also join the line" \
  "$(post /api/queue "{\"user\":\"${WIN_USER[$((STOCK - 1))]}\"}" | jq -r .error)" already_holding
expect "Buy while sold out says sold_out" "$(post /api/hold '{"user":"latecomer"}' | jq -r .error)" sold_out

# ---------------------------------------------------------------- 5. expiry

HANDOFFS=$((STOCK - PAID))
step "5. The $HANDOFFS unpaid holds run out; their pairs go to the line, in order"
note "Waiting for the ${TTL}s holds to expire..."
line_moved() { [ "$(stats_field waiting)" = "$((LINE - HANDOFFS))" ]; }
wait_until "the unpaid holds to expire" $((TTL + 5)) line_moved || true
for ((i = PAID; i < STOCK; i++)); do
  expect "${WIN_USER[$i]} did not pay and lost the hold" "$(seen "${WIN_USER[$i]}" '.hold == null')" true
done
for ((i = 0; i < HANDOFFS; i++)); do
  expect "${LINE_USERS[$i]} (number $((i + 1)) in line) was given a pair" "$(seen "${LINE_USERS[$i]}" '.hold != null')" true
done
for ((i = HANDOFFS; i < LINE; i++)); do
  expect "${LINE_USERS[$i]} moved up to number $((i - HANDOFFS + 1))" \
    "$(seen "${LINE_USERS[$i]}" .queue_position)" "$((i - HANDOFFS + 1))"
done
left=$(seen "${LINE_USERS[0]}" '.hold.expires_in_ms / 1000 | floor')
if [ "$left" -ge $((TTL - 3)) ]; then
  pass "a passed-on pair comes with a fresh hold of its own (${left}s of ${TTL}s left)"
else
  fail "the passed-on hold only had ${left}s left"
fi
expect "the freed pairs did not go back on sale" "$(stats_field available)" 0

# ---------------------------------------------------------------- 6. limit

U=${WIN_USER[0]}
step "6. Purchase limit: $U pays for a second pair, then tries for a third"
post /api/pay "{\"user\":\"$U\",\"hold_id\":$(seen "$U" .hold.id)}" >/dev/null
owns_two() { [ "$(seen "$U" .purchases)" = 2 ]; }
wait_until "the second payment" 10 owns_two || true
expect "$U now owns 2 pairs" "$(seen "$U" .purchases)" 2
expect "$U cannot get in line for a third" "$(post /api/queue "{\"user\":\"$U\"}" | jq -r .error)" limit_reached

# ---------------------------------------------------------------- 7. webhooks

step "7. The payment company misbehaves (the script sends these, signed with the shared secret)"
expect "a replay of an old success changes nothing" "$(webhook "$FIRST_EVENT" "${WIN_HOLD[0]}" success)" "200 duplicate"
expect "a failure arriving after the success does not undo it" "$(webhook evt_late_failure "${WIN_HOLD[0]}" failed)" "200 failure_noted"
expect "$U still owns 2 pairs" "$(seen "$U" .purchases)" 2
LATE_USER=${WIN_USER[$PAID]}
expect "money arriving after the hold ran out is flagged for refund" \
  "$(webhook evt_too_late "${WIN_HOLD[$PAID]}" success)" "200 refund_required"
expect "$LATE_USER gets a refund, not a pair" "$(seen "$LATE_USER" '"\(.purchases) bought, \(.refunds_due) refund"')" "0 bought, 1 refund"
expect "a message with a forged signature is rejected" \
  "$(webhook evt_forged "${WIN_HOLD[$((PAID + 1))]}" success wrong-secret)" "401 bad_signature"

# ---------------------------------------------------------------- 8. books

step "8. Final books"
books=$(get /api/stats)
note "$books"
expect "pairs sold" "$(jq -r .sold <<<"$books")" $((PAID + 1))
expect "pairs still held by people from the line" "$(jq -r .held <<<"$books")" $((HANDOFFS - 1))
expect "available + held + sold = $STOCK" "$(jq -r '.available + .held + .sold' <<<"$books")" "$STOCK"

echo
if [ "$FAILED" -eq 0 ]; then
  printf '\033[32mAll checks passed.\033[0m\n'
else
  printf '\033[31m%d check(s) failed.\033[0m\n' "$FAILED"
  exit 1
fi
