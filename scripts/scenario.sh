#!/usr/bin/env bash
# The proof of concept's scenario (Steps 0 to 2): a scripted session against
# the public API, through pli only, as users (ana and bob; dana and erin for
# the vacation request; hana, HR, for leave balances), an agent (triage-bot, for ana and for bob) and a
# user in another namespace (carol). It is the proof of concept's end-to-end
# test. Step 1 added links (blocks / blocked_by, duplicate_of) and the
# principal field f_assignee, on ticket v5. Step 2, part 1 added the vacation
# request: an approval as a business state, with the HR check. Step 2, part
# 2 added the leave balance: the held deduction, its refund, the race. Part 3
# added time and mail (reminders, expiry, a timer deferred by a hold, the
# emails), on the stack's dev clock; it needs curl.
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
# Written for bash 3.2 (macOS's): no associative arrays, no EPOCHREALTIME;
# milliseconds come from perl's Time::HiRes when there is one.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
server=${PLI_SERVER:-http://localhost:8080}
tokens=${PLI_TOKENS:-$repo/dev-tokens.example.yaml}

tmp=$(mktemp -d "${TMPDIR:-/tmp}/pli-scenario.XXXXXX")
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

step_name="setup"
step() {
	check_failed
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
	# Inside $(...) exit leaves only the subshell, and the script would go
	# on with an empty answer. The marker makes the parent stop too (see
	# check_failed): a failure can never pass as "nothing".
	: >"$tmp/failed"
	exit 1
}

# check_failed stops the script if a fail ran in a subshell.
check_failed() {
	if [ -e "$tmp/failed" ]; then
		printf '\nFAILED inside a command substitution, at step: %s (see above)\n' "$step_name" >&2
		exit 1
	fi
}

for tool in jq go curl; do
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
	check_failed # GOT may come from a subshell that failed
	[ "$2" = "$3" ] || fail "$1: got '$2', want '$3'"
}

# err_has TEXT: the last error output contains TEXT.
err_has() {
	grep -qF -- "$1" "$tmp/err" || fail "pli's stderr does not contain: $1"
}

# jqx ARGS...: jq on stdin, failing the script if jq itself fails (a
# malformed answer, or a filter that does not fit it): a jq error must never
# read as "no results".
jqx() {
	local out
	out=$(jq "$@") || fail "jq $* failed on pli's answer"
	printf '%s\n' "$out"
}

j() { jqx -r "$1"; }

# names: the Item names of a search or list answer, sorted, on one line. An
# answer with no Items has no "items" key at all: that is "", not an error.
names() { jqx -r '[.items[]?.name] | sort | join(" ")'; }

# sorted NAME...: the names sorted, on one line, to compare with names.
sorted() { printf '%s\n' "$@" | sort | tr '\n' ' ' | sed 's/ $//'; }

# An ID no Item has (canonical UUID form).
random_uuid() {
	local h
	h=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	printf '%s-%s-%s-%s-%s\n' "${h:0:8}" "${h:8:4}" "${h:12:4}" "${h:16:4}" "${h:20:12}"
}

# now_ms: the time in milliseconds, for the timings this script prints.
if perl -MTime::HiRes -e 1 2>/dev/null; then
	now_ms() { perl -MTime::HiRes=time -e 'printf "%d\n", time() * 1000'; }
else
	echo "note: no perl with Time::HiRes here; timings are in whole seconds"
	now_ms() { echo $(($(date +%s) * 1000)); }
fi

# This run's word: every title carries it, so searches see only this run.
w="run$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"

step "the server answers (is the stack up? docker compose up -d --wait)"
run ana type list
[ "$rc" -eq 0 ] || fail "pli could not reach $server as ana (exit $rc)"
types=$(j '[.types[].name] | join(" ")' <"$tmp/out")
case " $types " in
*" namespaces/acme/types/ticket/versions/5 "*) ;;
*) fail "type list does not have ticket v5: $types" ;;
esac

# ---------------------------------------------------------------------------
# Two users: ana creates, bob reads and moves the work on.
# ---------------------------------------------------------------------------

new_ticket() { # WHO [--set ...]...: a ticket@5 tagged with this run's word; prints its name
	local who=$1
	shift
	ok "$who" item create --type ticket@5 --set "f_title=$w" "$@" | j .item.name
}

