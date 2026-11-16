#!/usr/bin/env bash
# The Step 0 scenario: a scripted session against the public API, through pli
# only, as two users (ana and bob), an agent (triage-bot, for ana) and a user
# in another namespace (carol). It is the proof of concept's end-to-end test.
#
#   scripts/scenario.sh          (or: make scenario, which starts the stack first)
#
# It needs the stack running (docker compose up -d --wait), Go (to build
# pli) and jq. It exits non-zero on the first wrong outcome and says which
# step failed. Saved agent sessions go to a temporary XDG_CONFIG_HOME, never
# to your own config; the temporary directory is removed on exit. The Items it
# creates stay (there is no delete): each run tags them with its own word, so
# runs never see each other's. Reset with docker compose down -v.
#
# Environment: PLI_SERVER (http://localhost:8080), PLI_TOKENS
# (dev-tokens.example.yaml), PLI (a pli binary; default: build one).
#
# Written for bash 3.2 (macOS's): no associative arrays, no EPOCHREALTIME.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
server=${PLI_SERVER:-http://localhost:8080}
tokens=${PLI_TOKENS:-$repo/dev-tokens.example.yaml}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/pli-scenario.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

step_name="setup"
step() {
	step_name=$1
	printf '==> %s\n' "$1"
}

fail() {
	{
		printf '\nFAILED at step: %s\n' "$step_name"
		printf '  %s\n' "$@"
		if [ -s "$tmp/err" ]; then
			printf '  pli stderr:\n'
			sed 's/^/    /' "$tmp/err"
		fi
	} >&2
	exit 1
}

for tool in jq go; do
	if [ "$tool" = go ] && [ -n "${PLI:-}" ]; then continue; fi
	command -v "$tool" >/dev/null || fail "$tool is required (see the README's prerequisites)"
done

# Build pli before XDG_CONFIG_HOME changes: the go command keeps its own
# settings (go env -w) under the config directory.
if [ -n "${PLI:-}" ]; then
	pli_bin=$PLI
else
	step "build pli"
	(cd "$repo" && go build -o "$tmp/pli" ./cmd/pli) || fail "go build ./cmd/pli failed"
	pli_bin=$tmp/pli
fi
export XDG_CONFIG_HOME="$tmp/config"

# run WHO ARGS...: runs pli as WHO; stdout in $tmp/out, stderr in $tmp/err,
# exit status in $rc.
rc=0
run() {
	local who=$1
	shift
	rc=0
	"$pli_bin" --server "$server" --tokens "$tokens" --as "$who" "$@" >"$tmp/out" 2>"$tmp/err" || rc=$?
}

# ok WHO ARGS...: pli must succeed; prints its JSON.
ok() {
	run "$@"
	[ "$rc" -eq 0 ] || fail "pli --as $* exited $rc, want success"
	cat "$tmp/out"
}

# Refusals seen so far, as " REASON REASON ".
seen=" "

# refuse WHO "CODE REASON" ARGS...: pli must exit 1 with that code and
# reason. The error lines stay in $tmp/err for metadata checks.
refuse() {
	local who=$1 want=$2
	shift 2
	local first
	run "$who" "$@"
	[ "$rc" -eq 1 ] || fail "pli --as $who $* exited $rc, want 1 (API error $want)"
	first=$(head -n 1 "$tmp/err")
	case $first in
	"error: $want: "*) ;;
	*) fail "pli --as $who $* answered: $first" "want: error: $want: ..." ;;
	esac
	seen="$seen${want#* } "
}

# meta KEY: the value of an ErrorInfo metadata line ("  key: value") of the
# last error.
meta() {
	sed -n "s/^  $1: //p" "$tmp/err" | head -n 1
}

# expect WHAT GOT WANT
expect() {
	[ "$2" = "$3" ] || fail "$1: got '$2', want '$3'"
}

# err_has TEXT: the last error output contains TEXT.
err_has() {
	grep -qF -- "$1" "$tmp/err" || fail "pli's stderr does not contain: $1"
}