step "ana creates a ticket"
created=$(ok ana item create --type ticket@5 --set "f_title=printer $w" --set f_priority=2 \
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
ok bob item update "$t1" --set f_assignee=user:bob >/dev/null
ok bob item transition "$t1" --to open >/dev/null
moved=$(ok bob item transition "$t1" --to in_progress)
expect "outcome" "$(j .outcome <<<"$moved")" "TRANSITION_OUTCOME_APPLIED"

step "ana writes with the etag she read before: STALE"
refuse ana "ABORTED STALE" item update "$t1" --set f_priority=3 --etag "$etag1"

step "bob approves with --wait 5s: the ERP check (1 s) holds it, then allows (APPLIED)"
began=$(now_ms)
approved=$(ok bob item transition "$t1" --to approved --wait 5s)
took=$(($(now_ms) - began))
expect "held approve's outcome (took ${took} ms)" "$(j .outcome <<<"$approved")" "TRANSITION_OUTCOME_APPLIED"
expect "state after approve" "$(j .item.state <<<"$approved")" "approved"
echo "    the held approve answered in ${took} ms"

step "a second ticket: bob approves with --wait 0s (PENDING); ana's write meanwhile is HELD"
t2=$(new_ticket ana --set f_customer_id=cust-good --set f_amount=200 --set f_assignee=user:bob)
ok bob item transition "$t2" --to open >/dev/null
ok bob item transition "$t2" --to in_progress >/dev/null
pending=$(ok bob item transition "$t2" --to approved --wait 0s)
pending_at=$(now_ms)
expect "outcome" "$(j .outcome <<<"$pending")" "TRANSITION_OUTCOME_PENDING"
hold=$(j .hold <<<"$pending")
case $hold in "$t2"/holds/*) ;; *) fail "PENDING names hold '$hold', want one under $t2" ;; esac
refuse ana "ABORTED HELD" item update "$t2" --set f_amount=5
expect "HELD names the hold" "$(meta hold)" "$hold"

step "a third ticket over the credit limit: the check denies it (DENIED is an answer, not an error)"
t3=$(new_ticket ana --set f_customer_id=cust-good --set f_amount=5000 --set f_assignee=user:bob)
ok bob item transition "$t3" --to open >/dev/null
ok bob item transition "$t3" --to in_progress >/dev/null
began=$(now_ms)
denied=$(ok bob item transition "$t3" --to approved --wait 5s)
echo "    the denied approve answered in $(($(now_ms) - began)) ms"
expect "outcome" "$(j .outcome <<<"$denied")" "TRANSITION_OUTCOME_DENIED"
last_seq=$(j .commit_seq <<<"$denied")

step "search with filters (read-your-writes with --min-seq)"
# t1 is approved and t3 is not; t2's held approve is decided by the ERP in
# the background (about 1 s after it was asked), so it may be either here.
found=$(ok ana item search "$w" --state approved --min-seq "$last_seq")
case " $(names <<<"$found") " in *" $t1 "*) ;; *) fail "approved tickets do not include t1: $(names <<<"$found")" ;; esac
case " $(names <<<"$found") " in *" $t3 "*) fail "approved tickets include t3, which was denied" ;; esac
found=$(ok bob item search --type ticket --where "f_title=$w" --where 'f_amount>=150' --min-seq "$last_seq")
expect "tickets titled with the run's word, amount >= 150" "$(names <<<"$found")" "$(sorted "$t2" "$t3")"
# Active: approved (t1), in_progress (t3 after its denial), and t2 either way
# (in_progress while held, approved once the ERP allows it).
found=$(ok bob item search "$w" --category active --type ticket)
expect "active tickets" "$(names <<<"$found")" "$(sorted "$t1" "$t2" "$t3")"

step "the PENDING approve completes: the worker finishes t2's held transition"
# This tests the asynchronous outcome of PENDING, not a retry: nothing is sent
# again. The worker asks the ERP (1 s) and finishes the held transition; the
# script only reads t2, at most every 250 ms, until it is approved, and fails
# if that takes more than 5 s.
began=$(now_ms)
while :; do
	state=$(ok ana item get "$t2" | j .item.state)
	[ "$state" = approved ] && break
	elapsed=$(($(now_ms) - began))
	[ "$elapsed" -lt 5000 ] || fail "t2 is still '$state' after ${elapsed} ms of reading it; want approved within 5 s"
	sleep 0.25
done
echo "    t2 was approved $(($(now_ms) - pending_at)) ms after its PENDING answer"

step "list a container, one page at a time"
board=$(ok ana item create --type channel@1 --set "f_name=board $w" | j .item.name)
a=$(ok ana item create --type ticket@5 --in "$board" --set "f_title=first $w" | j .item.name)
b=$(ok ana item create --type ticket@5 --in "$board" --set "f_title=second $w" | j .item.name)
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
inden=$(ok ana item create --type ticket@5 --in "$den" --set "f_title=in the den $w" | j .item.name)
refuse ana "FAILED_PRECONDITION WIDEN_NOT_CONFIRMED" item update "$inden" --move-to-top
ok ana item update "$inden" --move-to-top --confirm-widen >/dev/null

step "IDEMPOTENCY_KEY_REUSED: one request_id, two different requests"
key="scenario-$w"
first=$(ok ana item create --type ticket@5 --set "f_title=once $w" --request-id "$key")
again=$(ok ana item create --type ticket@5 --set "f_title=once $w" --request-id "$key")
expect "the replay is the same Item" "$(j .item.name <<<"$again")" "$(j .item.name <<<"$first")"
refuse ana "ALREADY_EXISTS IDEMPOTENCY_KEY_REUSED" item create --type ticket@5 --set "f_title=twice $w" --request-id "$key"

step "NOT_CAUGHT_UP: a min_seq far ahead (about 2 s)"
refuse ana "UNAVAILABLE NOT_CAUGHT_UP" item search "$w" --min-seq 999999999999


# ---------------------------------------------------------------------------
# Links (Step 1): blocks / blocked_by (many to many) and duplicate_of (one
# from a ticket) on ticket v5, and the principal field f_assignee. A link is
# an event on its source only; guards and search see links.
# ---------------------------------------------------------------------------

# Link-section tickets also carry their own word, so searches see only them.
lw="lnk$(od -An -N4 -tx1 /dev/urandom | tr -d ' \n')"
link_ticket() { # WHO [--set ...]...: a ticket@5 titled with both words; prints its name
	local who=$1
	shift
	ok "$who" item create --type ticket@5 --set "f_title=$w $lw" "$@" | j .item.name
}
# links_of: an Item answer's links as "DIRECTION link_type name", one line.
links_of() { jqx -r '[.item.links[]? | "\(.direction | sub("LINK_DIRECTION_"; "")) \(.link_type) \(if .item.restricted then "restricted" else .item.name end)"] | sort | join(",")'; }
# link_events: a history's link events as "KIND link_type target", one line.
link_events() { jqx -r '[.events[] | select(.kind | test("LINK")) | "\(.kind | sub("ITEM_EVENT_KIND_"; "")) \(.link_type) \(.link_target.name // "restricted")"] | join(",")'; }

step "links: ana links x blocks y; the link shows from both ends; the event is x's only"
x=$(link_ticket ana --set f_assignee=user:bob)
y=$(link_ticket ana --set f_assignee=user:bob)
linked=$(ok ana item link "$x" blocks "$y")
expect "x's links in the answer" "$(links_of <<<"$linked")" "OUTGOING blocks $y"
expect "y's links, as bob" "$(ok bob item get "$y" | links_of)" "INCOMING blocked_by $x"
expect "x's link events" "$(ok bob item history "$x" | link_events)" "LINK_ADDED blocks $y"
expect "y's link events (none: a link writes only on its source)" "$(ok bob item history "$y" | link_events)" ""

step "GUARD_FAILED from a link: y cannot be resolved while x (in triage) blocks it"
ok bob item transition "$y" --to open >/dev/null
ok bob item transition "$y" --to in_progress >/dev/null
refuse bob "FAILED_PRECONDITION GUARD_FAILED" item transition "$y" --to resolved --set f_resolution=fixed
expect "from_state" "$(meta from_state)" "in_progress"
expect "to_state" "$(meta to_state)" "resolved"

step "x is canceled: a canceled blocker does not block, and y resolves"
ok ana item transition "$x" --to canceled >/dev/null
resolved=$(ok bob item transition "$y" --to resolved --set f_resolution=fixed)
expect "y's state" "$(j .item.state <<<"$resolved")" "resolved"

step "unlink; NOT_LINKED, ALREADY_LINKED, LINK_TARGET_TYPE; STALE on the source's etag"
ok ana item unlink "$x" blocks "$y" >/dev/null
expect "y's links after the unlink" "$(ok bob item get "$y" | links_of)" ""
expect "x's link events" "$(ok bob item history "$x" | link_events)" "LINK_ADDED blocks $y,LINK_REMOVED blocks $y"
refuse ana "FAILED_PRECONDITION NOT_LINKED" item unlink "$x" blocks "$y"
ok ana item link "$x" blocks "$y" >/dev/null
refuse ana "FAILED_PRECONDITION ALREADY_LINKED" item link "$x" blocks "$y"
refuse ana "FAILED_PRECONDITION LINK_TARGET_TYPE" item link "$x" blocks "$board"
refuse ana "ABORTED STALE" item link "$x" duplicate_of "$y" --etag 1

step "LINK_CARDINALITY: a ticket is a duplicate of one ticket at most"
d=$(link_ticket ana)
ok ana item link "$d" duplicate_of "$x" >/dev/null
refuse ana "FAILED_PRECONDITION LINK_CARDINALITY" item link "$d" duplicate_of "$y"
expect "link_type" "$(meta link_type)" "duplicate_of"
expect "side" "$(meta side)" "source"
expect "x's links, as ana" "$(ok ana item get "$x" | links_of)" "INCOMING duplicates $d,OUTGOING blocks $y"

step "hidden ends: an outgoing link to ana's private ticket is restricted for bob; an incoming one is left out"
y2=$(link_ticket ana --set f_assignee=user:bob)
ok ana item link "$private" blocks "$y2" >/dev/null
ok ana item link "$y2" duplicate_of "$private" >/dev/null
expect "y2 as ana" "$(ok ana item get "$y2" | links_of)" "INCOMING blocked_by $private,OUTGOING duplicate_of $private"
expect "y2 as bob" "$(ok bob item get "$y2" | links_of)" "OUTGOING duplicate_of restricted"

step "the hidden blocker still blocks bob, and the refusal names nothing about it"
ok bob item transition "$y2" --to open >/dev/null
ok bob item transition "$y2" --to in_progress >/dev/null
refuse bob "FAILED_PRECONDITION GUARD_FAILED" item transition "$y2" --to resolved --set f_resolution=fixed
if grep -qF -- "${private##*/}" "$tmp/err"; then fail "the refusal names the hidden blocker"; fi
refuse bob "NOT_FOUND NOT_FOUND" item link "$y2" blocks "$private"

step "principal fields: f_assignee takes user:<id> or agent:<id>"
refuse ana "INVALID_ARGUMENT INVALID_REQUEST" item update "$d" --set f_assignee=bob

step "search: assigned to me, and linked to"
found=$(ok bob item search "$lw" --type ticket --where f_assignee=me)
expect "assigned to bob" "$(names <<<"$found")" "$(sorted "$x" "$y" "$y2")"
expect "assigned to ana" "$(ok ana item search "$lw" --type ticket --where f_assignee=me | names)" ""
expect "blocked by x" "$(ok bob item search --linked "in:blocked_by=$x" | names)" "$y"
expect "blocking y" "$(ok bob item search --linked "out:blocks=$y" | names)" "$x"
expect "blocking y2, as ana" "$(ok ana item search --linked "out:blocks=$y2" | names)" "$private"
expect "blocking y2, as bob (the blocker is hidden)" "$(ok bob item search --linked "out:blocks=$y2" | names)" ""
refuse bob "NOT_FOUND NOT_FOUND" item search --linked "in:blocked_by=$private"

# ---------------------------------------------------------------------------
# An agent in capability mode: ana's grant covers all of acme; the session
# asks for one container.
# ---------------------------------------------------------------------------

step "ana sets up a project: a restricted room, and inside it a narrower one"
room=$(ok ana item create --type channel@1 --set "f_name=project $w" \
	--narrow user:ana,user:bob,agent:triage-bot | j .item.name)