j() { jq -r "$1"; }

# names: the Item names of a search or list answer, sorted, on one line.
names() { jq -r '[.items[].name] | sort | join(" ")'; }

# sorted NAME...: the names sorted, on one line, to compare with names.
sorted() { printf '%s\n' "$@" | sort | tr '\n' ' ' | sed 's/ $//'; }

# An ID no Item has (canonical UUID form).
random_uuid() {
	local h
	h=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	printf '%s-%s-%s-%s-%s\n' "${h:0:8}" "${h:8:4}" "${h:12:4}" "${h:16:4}" "${h:20:12}"
}

# This run's word: every title carries it, so searches see only this run.
w="run$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"

step "the server answers (is the stack up? docker compose up -d --wait)"
run ana type list
[ "$rc" -eq 0 ] || fail "pli could not reach $server as ana (exit $rc)"
types=$(j '[.types[].name] | join(" ")' <"$tmp/out")
case " $types " in
*" namespaces/acme/types/ticket/versions/4 "*) ;;
*) fail "type list does not have ticket v4: $types" ;;
esac

# ---------------------------------------------------------------------------
# Two users: ana creates, bob reads and moves the work on.
# ---------------------------------------------------------------------------

new_ticket() { # WHO [--set ...]...: a ticket@4 tagged with this run's word; prints its name
	local who=$1
	shift
	ok "$who" item create --type ticket@4 --set "f_title=$w" "$@" | j .item.name
}

step "ana creates a ticket"
created=$(ok ana item create --type ticket@4 --set "f_title=printer $w" --set f_priority=2 \
	--set f_customer_id=cust-good --set f_amount=100)
t1=$(j .item.name <<<"$created")
etag1=$(j .item.etag <<<"$created")
expect "creator" "$(j .item.creator <<<"$created")" "user:ana"
expect "state" "$(j .item.state <<<"$created")" "triage"

step "bob reads it"
got=$(ok bob item get "$t1")
expect "bob sees the title" "$(j .item.fields.f_title <<<"$got")" "printer $w"
expect "bob sees the amount (an int, as a string)" "$(j .item.fields.f_amount <<<"$got")" "100"

step "bob assigns it to himself and moves it to in_progress"
ok bob item update "$t1" --set f_assignee=bob >/dev/null
ok bob item transition "$t1" --to open >/dev/null
moved=$(ok bob item transition "$t1" --to in_progress)
expect "outcome" "$(j .outcome <<<"$moved")" "TRANSITION_OUTCOME_APPLIED"

step "ana writes with the etag she read before: STALE"
refuse ana "ABORTED STALE" item update "$t1" --set f_priority=3 --etag "$etag1"

step "bob approves with --wait 5s: the ERP check (1 s) holds it, then allows (APPLIED)"
began=$SECONDS
approved=$(ok bob item transition "$t1" --to approved --wait 5s)
took=$((SECONDS - began))
expect "held approve's outcome (took ${took}s)" "$(j .outcome <<<"$approved")" "TRANSITION_OUTCOME_APPLIED"
expect "state after approve" "$(j .item.state <<<"$approved")" "approved"
echo "    the held approve answered in about ${took}s"