inner=$(ok ana item create --type channel@1 --in "$room" --set "f_name=secrets $w" \
	--narrow user:ana,agent:triage-bot | j .item.name)
in_room=$(ok ana item create --type ticket@5 --in "$room" --set "f_title=in the room $w" | j .item.name)
in_inner=$(ok ana item create --type ticket@5 --in "$inner" --set "f_title=in the inner room $w" | j .item.name)
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

step "a link is a write to both ends: from the inner room to the wider one needs approval on the target's side"
refuse triage-bot "PERMISSION_DENIED NEEDS_APPROVAL" item link "$in_inner" blocks "$in_room"
expect "NEEDS_APPROVAL names the target" "$(meta scope)" "$in_room"

step "AUDIENCE_CHANGED: approving for an audience that is not the one at commit"
refuse ana "FAILED_PRECONDITION AUDIENCE_CHANGED" agent approve "$session" "$scope" --expect-audience namespace --yes
expect "audience" "$(meta audience)" "restricted"
expect "expected_audience" "$(meta expected_audience)" "namespace"
refuse triage-bot "PERMISSION_DENIED NEEDS_APPROVAL" item update "$in_room" --set f_priority=1

step "ana approves (pli says who will see the content); the agent's retry works"
ok ana agent approve "$session" "$scope" --yes >/dev/null
err_has "Content from this agent session will become visible to the readers of $scope"
ok triage-bot item update "$in_room" --set f_priority=1 >/dev/null
ok triage-bot item link "$in_inner" blocks "$in_room" >/dev/null

# ---------------------------------------------------------------------------
# Step 2, part 1: the vacation request. Approval is a business state: the HR
# check names the approver on the way into submitted, the terms freeze, and
# the manager decides by an ordinary transition. ana reports to bob, dana to
# herself; erin is not in the HR directory (deploy/hr-directory.json).
# Since part 3 the requests are vacation_request@3 (part 2 added the
# balance: v2; part 3 time and mail: v3). Submitting links the
# request to the requester's open leave balance, approving deducts from it.
# ---------------------------------------------------------------------------

# close_open_balances OWNER: hana closes OWNER's open leave balances, left by
# earlier runs (a request finds its balance among the open ones only).
close_open_balances() {
	local b
	for b in $(ok hana item search --type leave_balance --state open --where "f_owner=$1" --page-size 100 | names); do
		ok hana item transition "$b" --to closed >/dev/null
	done
}

# new_balance OWNER ALLOWANCE: hana opens a balance for OWNER, readable by
# OWNER and hana only; prints its name.
new_balance() {
	ok hana item create --type leave_balance@1 --narrow "$1,user:hana" \
		--set "f_owner=$1" --set "f_allowance=$2" | j .item.name
}

# used BALANCE: its f_used, as hana sees it (unset is 0).
used() { ok hana item get "$1" | j '.item.fields.f_used // 0'; }

# wait_used BALANCE WANT: waits up to 10 s for f_used to be WANT (a refund
# runs after the withdrawal has committed).
wait_used() {
	local i got
	for i in $(seq 1 100); do
		got=$(used "$1")
		[ "$got" = "$2" ] && return 0
		sleep 0.1
	done
	fail "f_used of $1 is $got after 10 s, want $2"
}

# reactions BALANCE REQUEST REACTION: how many events REACTION (deduct or
# refund) wrote on BALANCE for REQUEST.
reactions() {
	ok hana item history "$1" --page-size 100 | jqx -r --arg id "${2##*/}" --arg actor "reaction:vacation_request:$3" \
		'[.events[] | select(.actor == $actor and (.causation_id | startswith($id)))] | length'
}

step "vacation: hana closes earlier runs' balances and opens ana's (25 days); bob has none yet"
close_open_balances user:ana
close_open_balances user:bob
abal=$(new_balance user:ana 25)
expect "ana's f_used" "$(used "$abal")" "0"

new_leave() { # WHO [--set ...]...: a vacation request, five days from 2099-07-01; prints its name
	local who=$1
	shift
	ok "$who" item create --type vacation_request@3 --set f_start=2099-07-01 --set f_end=2099-07-05 \
		--set f_days=5 --set f_leave_type=annual --set "f_comment=$w" "$@" | j .item.name
}

# submit_leave WHO NAME: submits and waits for the HR check; prints the answer.
submit_leave() { ok "$1" item transition "$2" --to submitted --wait 5s; }

step "vacation: ana submits (--wait); the HR check names bob on the way into submitted (APPLIED)"
v1=$(new_leave ana)
expect "a new request's state" "$(ok ana item get "$v1" | j .item.state)" "draft"
began=$(now_ms)
sub=$(submit_leave ana "$v1")
echo "    the HR check answered in $(($(now_ms) - began)) ms"
expect "submit's outcome" "$(j .outcome <<<"$sub")" "TRANSITION_OUTCOME_APPLIED"
expect "state" "$(j .item.state <<<"$sub")" "submitted"
expect "the approver, from HR" "$(j .item.fields.f_approver <<<"$sub")" "user:bob"
expect "the balance, set by the kernel" \
	"$(j '[.item.links[]? | select(.link_type == "balance") | .item.name] | join(" ")' <<<"$sub")" "$abal"