step "a second ticket: bob approves with --wait 0s (PENDING); ana's write meanwhile is HELD"
t2=$(new_ticket ana --set f_customer_id=cust-good --set f_amount=200 --set f_assignee=bob)
ok bob item transition "$t2" --to open >/dev/null
ok bob item transition "$t2" --to in_progress >/dev/null
pending=$(ok bob item transition "$t2" --to approved --wait 0s)
expect "outcome" "$(j .outcome <<<"$pending")" "TRANSITION_OUTCOME_PENDING"
hold=$(j .hold <<<"$pending")
case $hold in "$t2"/holds/*) ;; *) fail "PENDING names hold '$hold', want one under $t2" ;; esac
refuse ana "ABORTED HELD" item update "$t2" --set f_amount=5
expect "HELD names the hold" "$(meta hold)" "$hold"

step "a third ticket over the credit limit: the check denies it (DENIED is an answer, not an error)"
t3=$(new_ticket ana --set f_customer_id=cust-good --set f_amount=5000 --set f_assignee=bob)
ok bob item transition "$t3" --to open >/dev/null
ok bob item transition "$t3" --to in_progress >/dev/null
denied=$(ok bob item transition "$t3" --to approved --wait 5s)
expect "outcome" "$(j .outcome <<<"$denied")" "TRANSITION_OUTCOME_DENIED"
last_seq=$(j .commit_seq <<<"$denied")

step "search with filters (read-your-writes with --min-seq)"
found=$(ok ana item search "$w" --state approved --min-seq "$last_seq")
expect "approved tickets" "$(names <<<"$found")" "$t1"
found=$(ok bob item search --type ticket --where "f_title=$w" --where 'f_amount>=150' --min-seq "$last_seq")
expect "tickets titled with the run's word, amount >= 150" "$(names <<<"$found")" "$(sorted "$t2" "$t3")"
# Active: approved (t1), and in_progress (t2 held, t3 after its denial).
found=$(ok bob item search "$w" --category active --type ticket)
expect "active tickets" "$(names <<<"$found")" "$(sorted "$t1" "$t2" "$t3")"

step "list a container, one page at a time"
board=$(ok ana item create --type channel@1 --set "f_name=board $w" | j .item.name)
a=$(ok ana item create --type ticket@4 --in "$board" --set "f_title=first $w" | j .item.name)
b=$(ok ana item create --type ticket@4 --in "$board" --set "f_title=second $w" | j .item.name)
page=$(ok bob item list "$board" --page-size 1)
expect "page 1" "$(j '[.items[].name] | join(" ")' <<<"$page")" "$b"
next=$(j .next_page_token <<<"$page")
[ -n "$next" ] && [ "$next" != null ] || fail "page 1 has no next_page_token"
page=$(ok bob item list "$board" --page-size 1 --page-token "$next")
expect "page 2" "$(j '[.items[].name] | join(" ")' <<<"$page")" "$a"

step "read the history"
history=$(ok bob item history "$t1")
expect "first event" "$(j '.events[0].kind' <<<"$history")" "ITEM_EVENT_KIND_CREATED"
expect "first event's actor" "$(j '.events[0].actor' <<<"$history")" "user:ana"
expect "first event's title" "$(j '.events[0].fields.f_title' <<<"$history")" "printer $w"
[ "$(j '[.events[] | select(.kind == "ITEM_EVENT_KIND_TRANSITIONED")] | length' <<<"$history")" -ge 3 ] ||
	fail "history has fewer than 3 transitions: $(j '[.events[].kind] | join(" ")' <<<"$history")"

step "a missing Item and a hidden one are the same NOT_FOUND"
refuse bob "NOT_FOUND NOT_FOUND" item get "namespaces/acme/items/$(random_uuid)"
private=$(new_ticket ana --narrow user:ana)
ok ana item get "$private" >/dev/null
refuse bob "NOT_FOUND NOT_FOUND" item get "$private"

# ---------------------------------------------------------------------------
# Every other refusal of the error table, once.
# ---------------------------------------------------------------------------

step "UNAUTHENTICATED: a token the server does not know"
# A copy of the token file with one more entry, mallory, whose token the
# server never loaded.
awk '{ print } /^tokens:/ { print "  - {name: mallory, token: not-a-token-the-server-knows, principal: \"user:mallory\", namespace: acme}" }' \
	"$tokens" >"$tmp/tokens.yaml"
tokens_saved=$tokens
tokens=$tmp/tokens.yaml
refuse mallory "UNAUTHENTICATED UNAUTHENTICATED" type list
tokens=$tokens_saved

step "SESSION_INVALID: an agent session that does not exist, with pli's hint"
refuse triage-bot "UNAUTHENTICATED SESSION_INVALID" --session "ses_$(random_uuid)" item get "$t1"
err_has "hint:"
err_has "pli agent start"

step "INVALID_REQUEST: a value that is not an int"
refuse ana "INVALID_ARGUMENT INVALID_REQUEST" item update "$t1" --set f_priority=high

step "NOT_ALLOWED: ana does not hold role:approver"
refuse ana "PERMISSION_DENIED NOT_ALLOWED" item transition "$t3" --to approved

step "MISSING_FIELDS: resolving needs f_resolution"
refuse bob "FAILED_PRECONDITION MISSING_FIELDS" item transition "$t3" --to resolved
expect "fields" "$(meta fields)" "f_resolution"

t4=$(new_ticket ana)
step "NO_TRANSITION: triage cannot go to resolved"
refuse ana "FAILED_PRECONDITION NO_TRANSITION" item transition "$t4" --to resolved

step "GUARD_FAILED: in_progress needs an assignee"
ok ana item transition "$t4" --to open >/dev/null
refuse ana "FAILED_PRECONDITION GUARD_FAILED" item transition "$t4" --to in_progress
expect "from_state" "$(meta from_state)" "open"
expect "to_state" "$(meta to_state)" "in_progress"

step "WIDEN_NOT_CONFIRMED: moving out of a narrowed container must be confirmed"
den=$(ok ana item create --type channel@1 --set "f_name=den $w" --narrow user:ana | j .item.name)
inden=$(ok ana item create --type ticket@4 --in "$den" --set "f_title=in the den $w" | j .item.name)
refuse ana "FAILED_PRECONDITION WIDEN_NOT_CONFIRMED" item update "$inden" --move-to-top
ok ana item update "$inden" --move-to-top --confirm-widen >/dev/null

step "IDEMPOTENCY_KEY_REUSED: one request_id, two different requests"
key="scenario-$w"
first=$(ok ana item create --type ticket@4 --set "f_title=once $w" --request-id "$key")
again=$(ok ana item create --type ticket@4 --set "f_title=once $w" --request-id "$key")
expect "the replay is the same Item" "$(j .item.name <<<"$again")" "$(j .item.name <<<"$first")"
refuse ana "ALREADY_EXISTS IDEMPOTENCY_KEY_REUSED" item create --type ticket@4 --set "f_title=twice $w" --request-id "$key"

step "NOT_CAUGHT_UP: a min_seq far ahead (about 2 s)"
refuse ana "UNAVAILABLE NOT_CAUGHT_UP" item search "$w" --min-seq 999999999999

# ---------------------------------------------------------------------------
# An agent in capability mode: ana's grant covers all of acme; the session
# asks for one container.
# ---------------------------------------------------------------------------

step "ana sets up a project: a restricted room, and inside it a narrower one"
room=$(ok ana item create --type channel@1 --set "f_name=project $w" \
	--narrow user:ana,user:bob,agent:triage-bot | j .item.name)
inner=$(ok ana item create --type channel@1 --in "$room" --set "f_name=secrets $w" \
	--narrow user:ana,agent:triage-bot | j .item.name)
in_room=$(ok ana item create --type ticket@4 --in "$room" --set "f_title=in the room $w" | j .item.name)
in_inner=$(ok ana item create --type ticket@4 --in "$inner" --set "f_title=in the inner room $w" | j .item.name)
outside=$(new_ticket ana)

step "triage-bot starts a session for ana, scoped to the room only"
session=$(ok triage-bot agent start --for user:ana --scopes "$room" --powers read,edit_fields | j .session.id)
[ -s "$XDG_CONFIG_HOME/pli/sessions/triage-bot" ] || fail "the session was not saved under XDG_CONFIG_HOME"

step "inside its scope the agent reads"
ok triage-bot item get "$in_room" >/dev/null
ok triage-bot item get "$in_inner" >/dev/null

step "outside its scope nothing exists for it"
refuse triage-bot "NOT_FOUND NOT_FOUND" item get "$outside"
refuse triage-bot "NOT_FOUND NOT_FOUND" item update "$outside" --set f_priority=1

step "it read the inner room, so a write to the wider room needs ana's approval"
refuse triage-bot "PERMISSION_DENIED NEEDS_APPROVAL" item update "$in_room" --set f_priority=1
scope=$(meta scope)
expect "NEEDS_APPROVAL's scope" "$scope" "$in_room"
expect "NEEDS_APPROVAL's audience" "$(meta audience)" "restricted"

step "AUDIENCE_CHANGED: approving for an audience that is not the one at commit"
refuse ana "FAILED_PRECONDITION AUDIENCE_CHANGED" agent approve "$session" "$scope" --expect-audience namespace --yes
expect "audience" "$(meta audience)" "restricted"
expect "expected_audience" "$(meta expected_audience)" "namespace"
refuse triage-bot "PERMISSION_DENIED NEEDS_APPROVAL" item update "$in_room" --set f_priority=1

step "ana approves (pli says who will see the content); the agent's retry works"
ok ana agent approve "$session" "$scope" --yes >/dev/null
err_has "Content from this agent session will become visible to the readers of $scope"
ok triage-bot item update "$in_room" --set f_priority=1 >/dev/null

# ---------------------------------------------------------------------------
# Another namespace: carol, in globex, reaches nothing of acme, on every
# operation that can name acme; and acme reaches nothing of hers.
# ---------------------------------------------------------------------------

step "cross-namespace: carol (globex) on every operation that names acme"
refuse carol "NOT_FOUND NOT_FOUND" item get "$t1"
refuse carol "NOT_FOUND NOT_FOUND" item update "$t1" --set f_priority=9
refuse carol "NOT_FOUND NOT_FOUND" item transition "$t1" --to resolved --set f_resolution=fixed
refuse carol "NOT_FOUND NOT_FOUND" item list "$board"
refuse carol "NOT_FOUND NOT_FOUND" item history "$t1"
refuse carol "NOT_FOUND NOT_FOUND" item search --type namespaces/acme/types/ticket
refuse carol "NOT_FOUND NOT_FOUND" item create --type namespaces/acme/types/ticket/versions/4
refuse carol "NOT_FOUND NOT_FOUND" item create --type ticket@4 --in "$board"
refuse carol "NOT_FOUND NOT_FOUND" type get namespaces/acme/types/ticket/versions/4
refuse carol "NOT_FOUND NOT_FOUND" agent approve "$session" "$room" --yes
# ListTypes names no namespace: it lists the caller's own.

step "cross-namespace: acme on carol's Item"
theirs=$(ok carol item create --type ticket@4 --set "f_title=globex $w" | j .item.name)
case $theirs in namespaces/globex/items/*) ;; *) fail "carol's Item is $theirs, want it in globex" ;; esac
refuse ana "NOT_FOUND NOT_FOUND" item get "$theirs"
refuse triage-bot "NOT_FOUND NOT_FOUND" agent start --for user:ana --scopes "$theirs" --powers read

# ---------------------------------------------------------------------------
# Every reason of the error table a client can cause came back once.
# ---------------------------------------------------------------------------

step "every reachable refusal was seen"
for reason in UNAUTHENTICATED SESSION_INVALID INVALID_REQUEST NOT_FOUND NOT_ALLOWED NEEDS_APPROVAL \
	NO_TRANSITION MISSING_FIELDS GUARD_FAILED WIDEN_NOT_CONFIRMED STALE HELD \
	IDEMPOTENCY_KEY_REUSED NOT_CAUGHT_UP AUDIENCE_CHANGED; do
	case $seen in *" $reason "*) ;; *) fail "no step triggered $reason" ;; esac
done
cat <<'EOF'
    Not triggered here (a client cannot cause them; Go tests cover each):
      NOT_INITIAL_STATE  a create cannot name a state through the API
      HOP_LIMIT          only chains of hook writes reach it
      INTERNAL           a bug, by definition
      CANCELED           decided by the client, which then gets no answer
      DEADLINE_EXCEEDED  likewise; pli sets no deadline
EOF

echo
echo "scenario passed (run word $w)"