step "vacation: only the requester submits; the dates and days must make sense (GUARD_FAILED)"
v0=$(new_leave ana)
refuse bob "PERMISSION_DENIED NOT_ALLOWED" item transition "$v0" --to submitted
refuse ana "FAILED_PRECONDITION GUARD_FAILED" item transition "$v0" --to submitted --set f_days=0
refuse ana "FAILED_PRECONDITION GUARD_FAILED" item transition "$v0" --to submitted \
	--set f_days=5 --set f_start=2000-01-03 --set f_end=2000-01-07

step "vacation: ana cannot name the approver herself (FIELD_NOT_WRITABLE)"
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item create --type vacation_request@3 \
	--set f_start=2099-07-01 --set f_end=2099-07-05 --set f_days=5 --set f_leave_type=annual --set f_approver=user:ana
expect "the refused field" "$(meta fields)" "f_approver"
v2=$(new_leave ana)
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$v2" --set f_approver=user:ana
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item transition "$v2" --to submitted --set f_approver=user:ana
expect "no hold opened" "$(ok ana item get "$v2" | j '.item.hold // "none"')" "none"

step "vacation: the submitted terms are frozen (FIELD_FROZEN); the comment is not"
refuse ana "FAILED_PRECONDITION FIELD_FROZEN" item update "$v1" --set f_days=20
expect "the frozen field" "$(meta fields)" "f_days"
expect "the state that freezes it" "$(meta state)" "submitted"
ok ana item update "$v1" --set "f_comment=$w, beach" >/dev/null

step "vacation: ana may not approve her own request (except: creator); erin is not allowed"
refuse ana "PERMISSION_DENIED NOT_ALLOWED" item transition "$v1" --to approved
refuse erin "PERMISSION_DENIED NOT_ALLOWED" item transition "$v1" --to approved

step "vacation: triage-bot acting for bob, with a namespace-wide session, is not allowed"
ok triage-bot agent start --for user:bob --scopes namespaces/acme --powers 'read,transition:*' >/dev/null
refuse triage-bot "PERMISSION_DENIED NOT_ALLOWED" item transition "$v1" --to approved
refuse triage-bot "PERMISSION_DENIED NOT_ALLOWED" item transition "$v1" --to rejected

step "vacation: bob approves (--wait: the deduction holds it); what was approved stays frozen"
dec=$(ok bob item transition "$v1" --to approved --wait 5s)
expect "approve's outcome" "$(j .outcome <<<"$dec")" "TRANSITION_OUTCOME_APPLIED"
expect "state" "$(j .item.state <<<"$dec")" "approved"
expect "category" "$(j .item.category <<<"$dec")" "STATE_CATEGORY_DONE"
refuse ana "FAILED_PRECONDITION FIELD_FROZEN" item update "$v1" --set f_days=20
expect "the state that freezes it" "$(meta state)" "approved"
expect "ana's f_used after the approval" "$(used "$abal")" "5"

step "vacation: bob rejects another request"
v3=$(new_leave ana)
expect "submit's outcome" "$(submit_leave ana "$v3" | j .outcome)" "TRANSITION_OUTCOME_APPLIED"
dec=$(ok bob item transition "$v3" --to rejected)
expect "state" "$(j .item.state <<<"$dec")" "rejected"
expect "category" "$(j .item.category <<<"$dec")" "STATE_CATEGORY_CANCELED"

step "vacation: recall, edit, resubmit; withdraw"
v4=$(new_leave ana)
submit_leave ana "$v4" >/dev/null
refuse bob "PERMISSION_DENIED NOT_ALLOWED" item transition "$v4" --to draft
ok ana item transition "$v4" --to draft >/dev/null
ok ana item update "$v4" --set f_days=3 --set f_end=2099-07-03 >/dev/null
sub=$(submit_leave ana "$v4")
expect "resubmit's outcome" "$(j .outcome <<<"$sub")" "TRANSITION_OUTCOME_APPLIED"
expect "the new days" "$(j .item.fields.f_days <<<"$sub")" "3"
expect "the approver, from HR again" "$(j .item.fields.f_approver <<<"$sub")" "user:bob"
dec=$(ok ana item transition "$v4" --to withdrawn)
expect "state" "$(j .item.state <<<"$dec")" "withdrawn"

step "vacation: dana manages herself; HR answers no_eligible_approver (DENIED)"
v5=$(new_leave dana)
sub=$(submit_leave dana "$v5")
expect "outcome" "$(j .outcome <<<"$sub")" "TRANSITION_OUTCOME_DENIED"
expect "reason" "$(j .reason <<<"$sub")" "NO_ELIGIBLE_APPROVER"
expect "state" "$(j .item.state <<<"$sub")" "draft"

step "vacation: erin is not in the HR directory; HR answers unknown_employee (DENIED)"
v6=$(new_leave erin)
sub=$(submit_leave erin "$v6")
expect "outcome" "$(j .outcome <<<"$sub")" "TRANSITION_OUTCOME_DENIED"
expect "reason" "$(j .reason <<<"$sub")" "UNKNOWN_EMPLOYEE"

# ---------------------------------------------------------------------------
# Step 2, part 2: the leave balance. Only HR (hana) writes who owns a
# balance and its allowance; only the kernel writes f_used, through two
# link reactions of the request: deduct holds the approval, refund gives
# the days back. ana's balance has 25 days; 5 are used by now.
# ---------------------------------------------------------------------------

step "leave: bob has no open balance; his submit is DENIED no_balance, then HR opens one"
v10=$(new_leave bob)
sub=$(submit_leave bob "$v10")
expect "outcome" "$(j .outcome <<<"$sub")" "TRANSITION_OUTCOME_DENIED"
expect "reason" "$(j .reason <<<"$sub")" "NO_BALANCE"
expect "state" "$(j .item.state <<<"$sub")" "draft"
bbal=$(new_balance user:bob 10)
expect "resubmit's outcome" "$(submit_leave bob "$v10" | j .outcome)" "TRANSITION_OUTCOME_APPLIED"
dec=$(ok dana item transition "$v10" --to approved --wait 5s)
expect "dana approves bob's request" "$(j .outcome <<<"$dec")" "TRANSITION_OUTCOME_APPLIED"
expect "bob's f_used" "$(used "$bbal")" "5"
expect "ana's f_used is untouched" "$(used "$abal")" "5"

step "leave: an approved request deducts its days"
v7=$(new_leave ana)
submit_leave ana "$v7" >/dev/null
began=$(now_ms)
dec=$(ok bob item transition "$v7" --to approved --wait 5s)
echo "    the held approval answered in $(($(now_ms) - began)) ms"
expect "outcome" "$(j .outcome <<<"$dec")" "TRANSITION_OUTCOME_APPLIED"
expect "ana's f_used" "$(used "$abal")" "10"
expect "ana sees her own balance" "$(ok ana item get "$abal" | j .item.fields.f_used)" "10"

step "leave: a request beyond the balance is DENIED insufficient_balance"
v8=$(new_leave ana --set f_days=16)
submit_leave ana "$v8" >/dev/null
dec=$(ok bob item transition "$v8" --to approved --wait 5s)
expect "outcome" "$(j .outcome <<<"$dec")" "TRANSITION_OUTCOME_DENIED"
expect "reason" "$(j .reason <<<"$dec")" "INSUFFICIENT_BALANCE"
expect "state" "$(j .item.state <<<"$dec")" "submitted"
expect "ana's f_used" "$(used "$abal")" "10"

step "leave: withdrawing an approved request refunds it"
ok ana item transition "$v7" --to withdrawn >/dev/null
wait_used "$abal" 5
expect "refunds for it" "$(reactions "$abal" "$v7" refund)" "1"

step "leave: withdrawn during the approval hold, nothing extra is refunded (whichever wins)"
v9=$(new_leave ana)
submit_leave ana "$v9" >/dev/null
ok bob item transition "$v9" --to approved --wait 0s >/dev/null
ok ana item transition "$v9" --to withdrawn >/dev/null
wait_used "$abal" 5
deducted=$(reactions "$abal" "$v9" deduct)
echo "    the deduction ran $deducted time(s) before the withdrawal won"
expect "refunds for it, one per deduction" "$(reactions "$abal" "$v9" refund)" "$deducted"

step "leave: the requester cannot link or unlink a balance (LINK_NOT_WRITABLE)"
v11=$(new_leave ana)
refuse ana "PERMISSION_DENIED LINK_NOT_WRITABLE" item link "$v11" balance "$abal"
expect "the refused link type" "$(meta link_type)" "balance"
refuse ana "PERMISSION_DENIED LINK_NOT_WRITABLE" item unlink "$v1" balance "$abal"

step "leave: nobody edits f_used, hana included; only hana owns and allots (FIELD_NOT_WRITABLE)"
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$abal" --set f_used=0
refuse hana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$abal" --set f_used=0
expect "the refused field" "$(meta fields)" "f_used"
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$abal" --set f_allowance=99
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item create --type leave_balance@1 \
	--set f_owner=user:ana --set f_allowance=99

step "leave: a balance without an owner and an allowance cannot exist (INVARIANT_FAILED)"
refuse ana "FAILED_PRECONDITION INVARIANT_FAILED" item create --type leave_balance@1
expect "the invariant" "$(meta invariant)" "balance_incomplete"
refuse hana "FAILED_PRECONDITION INVARIANT_FAILED" item update "$abal" --set f_allowance=4
expect "the invariant" "$(meta invariant)" "insufficient_balance"

step "leave: two approvals race on 20 days left, 15 days each; one applies, one is DENIED"
r1=$(new_leave ana --set f_days=15)
r2=$(new_leave ana --set f_days=15)
submit_leave ana "$r1" >/dev/null
submit_leave ana "$r2" >/dev/null
"$pli_bin" --server "$server" --tokens "$tokens" --as bob item transition "$r1" --to approved --wait 5s >"$tmp/race1" 2>&1 &
p1=$!
"$pli_bin" --server "$server" --tokens "$tokens" --as bob item transition "$r2" --to approved --wait 5s >"$tmp/race2" 2>&1 &
p2=$!
wait "$p1" || fail "the first racing approval failed: $(cat "$tmp/race1")"
wait "$p2" || fail "the second racing approval failed: $(cat "$tmp/race2")"
expect "the two outcomes" "$(cat "$tmp/race1" "$tmp/race2" | jqx -rs '[.[].outcome] | sort | join(" ")')" \
	"TRANSITION_OUTCOME_APPLIED TRANSITION_OUTCOME_DENIED"
expect "the denial's reason" \
	"$(cat "$tmp/race1" "$tmp/race2" | jqx -rs '.[] | select(.outcome == "TRANSITION_OUTCOME_DENIED") | .reason')" \
	"INSUFFICIENT_BALANCE"
expect "ana's f_used, within the allowance" "$(used "$abal")" "20"

# ---------------------------------------------------------------------------
# Step 2, part 3: time and mail. The stack's worker runs on a dev clock (a
# day lasts 2 s; docker-compose.yml, never production): a submitted request
# reminds its approver at 2 and 3 days (4 s, 6 s) and expires at 5 days
# (10 s). Emails go to the stub, whose read-only port the dev compose file
# publishes ($mail); HOOK_RAN events in pli's history are the primary
# evidence. Every email is checked exactly once, and the absent ones absent.
# ---------------------------------------------------------------------------

mail=${PLI_MAIL:-http://localhost:8085}

# sent ITEM TEMPLATE: who the email stub sent TEMPLATE to for ITEM, one
# principal per message, sorted, on one line ("" for none).
sent() {
	curl -fsS "$mail/messages?item=${1##*/}" | jqx -r --arg t "$2" \
		'[.messages[]? | select(.template == $t) | .to] | sort | join(" ")'
}

# ran ITEM HOOK: how many HOOK_RAN events of HOOK ITEM's history has.
ran() {
	ok ana item history "$1" --page-size 100 | jqx -r --arg h "$2" \
		'[.events[] | select(.kind == "ITEM_EVENT_KIND_HOOK_RAN" and .hook == $h)] | length'
}

# kinds ITEM: the history kinds of ITEM, for the kinds check at the end.
kinds_seen=" "
kinds() {
	kinds_seen="$kinds_seen$(ok ana item history "$1" --page-size 100 | jqx -r '[.events[].kind] | unique | join(" ")') "
}

# wait_until SECONDS WHAT CMD...: polls CMD (every 0.25 s) until it
# succeeds, failing after SECONDS.
wait_until() {
	local limit=$1 what=$2 i
	shift 2
	for i in $(seq 1 $((limit * 4))); do
		if "$@" >/dev/null 2>&1; then return 0; fi
		sleep 0.25
	done
	fail "$what: not within $limit s"
}

state_is() { [ "$(ok "$1" item get "$2" | j .item.state)" = "$3" ]; }
sent_is() { [ "$(sent "$1" "$2")" = "$3" ]; }

step "mail: hana gives ana 15 more days (40); the stub is reachable"
ok hana item update "$abal" --set f_allowance=40 >/dev/null
curl -fsS "$mail/messages" >/dev/null || fail "the email stub's reader does not answer at $mail"

step "mail: a submitted request reminds bob twice, then expires; ana is told; no refund email"
v20=$(new_leave ana)
submit_leave ana "$v20" >/dev/null
began=$(now_ms)
wait_until 25 "the request expired" state_is ana "$v20" expired
echo "    expired after $(($(now_ms) - began)) ms (5 days of 2 s)"
wait_until 10 "the expiry email" sent_is "$v20" notify_expired user:ana
expect "reminders, to bob, twice" "$(sent "$v20" remind_approver)" "user:bob user:bob"
expect "the expiry email, to ana, once" "$(sent "$v20" notify_expired)" "user:ana"
expect "no approval email" "$(sent "$v20" notify_approved)" ""
expect "no refund email (no deduction can exist)" "$(sent "$v20" refund_email)" ""
expect "HOOK_RAN reminders in the history" "$(ran "$v20" remind_approver)" "2"
expect "HOOK_RAN expiry email in the history" "$(ran "$v20" notify_expired)" "1"
expect "f_used unchanged" "$(used "$abal")" "20"
kinds "$v20"

step "mail: bob approves (the deduction holds it); ana is emailed"
v21=$(new_leave ana)
submit_leave ana "$v21" >/dev/null
began=$(now_ms)
dec=$(ok bob item transition "$v21" --to approved --wait 5s)
echo "    the held approval answered in $(($(now_ms) - began)) ms"
expect "outcome" "$(j .outcome <<<"$dec")" "TRANSITION_OUTCOME_APPLIED"
expect "f_used" "$(used "$abal")" "25"
wait_until 10 "the approval email" sent_is "$v21" notify_approved user:ana

step "mail: ana withdraws; the refund commits, and she is emailed once"
ok ana item transition "$v21" --to withdrawn >/dev/null
wait_used "$abal" 20
wait_until 10 "the refund email" sent_is "$v21" refund_email user:ana
kinds "$v21"

step "mail: an approval beyond the balance is DENIED; no refund email"
v22=$(new_leave ana --set f_days=30)
submit_leave ana "$v22" >/dev/null
dec=$(ok bob item transition "$v22" --to approved --wait 5s)
expect "outcome" "$(j .outcome <<<"$dec")" "TRANSITION_OUTCOME_DENIED"
expect "reason" "$(j .reason <<<"$dec")" "INSUFFICIENT_BALANCE"
ok ana item transition "$v22" --to withdrawn >/dev/null # before it expires: this check is about the denial

step "mail: a timer deferred by a hold (a ticket, its ERP check slow: 3 s)"
# ticket@5's in_progress timer (24 h: 2 s here) comes due while the ERP
# check holds the approval (cust-slow answers after 3 s, and denies: the
# amount is over its credit limit). The timer waits for the hold to close,
# then looks again: still in_progress after the denial, so it fires at once
# (in_progress -> open). A spent timer would have left it in_progress.
t9=$(new_ticket ana --set f_customer_id=cust-slow --set f_amount=5000 --set f_assignee=user:bob)
ok bob item transition "$t9" --to open >/dev/null
ok bob item transition "$t9" --to in_progress >/dev/null
pending=$(ok bob item transition "$t9" --to approved --wait 0s)
expect "the approval is held" "$(j .outcome <<<"$pending")" "TRANSITION_OUTCOME_PENDING"
wait_until 15 "the deferred timer moved t9 back to open" state_is bob "$t9" open
order=$(ok bob item history "$t9" --page-size 100 | jqx -r \
	'[.events[] | select(.kind == "ITEM_EVENT_KIND_TRANSITION_DENIED" or
	  (.kind == "ITEM_EVENT_KIND_TRANSITIONED" and .to_state == "open" and .actor == "system:timer")) | .kind] | join(" ")')
expect "the denial, then the timer's move" "$order" "ITEM_EVENT_KIND_TRANSITION_DENIED ITEM_EVENT_KIND_TRANSITIONED"

step "mail: every email exactly once, the absent ones absent (after two more dev days)"
sleep 4
expect "v20 reminders" "$(sent "$v20" remind_approver)" "user:bob user:bob"
expect "v20 expiry" "$(sent "$v20" notify_expired)" "user:ana"
expect "v20 refund email" "$(sent "$v20" refund_email)" ""
expect "v21 approval" "$(sent "$v21" notify_approved)" "user:ana"
expect "v21 refund email" "$(sent "$v21" refund_email)" "user:ana"
expect "v21 expiry (approved: none)" "$(sent "$v21" notify_expired)" ""
expect "v22 refund email (denied: none)" "$(sent "$v22" refund_email)" ""
expect "v22 approval email (denied: none)" "$(sent "$v22" notify_approved)" ""
for kind in ITEM_EVENT_KIND_HOOK_RAN ITEM_EVENT_KIND_REACTION_APPLIED; do
	case $kinds_seen in *" $kind "* | *" $kind"*) ;; *) fail "no history showed $kind" ;; esac
done

# ---------------------------------------------------------------------------
# Another namespace: carol, in globex, reaches nothing of acme, on every
# operation that can name acme; and acme reaches nothing of hers.
# ---------------------------------------------------------------------------

step "cross-namespace: carol (globex) on every operation that names acme"
refuse carol "NOT_FOUND NOT_FOUND" item get "$t1"
refuse carol "NOT_FOUND NOT_FOUND" item update "$t1" --set f_priority=9
refuse carol "NOT_FOUND NOT_FOUND" item transition "$t1" --to resolved --set f_resolution=fixed
refuse carol "NOT_FOUND NOT_FOUND" item list "$board"
refuse carol "NOT_FOUND NOT_FOUND" item link "$t1" blocks "$t3"
refuse carol "NOT_FOUND NOT_FOUND" item unlink "$x" blocks "$y"
refuse carol "NOT_FOUND NOT_FOUND" item search --linked "in:blocked_by=$x"
refuse carol "NOT_FOUND NOT_FOUND" item history "$t1"
refuse carol "NOT_FOUND NOT_FOUND" item search --type namespaces/acme/types/ticket
refuse carol "NOT_FOUND NOT_FOUND" item create --type namespaces/acme/types/ticket/versions/5
refuse carol "NOT_FOUND NOT_FOUND" item create --type ticket@5 --in "$board"
refuse carol "NOT_FOUND NOT_FOUND" type get namespaces/acme/types/ticket/versions/5
refuse carol "NOT_FOUND NOT_FOUND" agent approve "$session" "$room" --yes
# ListTypes names no namespace: it lists the caller's own.

step "cross-namespace: acme on carol's Item"
theirs=$(ok carol item create --type ticket@5 --set "f_title=globex $w" | j .item.name)
case $theirs in namespaces/globex/items/*) ;; *) fail "carol's Item is $theirs, want it in globex" ;; esac
refuse ana "NOT_FOUND NOT_FOUND" item get "$theirs"
refuse triage-bot "NOT_FOUND NOT_FOUND" agent start --for user:ana --scopes "$theirs" --powers read

# ---------------------------------------------------------------------------
# Every reason of the error table a client can cause came back once.
# ---------------------------------------------------------------------------

step "every reachable refusal was seen"
for reason in UNAUTHENTICATED SESSION_INVALID INVALID_REQUEST NOT_FOUND NOT_ALLOWED NEEDS_APPROVAL \
	NO_TRANSITION MISSING_FIELDS GUARD_FAILED WIDEN_NOT_CONFIRMED STALE HELD \
	IDEMPOTENCY_KEY_REUSED NOT_CAUGHT_UP AUDIENCE_CHANGED \
	LINK_CARDINALITY ALREADY_LINKED NOT_LINKED LINK_TARGET_TYPE \
	FIELD_NOT_WRITABLE FIELD_FROZEN INVARIANT_FAILED LINK_NOT_WRITABLE; do
	case $seen in *" $reason "*) ;; *) fail "no step triggered $reason" ;; esac
done
cat <<'EOF'
    Not triggered here (a client cannot cause them; Go tests cover each):
      NOT_INITIAL_STATE  a create cannot name a state through the API
      HOP_LIMIT          only chains of hook writes reach it
      LINK_UNRESOLVED    only a direct transition into a resolve state; the
                         vacation request enters submitted through a hold, so
                         a missing balance is DENIED no_balance (above)
      INTERNAL           a bug, by definition
      CANCELED           decided by the client, which then gets no answer
      DEADLINE_EXCEEDED  likewise; pli sets no deadline
EOF

check_failed
echo
echo "scenario passed (run word $w)"
