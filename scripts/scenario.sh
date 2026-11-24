#!/usr/bin/env bash
# The proof of concept's scenario (Steps 0 to 2): a scripted session against
# the public API, through pli only, as users (ana and bob; dana and erin for
# the vacation request; hana, HR, for leave balances), an agent (triage-bot, for ana and for bob) and a
# user in another namespace (carol). It is the proof of concept's end-to-end
# test. Step 1 added links (blocks / blocked_by, duplicate_of) and the
# principal field f_assignee, on ticket v5. Step 2, part 1 added the vacation
# request: an approval as a business state, with the HR check. Step 2, part
# 2 added the leave balance: the held deduction, its refund, the race. Part 3
# added time and mail (reminders, expiry, the emails), on the stack's dev
# clock (vacation_request only); it needs curl. Jira part 1 added the PLAT
# software project (scenarios A and D of docs/design.md): lea leads, sam
# works, ana watches; order_by, rank through pli, the epic's progress,
# resolution on done, watching as a Subscription. Jira part 2, step 1 added
# date-time fields, sprints that close themselves at their end instant (and
# re-arm when it moves) and a story's resolution time, on version 2 of the
# PLAT Types. Jira part 2, step 2 added multi-value fields (labels and
# reviewers on version 3 of the PLAT issues) and reactions that move or
# create (ticket@6: a customer's comment sets a waiting ticket back in
# progress; starting work creates a checklist). Jira part 2, step 3b added
# scenario B, the IT service desk: incidents in a desk, with first-response
# and resolution goals (the `sla` package), breach marks and emails, the
# queue by due instant; lea holds the role sla_admin. Jira part 2, step 4a
# added scenario C1, customers: kim and lee open incidents in a portal and
# see their own and the replies on them, nothing else; sam opens one for kim
# on the phone; portal-bot acts for each of them. Ventoux V2b added pli
# type validate: draft Types checked against the registered ones. Ventoux
# V1 added action contracts: pli action list, and hooks checked against
# their actions' types and kinds.
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
refuse ana "ABORTED STALE" item link "$x" duplicate_of "$y" --etag c1

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
# Jira proof of concept, part 1: the PLAT software project (docs/design.md,
# "Jira proof of concept", scenarios A and D). lea leads, sam works, ana
# watches. Stories, bugs and tasks are contained in PLAT; they belong to an
# epic and a sprint by links. Rank is an app field (f_rank) that pli writes
# with ordinary updates; the kernel only sorts by it. There are no issue
# keys yet: "PLAT-2" below is the variable $plat2.
# ---------------------------------------------------------------------------

issue() { # WHO TYPE SUMMARY [--set ...]...: an issue in PLAT, tagged with this run's word; prints its name
	local who=$1 typ=$2 summary=$3
	shift 3
	ok "$who" item create --type "$typ@1" --in "$plat" --set "f_summary=$summary $w" "$@" | j .item.name
}
etag_of() { ok lea item get "$1" | j .item.etag; }
# in_order: the Item names of a search or list answer, in the order given.
in_order() { jqx -r '[.items[]?.name] | join(" ")'; }
# progress WHO: the epic's progress as WHO sees it, as "active=1 todo=2".
progress() {
	ok "$1" item get "$plat1" |
		j '.item.rollups.progress.counts // {} | to_entries | map("\(.key)=\(.value)") | sort | join(" ")'
}
board() { ok lea item search --linked "out:sprint=$sprint" --order-by f_rank | in_order; }

step "A1: lea creates the PLAT space and the epic Single sign-on (PLAT-1)"
plat=$(ok lea item create --type space@1 --set f_key=PLAT --set "f_name=Platform $w" | j .item.name)
plat1=$(ok lea item create --type epic@1 --in "$plat" --set "f_summary=Single sign-on $w" --set f_owner=user:lea | j .item.name)
expect "the epic's progress, nothing linked yet" "$(progress lea)" ""

step "A2: lea creates two stories and a bug in PLAT, each linked to PLAT-1 by epic, ranked in turn"
plat2=$(issue lea story "Log in with SSO" --set f_priority=P2 --set f_points=5)
plat3=$(issue lea story "Log out everywhere" --set f_priority=P1 --set f_points=3)
plat4=$(issue lea bug "SSO button is misaligned" --set f_priority=P3)
for i in "$plat2" "$plat3" "$plat4"; do
	ok lea item link "$i" epic "$plat1" >/dev/null
	ok lea rank "$i" bottom >/dev/null
done
expect "PLAT-2's container" "$(ok lea item get "$plat2" | j .item.container)" "$plat"
expect "the backlog by rank" \
	"$(ok lea item list "$plat" --type story --type bug --type task --order-by f_rank | in_order)" "$plat2 $plat3 $plat4"
expect "the epic's progress" "$(progress lea)" "todo=3"

step "A3: lea creates Sprint 12 (ends 2026-12-04), puts the three in it, and assigns PLAT-2 to sam"
sprint=$(ok lea item create --type sprint@1 --in "$plat" --set "f_name=Sprint 12 $w" \
	--set f_start=2026-11-23 --set f_end=2026-12-04 --set "f_goal=Ship SSO" | j .item.name)
expect "a new sprint's state" "$(ok lea item get "$sprint" | j .item.state)" "planned"
for i in "$plat2" "$plat3" "$plat4"; do ok lea item link "$i" sprint "$sprint" >/dev/null; done
ok lea item update "$plat2" --set f_assignee=user:sam >/dev/null
ok lea item update "$plat3" --set f_assignee=user:sam >/dev/null
ok lea item update "$plat4" --set f_assignee=user:lea >/dev/null
# A story is in at most one sprint.
other_sprint=$(ok lea item create --type sprint@1 --in "$plat" --set "f_name=Sprint 13 $w" --set f_end=2026-12-18 | j .item.name)
refuse lea "FAILED_PRECONDITION LINK_CARDINALITY" item link "$plat2" sprint "$other_sprint"
expect "sprints of this run ending in early December" \
	"$(ok lea item search "$w" --type sprint --where 'f_end>=2026-12-01' --where 'f_end<2026-12-10' | names)" "$sprint"
expect "sprints of this run, latest end first" \
	"$(ok lea item search "$w" --type sprint --order-by 'f_end desc' | in_order)" "$other_sprint $sprint"

step "A4: sam links PLAT-2 blocks PLAT-3, starts PLAT-2, and comments on it"
ok sam item link "$plat2" blocks "$plat3" >/dev/null
expect "PLAT-2's state" "$(ok sam item transition "$plat2" --to in_progress | j .item.state)" "in_progress"
comment=$(ok sam item create --type comment@1 --in "$plat2" --set "f_body=Waiting for the IdP metadata $w" | j .item.name)
expect "PLAT-2's comments" "$(ok lea item list "$plat2" | names)" "$comment"
expect "the comment's author" "$(ok lea item get "$comment" | j .item.creator)" "user:sam"

step "A5: lea opens the board: the Items linked to Sprint 12, by rank (a search, not a container listing)"
began=$(now_ms)
expect "the board" "$(board)" "$plat2 $plat3 $plat4"
echo "    the board answered in $(($(now_ms) - began)) ms"
expect "the sprint as a container" "$(ok lea item list "$sprint" | names)" ""
expect "the board, last first" \
	"$(ok lea item search --linked "out:sprint=$sprint" --order-by 'f_rank desc' | in_order)" "$plat4 $plat3 $plat2"

step "A6: lea ranks PLAT-4 before PLAT-2; the board order changes, with one write"
e2=$(etag_of "$plat2")
e3=$(etag_of "$plat3")
e4=$(etag_of "$plat4")
began=$(now_ms)
ok lea rank "$plat4" before "$plat2" >/dev/null
echo "    the move took $(($(now_ms) - began)) ms"
[ ! -s "$tmp/err" ] || fail "pli rank wrote to stderr: $(cat "$tmp/err")"
expect "the board after the move" "$(board)" "$plat4 $plat2 $plat3"
expect "PLAT-2 untouched by the move" "$(etag_of "$plat2")" "$e2"
expect "PLAT-3 untouched by the move" "$(etag_of "$plat3")" "$e3"
[ "$(etag_of "$plat4")" != "$e4" ] || fail "PLAT-4's etag did not change: the move wrote nothing"
ok lea rank "$plat3" after "$plat4" >/dev/null
expect "the board after a second move" "$(board)" "$plat4 $plat3 $plat2"
# The order is the backlog's own: the same in PLAT's listing.
expect "the backlog by rank" \
	"$(ok lea item list "$plat" --type story --type bug --type task --order-by f_rank | in_order)" "$plat4 $plat3 $plat2"
# A rebalance is a separate, deliberate command; it keeps the order.
# (How many cards it rewrites is not asserted: a card's random key may
# already be the one the rebalance would give it.)
rebalanced=$(ok lea rank rebalance --in "$plat")
expect "cards the rebalance skipped" "$(j .skipped <<<"$rebalanced")" "0"
expect "the board after the rebalance" "$(board)" "$plat4 $plat3 $plat2"
expect "the backlog after the rebalance" \
	"$(ok lea item list "$plat" --type story --type bug --type task --order-by f_rank | in_order)" "$plat4 $plat3 $plat2"
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item search --linked "out:sprint=$sprint" --order-by 'f_rank sideways'

step "A7: sam lists his open work: assignee = me, todo and active, by priority"
mine() { ok sam item search "$w" --type story --type bug --type task --where f_assignee=me \
	--category todo --category active --order-by f_priority | in_order; }
expect "sam's open work (P1 then P2)" "$(mine)" "$plat3 $plat2"
# The same field means something else on a ticket (an int): with no Type
# named, the declarations disagree and the sort is refused.
refuse sam "INVALID_ARGUMENT INVALID_REQUEST" item search "$w" --where f_assignee=me --order-by f_priority

step "A8: lea reads PLAT-2's history"
history=$(ok lea item history "$plat2")
expect "PLAT-2's history" "$(j '[.events[:7][].kind | sub("ITEM_EVENT_KIND_"; "")] | join(" ")' <<<"$history")" \
	"CREATED LINK_ADDED UPDATED LINK_ADDED UPDATED LINK_ADDED TRANSITIONED"
expect "who started it" "$(j '.events[6].actor' <<<"$history")" "user:sam"
# Ranking is an ordinary update: the first key (A2), and the rebalance (A6)
# if it gave PLAT-2 another key. Nothing else has happened to it since.
expect "what the first rank write set" "$(j '.events[2].fields | keys | join(" ")' <<<"$history")" "f_rank"
expect "events after sam started it that are not rank writes" \
	"$(j '[.events[7:][] | select(.kind != "ITEM_EVENT_KIND_UPDATED" or (.fields | keys) != ["f_rank"])] | length' <<<"$history")" "0"

step "A9: lea shows PLAT-1: its progress counts only what she can see"
hidden=$(ok sam item create --type task@1 --in "$plat" --narrow user:sam --set "f_summary=Rotate the signing key $w" | j .item.name)
ok sam item link "$hidden" epic "$plat1" >/dev/null
refuse lea "NOT_FOUND NOT_FOUND" item get "$hidden"
expect "the epic's progress as lea" "$(progress lea)" "active=1 todo=2"
expect "the epic's progress as sam, who sees one more" "$(progress sam)" "active=1 todo=3"
expect "the epic's links as lea do not show it" \
	"$(ok lea item get "$plat1" | j '[.item.links[]? | select(.link_type == "issues")] | length')" "3"
# The guard sees every issue, whoever asks: the epic does not close.
ok lea item transition "$plat1" --to in_progress >/dev/null
refuse lea "FAILED_PRECONDITION GUARD_FAILED" item transition "$plat1" --to done
expect "from_state" "$(meta from_state)" "in_progress"

step "A10: sam finishes PLAT-2: done needs a resolution; reopening clears it"
ok sam item transition "$plat2" --to in_review >/dev/null
refuse sam "FAILED_PRECONDITION MISSING_FIELDS" item transition "$plat2" --to done
expect "the missing field" "$(meta fields)" "f_resolution"
done2=$(ok sam item transition "$plat2" --to done --set f_resolution=fixed)
expect "PLAT-2's resolution" "$(j .item.fields.f_resolution <<<"$done2")" "fixed"
expect "the epic's progress" "$(progress lea)" "done=1 todo=2"
reopened=$(ok sam item transition "$plat2" --to todo)
expect "PLAT-2's resolution after reopening" "$(j '.item.fields.f_resolution // "cleared"' <<<"$reopened")" "cleared"
expect "history shows the clear on the reopening event" \
	"$(ok lea item history "$plat2" | j '.events[-1] | "\(.from_state)>\(.to_state) \(.fields | has("f_resolution")) \(.fields.f_resolution)"')" \
	"done>todo true null"
ok sam item transition "$plat2" --to in_progress >/dev/null
refuse sam "FAILED_PRECONDITION MISSING_FIELDS" item transition "$plat2" --to done

step "A11: lea closes the sprint by hand"
ok lea item transition "$sprint" --to active >/dev/null
closed=$(ok lea item transition "$sprint" --to closed)
expect "the sprint's state" "$(j .item.state <<<"$closed")" "closed"
expect "the sprint's category" "$(j .item.category <<<"$closed")" "STATE_CATEGORY_DONE"

# ---------------------------------------------------------------------------
# Jira part 2, step 1: date-time fields, a timer at a field's instant, and
# computed field effects, on version 2 of the PLAT Types (the steps above
# stay on version 1: Items keep the version they were created with).
#
# The stack's dev clock scales declared durations for vacation_request
# only; an instant is not a duration and is never scaled. So the sprints
# below end a few seconds from now, by the real clock, and the script waits
# for them: about 12 s in all.
# ---------------------------------------------------------------------------

# instant_in SECONDS: the instant that many seconds from now (negative:
# ago), in UTC, to the second. GNU date first, then BSD's.
instant_in() {
	local at=$(($(date +%s) + $1))
	date -u -d "@$at" +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -r "$at" +%Y-%m-%dT%H:%M:%SZ
}
# stored INSTANT: an instant to the second, as the API returns it.
stored() { printf '%s\n' "${1%Z}.000000Z"; }
# not_before A B: instant A is not before instant B. Both are RFC 3339 in
# UTC and B has its six fractional digits, so byte order is time order
# whatever fraction A is printed with (none, 3 or 6 digits).
not_before() { [ "$(printf '%s\n%s\n' "$1" "$2" | LC_ALL=C sort | head -n 1)" = "$2" ]; }
# closings ITEM: who closed it, one actor per closing event.
closings() {
	ok lea item history "$1" | j '[.events[] | select(.to_state == "closed") | .actor] | join(" ")'
}
sprint2() { # NAME [--set ...]...: a sprint@2 in PLAT, tagged with this run's word; prints its name
	local name=$1
	shift
	ok lea item create --type sprint@2 --in "$plat" --set "f_name=$name $w" "$@" | j .item.name
}

step "J1: a date-time is sent with an offset and comes back in UTC; without one it is refused"
s14=$(sprint2 "Sprint 14" --set f_end=2026-12-18 --set f_end_at=2026-12-18T18:00:00+01:00)
expect "Sprint 14's end, as stored" "$(ok lea item get "$s14" | j .item.fields.f_end_at)" "2026-12-18T17:00:00.000000Z"
expect "Sprint 14's end date is still a date" "$(ok lea item get "$s14" | j .item.fields.f_end)" "2026-12-18"
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item update "$s14" --set f_end_at=2026-12-18T18:00:00
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item update "$s14" --set f_end_at=2026-12-18
expect "sprint@2 says what its active state will do" \
	"$(ok lea type get sprint@2 | j '.type.states[] | select(.id == "active") | "\(.at.field) \(.at.to_state)"')" \
	"f_end_at closed"

step "J2: lea filters and orders sprints by their end instant"
s15=$(sprint2 "Sprint 15" --set f_end_at=2027-01-08T12:00:00-05:00) # 17:00Z
s16=$(sprint2 "Sprint 16")                                          # no end
expect "sprints of this run by end instant, those without one last" \
	"$(ok lea item search "$w" --type sprint --where 'f_end_at>=2026-01-01T00:00:00Z' --order-by f_end_at | in_order)" \
	"$s14 $s15"
expect "version 2 sprints of this run, latest end first" \
	"$(ok lea item search "$w" --type sprint --where "f_name>=Sprint 14" --where "f_name<Sprint 17" --order-by 'f_end_at desc' | in_order)" \
	"$s15 $s14 $s16"
expect "sprints ending on 18 December in Paris (a range with offset bounds)" \
	"$(ok lea item search "$w" --type sprint --where 'f_end_at>=2026-12-18T00:00:00+01:00' --where 'f_end_at<2026-12-19T00:00:00+01:00' | names)" \
	"$s14"
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item search "$w" --type sprint --where 'f_end_at>=2026-12-18'

step "J3: a sprint closes itself when its end instant passes"
end17=$(instant_in 5)
s17=$(sprint2 "Sprint 17" --set "f_end_at=$end17")
ok lea item transition "$s17" --to active >/dev/null
expect "Sprint 17, started, before its end" "$(ok lea item get "$s17" | j .item.state)" "active"
began=$(now_ms)
wait_until 30 "Sprint 17 closes itself" state_is lea "$s17" closed
echo "    Sprint 17 closed itself $(($(now_ms) - began)) ms after it was started (its end was at most 5 s away)"
expect "who closed Sprint 17" "$(closings "$s17")" "system:timer"
closed_at=$(ok lea item history "$s17" | j '.events[-1].create_time')
not_before "$closed_at" "$(stored "$end17")" || fail "Sprint 17 was closed at $closed_at, before its end $end17"

step "J4: moving a running sprint's end re-arms the timer"
end18=$(instant_in 4)
s18=$(sprint2 "Sprint 18" --set "f_end_at=$end18")
ok lea item transition "$s18" --to active >/dev/null
ok lea item update "$s18" --set "f_end_at=$(instant_in 3600)" >/dev/null
sleep 6 # past the first end
expect "Sprint 18, past its first end" "$(ok lea item get "$s18" | j .item.state)" "active"
expect "nobody closed Sprint 18" "$(closings "$s18")" ""
end18=$(instant_in 3)
ok lea item update "$s18" --set "f_end_at=$end18" >/dev/null
wait_until 30 "Sprint 18 closes at its new end" state_is lea "$s18" closed
expect "who closed Sprint 18, once" "$(closings "$s18")" "system:timer"
# A sprint whose end has already passed closes as soon as it starts.
s19=$(sprint2 "Sprint 19" --set "f_end_at=$(instant_in -3600)")
ok lea item transition "$s19" --to active >/dev/null
wait_until 30 "Sprint 19, already over, closes at once" state_is lea "$s19" closed
# A sprint without an end stays open; lea closes it by hand, as before.
ok lea item transition "$s16" --to active >/dev/null
expect "Sprint 16, no end" "$(ok lea item get "$s16" | j .item.state)" "active"
ok lea item transition "$s16" --to closed >/dev/null
expect "who closed Sprint 16" "$(closings "$s16")" "user:lea"

step "J5: done sets a story's resolution time from its own event; reopening clears it"
issue2() { # SUMMARY: a story@2 in PLAT; prints its name
	ok lea item create --type story@2 --in "$plat" --set "f_summary=$1 $w" | j .item.name
}
r1=$(issue2 "Resolved first")
r2=$(issue2 "Resolved second")
r3=$(issue2 "Never resolved")
expect "story@2 says what done computes" \
	"$(ok lea type get story@2 | j '[.type.transitions[] | select(.sets != null) | "\(.from_state)>\(.to_state)=\(.sets.f_resolved_at)"] | sort | join(" ")')" \
	"done>todo=null in_progress>done=cause.at in_review>done=cause.at"
for i in "$r1" "$r2"; do ok lea item transition "$i" --to in_progress >/dev/null; done
refuse lea "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$r1" --set f_resolved_at=2026-12-04T17:00:00Z
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item transition "$r1" --to done --set f_resolution=fixed --set f_resolved_at=2026-12-04T17:00:00Z
done1=$(ok lea item transition "$r1" --to done --set f_resolution=fixed)
resolved1=$(j .item.fields.f_resolved_at <<<"$done1")
# The event's time, in the form a datetime field holds (six fractional digits).
event_at=$(ok lea item history "$r1" | j '.events[-1].create_time | sub("Z$"; "") | split(".") | "\(.[0]).\(((.[1] // "") + "000000")[0:6])Z"')
expect "R1's resolution time is its done event's time" "$resolved1" "$event_at"
expect "the done event carries it" "$(ok lea item history "$r1" | j '.events[-1].fields.f_resolved_at')" "$resolved1"
ok lea item transition "$r2" --to done --set f_resolution=wontfix >/dev/null
expect "stories of this run by resolution time, latest first, unresolved last" \
	"$(ok lea item search "$w" --type story --where "f_summary>=Never" --where "f_summary<Resolved t" --order-by 'f_resolved_at desc' | in_order)" \
	"$r2 $r1 $r3"
expect "stories resolved since R1 was" \
	"$(ok lea item search "$w" --type story --where "f_resolved_at>$resolved1" | names)" "$r2"
reopened=$(ok lea item transition "$r1" --to todo)
expect "R1's resolution time after reopening" "$(j '.item.fields.f_resolved_at // "cleared"' <<<"$reopened")" "cleared"
expect "R1's resolution after reopening" "$(j '.item.fields.f_resolution // "cleared"' <<<"$reopened")" "cleared"
expect "history shows both cleared on the reopening event" \
	"$(ok lea item history "$r1" | j '.events[-1] | "\(.fields.f_resolved_at) \(.fields.f_resolution)"')" "null null"
expect "stories of this run still resolved" \
	"$(ok lea item search "$w" --type story --where 'f_resolved_at>=2026-01-01T00:00:00Z' | names)" "$r2"

# ---------------------------------------------------------------------------
# Jira part 2, step 2: multi-value fields, and reactions that move or
# create. Labels and reviewers are sets on version 3 of the PLAT issues.
# ticket@6 has a waiting_for_customer state: a public comment (comment@2)
# made inside the ticket by its creator sets it back in progress, a rule
# the TICKET declares (the comment Type knows nothing of it); and starting
# work on a ticket creates a checklist inside it. The worker runs the
# reactions a moment after the write that triggers them, so the script
# waits for each outcome (at most 30 s).
# ---------------------------------------------------------------------------

# set_of FIELD: a multi-value field of an Item answer, as "a b c" ("" if unset).
set_of() { j ".item.fields.$1 // [] | join(\" \")"; }
# records ITEM: the outcome records of the Item's reactions, in order, as
# "APPLIED ticket:reopen" or "SKIPPED ticket:reopen NOT_BY", joined by ", ".
records() {
	ok ana item history "$1" --page-size 100 | j '[.events[]
		| select(.kind | test("REACTION_(APPLIED|SKIPPED|FAILED)$"))
		| "\(.kind | sub("ITEM_EVENT_KIND_REACTION_"; "")) \(.reaction)\(if (.reason // "") != "" then " " + .reason else "" end)"]
		| join(", ")'
}
records_are() { [ "$(records "$1")" = "$2" ]; }
comment_in() { # WHO ITEM PUBLIC: a comment@2 inside ITEM; prints its name
	ok "$1" item create --type comment@2 --in "$2" --set "f_body=any news? $w" --set "f_public=$3" | j .item.name
}

step "K1: labels and reviewers are sets: a list, add and remove, sorted, no duplicates"
k1=$(ok lea item create --type story@3 --in "$plat" --set "f_summary=Labelled one $w" \
	--set 'f_labels=["sso","backend","sso"]' | j .item.name)
expect "K1's labels, sorted, once each" "$(ok lea item get "$k1" | set_of f_labels)" "backend sso"
changed=$(ok lea item update "$k1" --add f_labels=urgent --remove f_labels=sso --add f_reviewers=user:sam --add f_reviewers=user:lea)
expect "K1's labels after add and remove" "$(set_of f_labels <<<"$changed")" "backend urgent"
expect "K1's reviewers" "$(set_of f_reviewers <<<"$changed")" "user:lea user:sam"
expect "history shows what was asked" \
	"$(ok lea item history "$k1" | j '.events[-1].set_ops.f_labels | "\(.add | join(",")) \(.remove | join(","))"')" "urgent sso"
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item update "$k1" --set f_labels=backend
expect "story@3 says f_labels is a set" \
	"$(ok lea type get story@3 | j '.type.fields[] | select(.id == "f_labels") | "\(.multiple) \(.max)"')" "true 20"

step "K2: two people add a label at the same moment, with no etag: both land"
k2=$(ok lea item create --type story@3 --in "$plat" --set "f_summary=Labelled two $w" --set 'f_labels=["backend"]' | j .item.name)
"$pli_bin" --server "$server" --tokens "$tokens" --as lea item update "$k2" --add f_labels=from-lea >/dev/null 2>"$tmp/race1" &
race1=$!
"$pli_bin" --server "$server" --tokens "$tokens" --as sam item update "$k2" --add f_labels=from-sam >/dev/null 2>"$tmp/race2" &
race2=$!
wait "$race1" || fail "lea's --add failed: $(cat "$tmp/race1")"
wait "$race2" || fail "sam's --add failed: $(cat "$tmp/race2")"
expect "K2's labels after the race" "$(ok lea item get "$k2" | set_of f_labels)" "backend from-lea from-sam"

step "K3: contains and empty filters; a set has no order and no more than its max"
k3=$(ok lea item create --type story@3 --in "$plat" --set "f_summary=Labelled three $w" | j .item.name)
labelled() { ok lea item search "$w" --type story --where "f_summary>=Labelled" --where "f_summary<Labelled z" "$@" | names; }
expect "stories labelled backend" "$(labelled --where f_labels=backend)" "$(sorted "$k1" "$k2")"
expect "stories labelled backend and urgent" "$(labelled --where f_labels=backend --where f_labels=urgent)" "$k1"
expect "stories without a label" "$(labelled --empty f_labels)" "$k3"
expect "stories sam reviews" "$(ok sam item search "$w" --type story --where f_reviewers=me | names)" "$k1"
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item search "$w" --type story --order-by f_labels
many=$(jq -cn '[range(21) | "l\(.)"]')
refuse lea "FAILED_PRECONDITION TOO_MANY_VALUES" item update "$k3" --set "f_labels=$many"
expect "the field" "$(meta field)" "f_labels"
expect "its max" "$(meta max)" "20"

step "K4: starting work on a ticket@6 creates a checklist inside it"
k4=$(ok ana item create --type ticket@6 --set "f_title=Printer jams $w" --set f_assignee=user:bob \
	--set f_customer_id=cust-good --set f_amount=100 | j .item.name)
ok ana item transition "$k4" --to open >/dev/null
started=$(ok ana item transition "$k4" --to in_progress)
wait_until 30 "the checklist reaction recorded on K4" records_are "$k4" "APPLIED ticket:open_checklist"
checklist=$(ok ana item list "$k4" --type checklist | j '.items[0].name')
expect "one checklist in K4" "$(ok ana item list "$k4" --type checklist | j '.items | length')" "1"
expect "the checklist's creator: the reaction, no person" "$(ok ana item get "$checklist" | j .item.creator)" "reaction:ticket:open_checklist"
# Its time is the cause's: the event that started the work, not the moment
# the worker got to it.
entered_at=$(ok ana item history "$k4" --page-size 100 | j '[.events[] | select(.to_state == "in_progress")][0].create_time
	| sub("Z$"; "") | split(".") | "\(.[0]).\(((.[1] // "") + "000000")[0:6])Z"')
expect "the checklist's f_opened_at is K4's event time" "$(ok ana item get "$checklist" | j .item.fields.f_opened_at)" "$entered_at"
: "$started"

step "K5: K4 waits for its customer; only her own public comment sets it back in progress"
ok bob item transition "$k4" --to waiting_for_customer >/dev/null
refuse ana "PERMISSION_DENIED NOT_ALLOWED" item transition "$k4" --to in_progress # support only, by hand
comment_in bob "$k4" true >/dev/null
wait_until 30 "bob's comment recorded as skipped" records_are "$k4" "APPLIED ticket:open_checklist, SKIPPED ticket:reopen NOT_BY"
expect "K4 after someone else's comment" "$(ok ana item get "$k4" | j .item.state)" "waiting_for_customer"
comment_in ana "$k4" false >/dev/null
wait_until 30 "ana's private comment recorded as skipped" records_are "$k4" \
	"APPLIED ticket:open_checklist, SKIPPED ticket:reopen NOT_BY, SKIPPED ticket:reopen CONDITION_FALSE"
expect "K4 after a private comment" "$(ok ana item get "$k4" | j .item.state)" "waiting_for_customer"
mine=$(comment_in ana "$k4" true)
began=$(now_ms)
wait_until 30 "ana's public comment sets K4 back in progress" state_is ana "$k4" in_progress
echo "    K4 was back in progress $(($(now_ms) - began)) ms after ana's comment"
wait_until 30 "the move recorded on K4" records_are "$k4" \
	"APPLIED ticket:open_checklist, SKIPPED ticket:reopen NOT_BY, SKIPPED ticket:reopen CONDITION_FALSE, APPLIED ticket:reopen"
expect "who moved K4, and for whom" \
	"$(ok ana item history "$k4" --page-size 100 | j '[.events[] | select(.from_state == "waiting_for_customer")][0] | "\(.actor) \(.on_behalf_of)"')" \
	"reaction:ticket:reopen user:ana"
expect "the comment itself: one event" "$(ok ana item history "$mine" | j '.events | length')" "1"
expect "still one checklist: coming back is not starting" "$(ok ana item list "$k4" --type checklist | j '.items | length')" "1"

step "K6: a move the ticket refuses is recorded as REACTION_FAILED, and not retried"
k6=$(ok ana item create --type ticket@6 --set "f_title=No assignee $w" --set f_assignee=user:bob \
	--set f_customer_id=cust-good --set f_amount=100 | j .item.name)
ok ana item transition "$k6" --to open >/dev/null
ok ana item transition "$k6" --to in_progress >/dev/null
wait_until 30 "K6's checklist" records_are "$k6" "APPLIED ticket:open_checklist"
ok bob item transition "$k6" --to waiting_for_customer >/dev/null
ok bob item update "$k6" --set f_assignee=null >/dev/null # nobody to take it back
comment_in ana "$k6" true >/dev/null
wait_until 30 "the refused move recorded on K6" records_are "$k6" \
	"APPLIED ticket:open_checklist, FAILED ticket:reopen GUARD_FAILED"
expect "K6 after the refusal" "$(ok ana item get "$k6" | j .item.state)" "waiting_for_customer"
kinds_seen="$kinds_seen$(ok ana item history "$k6" --page-size 100 | jqx -r '[.events[].kind] | unique | join(" ")') "
kinds_seen="$kinds_seen$(ok ana item history "$k4" --page-size 100 | jqx -r '[.events[].kind] | unique | join(" ")') "
case $kinds_seen in *" ITEM_EVENT_KIND_REACTION_SKIPPED "* | *" ITEM_EVENT_KIND_REACTION_SKIPPED"*) ;; *) fail "no history showed REACTION_SKIPPED" ;; esac

step "K7: a comment inside a story, whose Type declares no such rule, costs the story nothing"
before=$(ok lea item history "$k3" | j '.events | length')
etag_before=$(ok lea item get "$k3" | j .item.etag)
quiet=$(comment_in lea "$k3" true)
sleep 2 # were anything to react, it would have by now
expect "the story's events, unchanged" "$(ok lea item history "$k3" | j '.events | length')" "$before"
expect "the story's etag, unchanged" "$(ok lea item get "$k3" | j .item.etag)" "$etag_before"
expect "the comment: its create, nothing else" "$(ok lea item history "$quiet" | j '.events | length')" "1"

# ---------------------------------------------------------------------------
# Jira part 2, step 3b: scenario B, the IT service desk (docs/design.md).
# The `sla` package (types/sla/: sla_goal@1, calendar@1, sla_policy@1) and
# its first host: incident@1 in a desk@1. lea runs the desk (role
# sla_admin), ana opens incidents, sam works on them.
#
# An incident starts two goals when it is created, first response and
# resolution, each an Item contained in it, with the target the desk's
# policy gives its priority. The goals are moved by the incident's own
# reactions and marked breached by a timer at their due instant; each shows
# its due instant on the incident (f_due_first_response, f_due_resolution),
# which is what the queue sorts by. Nothing here is a command of its own:
# pli creates, moves, lists, searches and reads histories.
#
# Time: the calendar is open around the clock in UTC, so working time is
# time; the policy's P1 targets are seconds (3 and 6), so a P1 breaches
# while the script watches, and its P3 targets are hours, so a P3 never
# does. The dev clock does not apply: an instant is not a duration.
# Working hours, holidays and the 120-hour close are in the Go tests.
# ---------------------------------------------------------------------------

# goals INCIDENT METRIC: the names of the incident's goals of a metric,
# oldest first by f_started_at.
goals() {
	ok lea item list "$1" --type sla_goal --page-size 100 |
		jqx -r --arg m "$2" '[.items[]? | select(.fields.f_metric == $m)] | sort_by(.fields.f_started_at) | map(.name) | join(" ")'
}
goal() { goals "$1" "$2" | awk '{print $NF}'; } # the current one: the newest
has_goals() { [ -n "$(goal "$1" first_response)" ] && [ -n "$(goal "$1" resolution)" ]; }
fld() { ok lea item get "$1" | j ".item.fields.$2 // \"\""; } # ITEM FIELD: its value, "" if unset
fld_is() { [ "$(fld "$1" "$2")" = "$3" ]; }
fld_set() { [ -n "$(fld "$1" "$2")" ]; }
incident() { # WHO PRIORITY TITLE [--set ...]...: an incident in the IT desk; prints its name
	local who=$1 prio=$2 title=$3
	shift 3
	ok "$who" item create --type incident@1 --in "$desk" --set "f_title=$title $w" --set "f_priority=$prio" "$@" | j .item.name
}
# targets ITEM: what the Item's reaction records name, in order, as
# "APPLIED incident:sla_stop <name|restricted|none>", joined by ", ".
targets() {
	ok "$1" item history "$2" --page-size 100 | j '[.events[]
		| select(.kind | test("REACTION_(APPLIED|SKIPPED|FAILED)$"))
		| "\(.kind | sub("ITEM_EVENT_KIND_REACTION_"; "")) \(.reaction) \(if .reaction_target == null then "none" elif .reaction_target.restricted then "restricted" else .reaction_target.name end)"]
		| join(", ")'
}
# The breach hook's ID, which is the email's template: read from the Type.
breach_hook=$(ok lea type get sla_goal@1 | j '[.type.states[] | select(.id == "running") | .acting[]?.hook][0] // ""')

step "B1: lea sets up the IT desk: a calendar, a policy with a target per priority, both linked by her alone"
[ -n "$breach_hook" ] || fail "sla_goal@1's running state has no acting timer with a hook"
cal=$(ok lea item create --type calendar@1 --set "f_name=Always $w" --set f_zone=UTC \
	--set 'f_week=["mon 00:00-24:00","tue 00:00-24:00","wed 00:00-24:00","thu 00:00-24:00","fri 00:00-24:00","sat 00:00-24:00","sun 00:00-24:00"]' | j .item.name)
policy=$(ok lea item create --type sla_policy@1 --set "f_name=Standard $w" \
	--set f_first_response_s_p1=3 --set f_resolution_s_p1=6 \
	--set f_first_response_s_p2=1800 --set f_resolution_s_p2=14400 \
	--set f_first_response_s_p3=3600 --set f_resolution_s_p3=28800 \
	--set f_first_response_s_p4=7200 --set f_resolution_s_p4=57600 | j .item.name)
desk=$(ok lea item create --type desk@1 --set f_key=IT --set "f_name=IT service desk $w" | j .item.name)
# Not sam's to choose, nor ana's: a requester never picks her own targets.
refuse sam "PERMISSION_DENIED LINK_NOT_WRITABLE" item link "$desk" calendar "$cal"
refuse ana "PERMISSION_DENIED LINK_NOT_WRITABLE" item link "$desk" sla_policy "$policy"
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$policy" --set f_resolution_s_p1=999999
refuse lea "FAILED_PRECONDITION INVARIANT_FAILED" item update "$policy" --set f_resolution_s_p1=0
ok lea item link "$desk" calendar "$cal" >/dev/null
ok lea item link "$desk" sla_policy "$policy" >/dev/null
refuse ana "PERMISSION_DENIED LINK_NOT_WRITABLE" item unlink "$desk" calendar "$cal"
expect "the desk's links" "$(ok ana item get "$desk" | j '[.item.links[].link_type] | sort | join(" ")')" "calendar sla_policy"
expect "what the incident offers its goals' hooks" \
	"$(ok ana type get incident@1 | j '[.type.offers[]? | "\(.name)=\(.field)"] | join(" ")')" "breach_contact=f_assignee"

step "B2: ana opens a P3 incident; its two goals start from its creation, with the policy's P3 targets"
refuse ana "FAILED_PRECONDITION INVARIANT_FAILED" item create --type incident@1 --in "$desk" --set "f_title=No priority $w"
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item create --type incident@1 --in "$desk" --set "f_title=Mine $w" \
	--set f_priority=P4 --set f_due_resolution=2099-01-01T00:00:00Z
# Only inside a desk: incident@4's start block (in: [desk]) judges every
# incident version (starting work, step 2).
refuse ana "PERMISSION_DENIED NOT_ALLOWED" item create --type incident@1 --set "f_title=Nowhere $w" --set f_priority=P3
b3=$(incident ana P3 "Laptop will not wake")
began=$(now_ms)
wait_until 30 "the incident's two goals" has_goals "$b3"
echo "    both goals existed $(($(now_ms) - began)) ms after the incident"
fr3=$(goal "$b3" first_response)
res3=$(goal "$b3" resolution)
expect "the incident starts in" "$(ok ana item get "$b3" | j .item.state)" "new"
expect "the goals' targets (P3)" "$(fld "$fr3" f_target_s) $(fld "$res3" f_target_s)" "3600 28800"
opened_at=$(ok ana item history "$b3" | j '.events[0].create_time | sub("Z$"; "") | split(".") | "\(.[0]).\(((.[1] // "") + "000000")[0:6])Z"')
expect "the goals started at the incident's own create time" "$(fld "$fr3" f_started_at) $(fld "$res3" f_started_at)" "$opened_at $opened_at"
expect "the goals are running" "$(ok ana item get "$fr3" | j .item.state) $(ok ana item get "$res3" | j .item.state)" "running running"
expect "a goal's creator: the incident's reaction, no person" "$(ok ana item get "$res3" | j .item.creator)" "reaction:incident:sla_start_resolution"
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$res3" --set f_target_s=999999
wait_until 30 "both due instants shown on the incident" fld_set "$b3" f_due_resolution
wait_until 30 "both due instants shown on the incident" fld_set "$b3" f_due_first_response
expect "the incident's f_due_resolution is its goal's f_due_at" "$(fld "$b3" f_due_resolution)" "$(fld "$res3" f_due_at)"
expect "the incident's f_due_first_response is its goal's f_due_at" "$(fld "$b3" f_due_first_response)" "$(fld "$fr3" f_due_at)"
expect "the start records name the goals they created" "$(targets ana "$b3")" \
	"APPLIED incident:sla_start_first_response $fr3, APPLIED incident:sla_start_resolution $res3"

step "B3: sam is assigned and answers in public: first response is met; resolution runs on"
ok ana item update "$b3" --set f_assignee=user:sam >/dev/null
ok sam item transition "$b3" --to in_progress >/dev/null
comment_in ana "$b3" true >/dev/null  # the requester's own words answer nothing
comment_in sam "$b3" false >/dev/null # nor does a private note
sleep 2
expect "first response after ana's comment and sam's private note" "$(ok ana item get "$fr3" | j .item.state)" "running"
comment_in sam "$b3" true >/dev/null
wait_until 30 "first response stopped by sam's public answer" state_is ana "$fr3" completed
expect "first response" "$(fld "$fr3" f_outcome)" "met"
wait_until 30 "f_due_first_response unset" fld_is "$b3" f_due_first_response ""
expect "resolution is still running, and shown" "$(ok ana item get "$res3" | j .item.state) $(fld "$b3" f_due_resolution)" \
	"running $(fld "$res3" f_due_at)"

step "B4: waiting for the customer pauses resolution; ana's public comment brings the incident back and resumes it"
due_before=$(fld "$res3" f_due_at)
ok sam item transition "$b3" --to waiting_for_customer >/dev/null
wait_until 30 "resolution paused" state_is ana "$res3" paused
wait_until 30 "f_due_resolution unset while waiting" fld_is "$b3" f_due_resolution ""
expect "a paused goal has no due instant" "$(fld "$res3" f_due_at)" ""
sleep 3 # three seconds the customer is asked to wait for nothing
comment_in sam "$b3" true >/dev/null # someone else's comment does not bring it back
sleep 2
expect "the incident after sam's comment" "$(ok ana item get "$b3" | j .item.state)" "waiting_for_customer"
comment_in ana "$b3" true >/dev/null
wait_until 30 "ana's public comment sets the incident back in progress" state_is ana "$b3" in_progress
expect "who moved it, and for whom" \
	"$(ok ana item history "$b3" --page-size 100 | j '[.events[] | select(.from_state == "waiting_for_customer")][0] | "\(.actor | sub(":[^:]*$"; "")) \(.on_behalf_of)"')" \
	"reaction:incident user:ana"
wait_until 30 "resolution running again" state_is ana "$res3" running
wait_until 30 "f_due_resolution shown again" fld_set "$b3" f_due_resolution
due_after=$(fld "$res3" f_due_at)
expect "the incident shows the new due instant" "$(fld "$b3" f_due_resolution)" "$due_after"
[ "$due_after" != "$due_before" ] && not_before "$due_after" "$due_before" ||
	fail "after a pause of five seconds the due instant went from $due_before to $due_after; want it later"
expect "the time worked before the pause is kept" "$([ "$(fld "$res3" f_elapsed_s)" -ge 0 ] && echo kept)" "kept"

step "B5: a P1 breaches: marked at its due instant, never before, still running; sam is emailed once per goal"
b1=$(incident ana P1 "VPN down for everyone" --set f_assignee=user:sam)
wait_until 30 "the P1's goals" has_goals "$b1"
fr1=$(goal "$b1" first_response)
res1=$(goal "$b1" resolution)
expect "the goals' targets (P1)" "$(fld "$fr1" f_target_s) $(fld "$res1" f_target_s)" "3 6"
due1=$(fld "$res1" f_due_at)
began=$(now_ms)
wait_until 30 "the resolution goal breached" fld_set "$res1" f_breached_at
echo "    the mark was visible $(($(now_ms) - began)) ms after the goal was read (its target is 6 s)"
expect "f_breached_at is the due instant itself" "$(fld "$res1" f_breached_at)" "$due1"
marked_at=$(ok lea item history "$res1" | j '[.events[] | select(.actor == "system:timer")][0].create_time')
not_before "$marked_at" "$due1" || fail "the breach was written at $marked_at, before the due instant $due1"
expect "breach is not the end" "$(ok ana item get "$res1" | j .item.state) $(fld "$res1" f_outcome)" "running "
expect "the incident goes on showing the missed instant" "$(fld "$b1" f_due_resolution)" "$due1"
wait_until 30 "sam emailed about the resolution breach" sent_is "$res1" "$breach_hook" user:sam
wait_until 30 "sam emailed about the first response breach" sent_is "$fr1" "$breach_hook" user:sam
expect "the first response goal's mark" "$(fld "$fr1" f_breached_at)" "$(fld "$fr1" f_due_at)"

step "B6: the breach of an incident nobody is assigned to tells nobody: one HOOK_FAILED NO_RECIPIENT, no email"
b0=$(incident ana P1 "Printer offline")
wait_until 30 "its goals" has_goals "$b0"
res0=$(goal "$b0" resolution)
no_recipient() {
	[ "$(ok lea item history "$res0" --page-size 100 |
		j '[.events[] | select(.kind == "ITEM_EVENT_KIND_HOOK_FAILED" and .reason == "NO_RECIPIENT")] | length')" = "$1" ]
}
wait_until 30 "the failed notification recorded" no_recipient 1
expect "the mark stands" "$(fld "$res0" f_breached_at)" "$(fld "$res0" f_due_at)"
ok ana item update "$b0" --set f_assignee=user:sam >/dev/null # assigned too late to be told
sleep 3
no_recipient 1 || fail "the notification was tried again"
expect "no email about it, to anyone" "$(sent "$res0" "$breach_hook")" ""
kinds_seen="$kinds_seen$(ok lea item history "$res0" --page-size 100 | jqx -r '[.events[].kind] | unique | join(" ")') "

step "B7: the queue: lea sorts open incidents by resolution due instant; the overdue and the ever-breached searches"
expect "this run's incidents with a resolution due, most urgent first" \
	"$(ok lea item search "$w" --type incident --not-empty f_due_resolution --order-by f_due_resolution | in_order)" \
	"$b1 $b0 $b3"
now=$(instant_in 0)
expect "overdue now: due before this instant" \
	"$(ok lea item search "$w" --type incident --where "f_due_resolution<$now" --order-by f_due_resolution | in_order)" "$b1 $b0"
breached=$(ok lea item search --type sla_goal --not-empty f_breached_at --order-by 'f_breached_at desc' --page-size 100 | j '[.items[]?.name] | join(" ")')
for g in "$fr1" "$res1" "$res0"; do
	case " $breached " in *" $g "*) ;; *) fail "the ever-breached search does not find $g" ;; esac
done
for g in "$fr3" "$res3"; do
	case " $breached " in *" $g "*) fail "the ever-breached search finds $g, which never breached" ;; esac
done

step "B8: sam links the bug that caused the P1 and resolves it, late: both goals missed. No rule runs over the link"
cause=$(ok sam item create --type bug@3 --in "$plat" --set "f_summary=VPN gateway leaks memory $w" | j .item.name)
ok sam item link "$b1" caused_by "$cause" >/dev/null
expect "the incident's link, and the bug's from its end" \
	"$(ok sam item get "$b1" | links_of) / $(ok sam item get "$cause" | links_of)" \
	"OUTGOING caused_by $cause / INCOMING causes $b1"
expect "the linked bug's state, read from the bug" "$(ok sam item get "$cause" | j .item.state)" "todo"
ok sam item transition "$b1" --to in_progress >/dev/null
refuse sam "FAILED_PRECONDITION MISSING_FIELDS" item transition "$b1" --to resolved # resolving says how
ok sam item transition "$b1" --to resolved --set f_resolution=fixed >/dev/null
refuse sam "PERMISSION_DENIED NOT_ALLOWED" item transition "$b1" --to closed # the timer's only, 120 hours on
wait_until 30 "the P1's resolution goal stopped" state_is ana "$res1" completed
wait_until 30 "the P1's first response goal stopped" state_is ana "$fr1" completed
expect "both goals, resolved after their instants" "$(fld "$fr1" f_outcome) $(fld "$res1" f_outcome)" "missed missed"
expect "the marks are kept" "$(fld "$res1" f_breached_at)" "$due1"
wait_until 30 "the resolved incident shows no due instant" fld_is "$b1" f_due_resolution ""
expect "one stop record per goal, each naming its goal" "$(targets ana "$b1" | tr ',' '\n' | grep -c 'APPLIED incident:sla_stop namespaces/')" "2"
# Fixing the bug is the bug's business: the incident hears nothing of it.
before=$(ok sam item history "$b1" --page-size 100 | j '.events | length')
ok sam item transition "$cause" --to in_progress >/dev/null
ok sam item transition "$cause" --to in_review >/dev/null
ok sam item transition "$cause" --to done --set f_resolution=fixed >/dev/null
sleep 2
expect "the incident's events after the bug was fixed" "$(ok sam item history "$b1" --page-size 100 | j '.events | length')" "$before"
expect "the incident is still" "$(ok sam item get "$b1" | j .item.state)" "resolved"
expect "the emails, once each, after everything" "$(sent "$res1" "$breach_hook") / $(sent "$fr1" "$breach_hook")" "user:sam / user:sam"
expect "HOOK_RAN on the breached goal" "$(ok lea item history "$res1" --page-size 100 |
	jqx -r --arg h "$breach_hook" '[.events[] | select(.kind == "ITEM_EVENT_KIND_HOOK_RAN" and .hook == $h)] | length')" "1"

step "B9: the P3 is resolved in time (met), then reopened: a second resolution goal, and only that"
ok sam item transition "$b3" --to resolved --set f_resolution=fixed >/dev/null
wait_until 30 "the P3's resolution goal stopped" state_is ana "$res3" completed
expect "resolved in time" "$(fld "$res3" f_outcome) $(fld "$res3" f_breached_at)" "met "
ok ana item transition "$b3" --to in_progress >/dev/null
second_cycle() { [ "$(goals "$b3" resolution | wc -w | tr -d ' ')" = "2" ]; }
wait_until 30 "a second resolution goal" second_cycle
res3b=$(goal "$b3" resolution)
[ "$res3b" != "$res3" ] || fail "the current resolution goal is still the first cycle's"
expect "the second cycle: a whole new target, running" "$(fld "$res3b" f_target_s) $(ok ana item get "$res3b" | j .item.state)" "28800 running"
expect "the first cycle stays as it ended" "$(ok ana item get "$res3" | j .item.state) $(fld "$res3" f_outcome)" "completed met"
expect "first response is measured once" "$(goals "$b3" first_response)" "$fr3"
wait_until 30 "the incident shows the second cycle's due instant" fld_is "$b3" f_due_resolution "$(fld "$res3b" f_due_at)"

step "B10: a canceled incident ends its goals with no outcome; nothing breaches, nobody is told"
bx=$(incident ana P1 "Opened by mistake" --set f_assignee=user:sam)
wait_until 30 "its goals" has_goals "$bx"
frx=$(goal "$bx" first_response)
resx=$(goal "$bx" resolution)
ok ana item transition "$bx" --to canceled >/dev/null
wait_until 30 "the resolution goal canceled" state_is ana "$resx" canceled
wait_until 30 "the first response goal canceled" state_is ana "$frx" canceled
wait_until 30 "the canceled incident shows no due instant" fld_is "$bx" f_due_resolution ""
sleep 8 # past both due instants
expect "no outcome, no late mark" "$(fld "$resx" f_outcome)$(fld "$frx" f_outcome)$(fld "$resx" f_breached_at)" ""
expect "no email about the canceled incident's resolution goal" "$(sent "$resx" "$breach_hook")" ""
refuse ana "FAILED_PRECONDITION NO_TRANSITION" item transition "$bx" --to in_progress

step "B11: ana saves an incident with the etag read at its create, after its goals wrote on it; a real conflict is still STALE"
created=$(ok ana item create --type incident@1 --in "$desk" --set "f_title=Printer jam $w" --set f_priority=P4)
be=$(j .item.name <<<"$created")
etag_read=$(j .item.etag <<<"$created")
wait_until 30 "its goals" has_goals "$be"
wait_until 30 "both due instants shown on the incident" fld_set "$be" f_due_resolution
wait_until 30 "both due instants shown on the incident" fld_set "$be" f_due_first_response
wait_until 30 "both start records on the incident" records_are "$be" \
	"APPLIED incident:sla_start_first_response, APPLIED incident:sla_start_resolution"
events=$(ok ana item history "$be" --page-size 100 | j '.events | length')
expect "the incident's events: its create, two records, two due instants" "$events" "5"
expect "the etag, unchanged by the kernel's four writes" "$(ok ana item get "$be" | j .item.etag)" "$etag_read"
saved=$(ok ana item update "$be" --set f_impact=minor --etag "$etag_read")
expect "ana's save with the etag read at the create" "$(j .item.fields.f_impact <<<"$saved")" "minor"
[ "$(j .item.etag <<<"$saved")" != "$etag_read" ] || fail "the etag did not change with ana's save"
# sam read the incident before ana saved: his writes are real conflicts.
refuse sam "ABORTED STALE" item update "$be" --set f_impact=extensive --etag "$etag_read"
refuse sam "ABORTED STALE" item transition "$be" --to in_progress --etag "$etag_read"
# The etag is not item_seq: a decimal one, as before this step, is STALE.
refuse ana "ABORTED STALE" item update "$be" --set f_impact=moderate --etag "$(j .item_seq <<<"$saved")"
expect "the refused writes wrote nothing" "$(fld "$be" f_impact) $(ok ana item get "$be" | j .item.state)" "minor new"

# ---------------------------------------------------------------------------
# Scenario C1 (Jira part 2, step 4a): customers. kim and lee are customers
# (customer:kim, customer:lee); the IT desk gets a portal (desk@2) where a
# customer opens incidents (incident@3) and sees their own, the replies on
# them (reply@2) and nothing else: no note (note@2), no goal, no assignee,
# no deadline, nobody else's request, nothing else in the namespace. Staff
# see everything as before; sam and lea hold desk_agent. portal-bot acts
# for a customer with a customer's powers.
# ---------------------------------------------------------------------------

# cincident WHO PRIORITY TITLE [ARGS]...: an incident in the portal; prints its name.
cincident() {
	local who=$1 prio=$2 title=$3
	shift 3
	ok "$who" item create --type incident@3 --in "$portal" --set "f_title=$title $w" --set "f_priority=$prio" "$@" | j .item.name
}
# kinds WHO ITEM: the Types of what WHO lists inside ITEM, sorted.
kinds() { ok "$1" item list "$2" --page-size 100 | j '[.items[]?.type | split("/")[3]] | sort | join(" ")'; }

step "C1: lea opens a portal on the IT desk's calendar and policy; every customer sees it, its key and name only"
portal=$(ok lea item create --type desk@2 --set "f_key=IT-$w" --set "f_name=IT portal $w" | j .item.name)
ok lea item link "$portal" calendar "$cal" >/dev/null
ok lea item link "$portal" sla_policy "$policy" >/dev/null
expect "the portal, as kim reads it" "$(ok kim item get "$portal" | j '[.item.fields | keys[]] | join(" ")') links=$(ok kim item get "$portal" | j '.item.links | length')" "f_key f_name links=0"
expect "the portal, as sam reads it" "$(ok sam item get "$portal" | j '.item.links | length')" "2"
expect "kim's top level: portals only" "$(ok kim item list --page-size 500 | j '[.items[]?.type] | unique | join(" ")')" "namespaces/acme/types/desk/versions/2"
ok kim item list --page-size 500 | j '.items[]?.name' | grep -qx "$portal" || fail "kim's top level does not list the portal"

step "C2: kim opens a P3 incident; its goals start; she sees her fields only, with no etag"
refuse kim "PERMISSION_DENIED FIELD_NOT_WRITABLE" item create --type incident@3 --in "$portal" --set "f_title=x $w" --set f_priority=P3 --set f_assignee=user:sam
refuse kim "PERMISSION_DENIED NOT_ALLOWED" item create --type incident@3 --set "f_title=x $w" --set f_priority=P3
c1=$(cincident kim P3 "Wifi keeps dropping")
wait_until 30 "the incident's two goals" has_goals "$c1"
wait_until 30 "its due instants, for staff" fld_set "$c1" f_due_resolution
ok sam item update "$c1" --set f_assignee=user:sam >/dev/null
kim_view=$(ok kim item get "$c1")
expect "kim's fields" "$(j '[.item.fields | keys[]] | join(" ")' <<<"$kim_view")" "f_priority f_title"
expect "kim's etag" "$(j '.item.etag // ""' <<<"$kim_view")" ""
expect "the creator, as kim reads it" "$(j .item.creator <<<"$kim_view")" "customer:kim"
expect "sam sees the assignee and the deadlines" "$(ok sam item get "$c1" | j '[.item.fields | keys[]] | join(" ")')" \
	"f_assignee f_due_first_response f_due_resolution f_priority f_title"
lee1=$(cincident lee P3 "Printer offline")

step "C3: what kim cannot see: the goals, a note, lee's incident, the old desk, the PLAT space"
note1=$(ok sam item create --type note@2 --in "$c1" --set "f_body=customer is on the old wifi driver $w" | j .item.name)
for kim_hidden in "$(goal "$c1" first_response)" "$(goal "$c1" resolution)" "$note1" "$lee1" "$desk" "$plat" "$plat1"; do
	refuse kim "NOT_FOUND NOT_FOUND" item get "$kim_hidden"
done
refuse lee "NOT_FOUND NOT_FOUND" item get "$c1"
refuse kim "NOT_FOUND NOT_FOUND" item list "$lee1"
refuse kim "NOT_FOUND NOT_FOUND" item create --type reply@2 --in "$lee1" --set "f_body=hello"

step "C4: kim's history: the create and the moves, no reaction record, no assignee"
hist=$(ok kim item history "$c1" --page-size 100)
expect "kim's events" "$(j '[.events[].kind | sub("ITEM_EVENT_KIND_"; "")] | join(" ")' <<<"$hist")" "CREATED"
expect "no value she cannot see" "$(j '[.events[].fields // {} | keys[]] | unique | join(" ")' <<<"$hist")" "f_priority f_title"

step "C5: sam replies (first response met) and writes a note; kim sees the reply only"
ok sam item transition "$c1" --to in_progress >/dev/null
c1_reply=$(ok sam item create --type reply@2 --in "$c1" --set "f_body=Which client version? $w" | j .item.name)
wait_until 30 "first response stopped by sam's reply" state_is lea "$(goal "$c1" first_response)" completed
expect "what kim lists in her incident" "$(kinds kim "$c1")" "reply"
expect "what sam lists in it" "$(kinds sam "$c1")" "note reply sla_goal sla_goal"
expect "kim's search finds the portal, her incident and the reply, not the note nor lee's" "$(ok kim item search "$w" | names)" "$(sorted "$portal" "$c1" "$c1_reply")"
expect "kim's incidents in the portal" "$(ok kim item list "$portal" | names)" "$c1"
expect "a search on a hidden field is refused as one on an unknown field" \
	"$(run kim item search "$w" --where f_assignee=user:sam; head -n 1 "$tmp/err")" \
	"$(run kim item search "$w" --where f_nope=user:sam; head -n 1 "$tmp/err")"

step "C6: kim's reply brings a waiting incident back and resumes resolution; a note does not"
ok sam item transition "$c1" --to waiting_for_customer >/dev/null
wait_until 30 "resolution paused" state_is lea "$(goal "$c1" resolution)" paused
ok sam item create --type note@2 --in "$c1" --set "f_body=still waiting $w" >/dev/null
sleep 2
expect "after sam's note" "$(ok kim item get "$c1" | j .item.state)" "waiting_for_customer"
ok kim item create --type reply@2 --in "$c1" --set "f_body=Driver 4.2, still broken $w" >/dev/null
wait_until 30 "kim's reply brings it back" state_is kim "$c1" in_progress
wait_until 30 "resolution resumed" state_is lea "$(goal "$c1" resolution)" running

step "C7: kim never updates, links, moves or writes a note; she takes only the requester's moves"
refuse kim "PERMISSION_DENIED NOT_ALLOWED" item update "$c1" --set "f_title=renamed $w"
refuse kim "PERMISSION_DENIED NOT_ALLOWED" item create --type note@2 --in "$c1" --set "f_body=x"
refuse kim "PERMISSION_DENIED NOT_ALLOWED" item transition "$c1" --to waiting_for_customer
refuse kim "PERMISSION_DENIED NOT_ALLOWED" item transition "$c1" --to canceled
refuse kim "NOT_FOUND NOT_FOUND" item update "$lee1" --set "f_title=x"
ok sam item transition "$c1" --to resolved --set f_resolution=fixed >/dev/null
ok kim item transition "$c1" --to in_progress >/dev/null
expect "kim reopened it" "$(ok kim item get "$c1" | j .item.state)" "in_progress"

step "C8: not found is not found: a hidden Item and a missing one, the same bytes, by name and by key"
missing="namespaces/acme/items/$(random_uuid)"
for verb in "item get" "item list" "item history"; do
	run kim $verb "$lee1"
	hidden_err=$(cat "$tmp/err")
	run kim $verb "$missing"
	expect "kim: $verb, hidden and missing" "$hidden_err" "$(cat "$tmp/err")"
done
staff_desk=$(ok lea item create --type desk@2 --set "f_key=DESK-12-$w" --set "f_name=staff only $w" --narrow user:lea,user:sam | j .item.name)
expect "kim searches a hidden portal's key, and a key nobody uses" \
	"$(ok kim item search --type desk --where "f_key=DESK-12-$w")" "$(ok kim item search --type desk --where "f_key=DESK-13-$w")"
expect "lea finds it" "$(ok lea item search --type desk --where "f_key=DESK-12-$w" | names)" "$staff_desk"

step "C9: sam takes kim's call: an incident he opens for her is hers; lee cannot see it"
c9=$(cincident sam P2 "Phoned in: Outlook crashes" --requester customer:kim)
expect "who and for whom" "$(ok kim item get "$c9" | j '"\(.item.creator) \(.item.creator_on_behalf_of)"')" "user:sam customer:kim"
refuse lee "NOT_FOUND NOT_FOUND" item get "$c9"
refuse sam "INVALID_ARGUMENT INVALID_REQUEST" item create --type incident@3 --in "$portal" --requester user:ana --set "f_title=x" --set f_priority=P3
run sam item create --type incident@3 --in "$staff_desk" --requester customer:kim --set "f_title=x" --set f_priority=P3
hidden_err=$(cat "$tmp/err")
run sam item create --type incident@3 --in "$staff_desk" --requester customer:nobody --set "f_title=x" --set f_priority=P3
expect "a customer the portal hides, and one that never was" "$hidden_err" "$(cat "$tmp/err")"
ok kim item transition "$c9" --to canceled >/dev/null

step "C10: portal-bot acts for kim and for lee: each one's view; one request id, two writes"
s_kim=$(ok portal-bot agent start --for customer:kim --scopes namespaces/acme --powers 'read,create,comment,transition:*' | j .session.id)
s_lee=$(ok portal-bot agent start --for customer:lee --scopes namespaces/acme --powers 'read,create,comment,transition:*' | j .session.id)
refuse portal-bot "PERMISSION_DENIED NOT_ALLOWED" agent start --for customer:kim --scopes namespaces/acme --powers read,edit_fields
ok portal-bot --session "$s_kim" item get "$c1" >/dev/null
refuse portal-bot "NOT_FOUND NOT_FOUND" --session "$s_kim" item get "$lee1"
refuse portal-bot "NOT_FOUND NOT_FOUND" --session "$s_lee" item get "$c1"
rid="c10-$w"
for_kim=$(ok portal-bot --session "$s_kim" item create --type reply@2 --in "$c1" --set "f_body=from the portal" --request-id "$rid" | j .item.name)
for_lee=$(ok portal-bot --session "$s_lee" item create --type reply@2 --in "$lee1" --set "f_body=from the portal" --request-id "$rid" | j .item.name)
[ "$for_kim" != "$for_lee" ] || fail "one request id for kim and for lee gave one reply"
expect "kim's retry" "$(ok portal-bot --session "$s_kim" item create --type reply@2 --in "$c1" --set "f_body=from the portal" --request-id "$rid" | j .item.name)" "$for_kim"
expect "lee's reply is lee's" "$(ok lee item get "$for_lee" | j .item.creator_on_behalf_of)" "customer:lee"

step "C11: kim's P1 breaches: sam, its assignee, is emailed; kim never"
c11=$(cincident kim P1 "Everything is down")
ok sam item update "$c11" --set f_assignee=user:sam >/dev/null
wait_until 30 "its goals" has_goals "$c11"
fr11=$(goal "$c11" first_response)
res11=$(goal "$c11" resolution)
wait_until 30 "sam told of both breaches" sent_is "$res11" "$breach_hook" "user:sam"
wait_until 30 "sam told of both breaches" sent_is "$fr11" "$breach_hook" "user:sam"
expect "kim's search finds her P1 and nothing of the SLA" \
	"$(ok kim item search "$w" --type incident --where f_priority=P1 | names)" "$c11"

step "C12: what a customer client gets since the review: no commit_seq, no min_seq, a hidden link name as an unknown one, APPROVAL_UNAVAILABLE"
c12=$(ok kim item create --type reply@2 --in "$c1" --set "f_body=one more $w")
expect "kim's create answers no commit_seq" "$(j '.commit_seq // 0 | tostring' <<<"$c12")" "0"
expect "kim's history carries no commit_seq" "$(ok kim item history "$c1" --page-size 100 | j '[.events[] | .commit_seq // 0 | tostring] | unique | join(",")')" "0"
refuse kim "INVALID_ARGUMENT INVALID_REQUEST" item search "$w" --min-seq 1
run kim item search "$w" --linked "out:caused_by=$c1"
hidden_name=$(cat "$tmp/err")
run kim item search "$w" --linked "out:no_such_link=$c1"
expect "a link name hidden from kim, and one nobody declares" "$hidden_name" "$(cat "$tmp/err")"
[ -n "$hidden_name" ] || fail "kim's search on caused_by was not refused"
c12n=$(cincident kim P4 "Narrowed for the bot")
ok lea item update "$c12n" --readers user:lea,user:sam,customer:kim,agent:portal-bot >/dev/null
ok portal-bot --session "$s_kim" item get "$c12n" >/dev/null
refuse portal-bot "PERMISSION_DENIED APPROVAL_UNAVAILABLE" --session "$s_kim" item create --type reply@2 --in "$c1" --set "f_body=wider $w"
err_has "an agent acting for a customer cannot be approved"

step "C13: the hardened versions: an incident@3 is placed by desk agents only, a reply and a note stay where written, a goal is no one's"
expect "kim's incident's goals" "$(ok lea item get "$(goal "$c1" resolution)" | j .item.type)" "namespaces/acme/types/sla_goal/versions/3"
a3=$(ok ana item create --type incident@3 --in "$portal" --set "f_title=Laptop slow $w" --set f_priority=P4 | j .item.name)
refuse ana "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$a3" --readers user:ana
refuse ana "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$a3" --move-to "$desk"
refuse sam "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$c1_reply" --move-to "$lee1"
refuse sam "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$note1" --readers user:sam
refuse sam "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$(goal "$c1" resolution)" --readers user:sam
refuse lea "PERMISSION_DENIED NOT_ALLOWED" item transition "$(goal "$c1" resolution)" --to completed
ok sam item update "$a3" --readers user:sam,user:lea,user:ana >/dev/null

# ---------------------------------------------------------------------------
# Scenario E (Jira 2.4b): an access request. A resource says who approves
# it and how many must; a request inside it copies both on submit, gets one
# approval per approver and an approval goal, and moves once when enough
# decide. lea holds access_admin (the resources) and sla_admin (their
# calendar and policy); sam is the resources' contact for breaches. ana
# asks, and is also an approver of one resource: she can neither approve her
# own request nor change who does.
# ---------------------------------------------------------------------------

# approvals REQ: "approver:state" for each approval in REQ, sorted.
approvals() {
	ok lea item list "$1" --type approval --page-size 100 |
		j '[.items[]? | "\(.fields.f_approver):\(.state)"] | sort | join(" ")'
}
# approval_of REQ WHO: the name of WHO's approval in REQ.
approval_of() {
	ok lea item list "$1" --type approval --page-size 100 | jqx -r --arg w "$2" '[.items[]? | select(.fields.f_approver == $w) | .name][0] // ""'
}
# children_of REQ TYPE: the names of REQ's children of TYPE.
children_of() { ok lea item list "$1" --type "$2" --page-size 100 | names; }
has_approvals() { [ "$(approvals "$1" | wc -w | tr -d ' ')" = "$2" ]; }
approvals_are() { [ "$(approvals "$1")" = "$2" ]; }
has_child() { [ -n "$(children_of "$1" "$2")" ]; }
# request WHO RESOURCE TITLE: a submitted request; prints its name.
request() {
	local r
	r=$(ok "$1" item create --type access_request@1 --in "$2" --set "f_title=$3 $w" | j .item.name)
	ok "$1" item transition "$r" --to pending >/dev/null
	printf '%s\n' "$r"
}
breach_hook2=$(ok lea type get sla_goal@2 | j '[.type.states[] | select(.id == "running") | .acting[]?.hook][0] // ""')

step "E1: lea sets up the access desk (desk@3, for its people only) and two resources; sam cannot narrow it"
adesk=$(ok lea item create --type desk@3 --set "f_key=AR-$w" --set "f_name=Access $w" \
	--narrow user:lea,user:sam,user:ana,user:bob,user:dana,user:erin | j .item.name)
refuse sam "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$adesk" --readers user:sam
slow=$(ok lea item create --type sla_policy@2 --set "f_name=Access, a day $w" --set f_approval_s=86400 | j .item.name)
fast=$(ok lea item create --type sla_policy@2 --set "f_name=Access, seconds $w" --set f_approval_s=3 | j .item.name)
wiki=$(ok lea item create --type resource@1 --in "$adesk" --set "f_name=Wiki $w" \
	--set 'f_approvers=["user:bob","user:dana","user:erin"]' --set f_required=2 --set f_contact=user:sam | j .item.name)
prod=$(ok lea item create --type resource@1 --in "$adesk" --set "f_name=Prod DB $w" \
	--set 'f_approvers=["user:ana","user:bob","user:dana"]' --set f_required=2 --set f_contact=user:sam | j .item.name)
for r in "$wiki" "$prod"; do ok lea item link "$r" calendar "$cal" >/dev/null; done
ok lea item link "$wiki" sla_policy "$slow" >/dev/null
ok lea item link "$prod" sla_policy "$fast" >/dev/null
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item create --type resource@1 --in "$adesk" --set "f_name=mine $w" \
	--set 'f_approvers=["user:ana"]' --set f_required=1
refuse lea "FAILED_PRECONDITION INVARIANT_FAILED" item create --type resource@1 --in "$adesk" --set "f_name=none $w" \
	--set 'f_approvers=["user:bob"]' --set f_required=2

step "E2: a request outside a resource cannot be submitted (no quorum to copy)"
loose=$(ok ana item create --type access_request@1 --in "$adesk" --set "f_title=loose $w" | j .item.name)
refuse ana "FAILED_PRECONDITION INVARIANT_FAILED" item transition "$loose" --to pending
expect "the invariant" "$(meta invariant)" "submit_without_quorum"

step "E3: 2 of 3 approve: the request moves once; the third approval is closed and refuses a late decision"
e1=$(request ana "$wiki" "Wiki for the audit")
wait_until 30 "one approval per approver" has_approvals "$e1" 3
expect "the copies" "$(fld "$e1" f_required) $(ok lea item get "$e1" | j '.item.fields.f_approvers | join(" ")') $(fld "$e1" f_desk_contact)" \
	"2 user:bob user:dana user:erin user:sam"
expect "the approval's title" "$(fld "$(approval_of "$e1" user:bob)" f_title)" "Wiki for the audit $w"
wait_until 30 "the approval goal" fld_set "$e1" f_due_approval
ok bob item transition "$(approval_of "$e1" user:bob)" --to approved >/dev/null
ok dana item transition "$(approval_of "$e1" user:dana)" --to approved >/dev/null
wait_until 30 "the request approved" state_is lea "$e1" approved
wait_until 30 "erin's approval closed" approvals_are "$e1" "user:bob:approved user:dana:approved user:erin:canceled"
refuse erin "FAILED_PRECONDITION NO_TRANSITION" item transition "$(approval_of "$e1" user:erin)" --to approved
expect "moved once, by the second approval" \
	"$(ok lea item history "$e1" --page-size 100 | j '[.events[] | select(.kind == "ITEM_EVENT_KIND_TRANSITIONED" and .to_state == "approved") | .actor] | join(" ")')" \
	"reaction:approval:move_approved"
wait_until 30 "the goal stopped" fld_is "$e1" f_due_approval ""
expect "the goal: met" "$(ok lea item list "$e1" --type sla_goal | j '[.items[] | "\(.state) \(.fields.f_outcome)"] | join(",")')" "completed met"

step "E4: the fulfilment task appears for sam and lea"
wait_until 30 "the fulfilment task" has_child "$e1" fulfilment_task
task=$(children_of "$e1" fulfilment_task)
for who in sam lea; do
	expect "$who reads the task" "$(ok "$who" item get "$task" | j '"\(.item.state) \(.item.fields.f_resource)"')" "open Wiki $w"
done
ok sam item transition "$task" --to in_progress >/dev/null
refuse ana "PERMISSION_DENIED NOT_ALLOWED" item transition "$task" --to done

step "E5: a rejection after one approval rejects the request and closes the rest"
e2=$(request ana "$wiki" "Wiki for the contractor")
wait_until 30 "its approvals" has_approvals "$e2" 3
ok erin item transition "$(approval_of "$e2" user:erin)" --to approved >/dev/null
ok bob item transition "$(approval_of "$e2" user:bob)" --to rejected >/dev/null
wait_until 30 "the request rejected" state_is lea "$e2" rejected
wait_until 30 "dana's approval closed" approvals_are "$e2" "user:bob:rejected user:dana:canceled user:erin:approved"
wait_until 30 "the goal stopped" fld_is "$e2" f_due_approval ""
expect "a rejection is a decision: the goal is met" "$(ok lea item list "$e2" --type sla_goal | j '[.items[] | "\(.state) \(.fields.f_outcome)"] | join(",")')" "completed met"
expect "no fulfilment task" "$(children_of "$e2" fulfilment_task)" ""

step "E6: bob hands over to sam; refused to dana (already approving) and to himself; one to ana she cannot decide"
e3=$(request ana "$wiki" "Wiki for the auditor")
wait_until 30 "its approvals" has_approvals "$e3" 3
refuse bob "FAILED_PRECONDITION NOT_UNIQUE" item transition "$(approval_of "$e3" user:bob)" --to pending --set f_delegate=user:dana
refuse bob "FAILED_PRECONDITION GUARD_FAILED" item transition "$(approval_of "$e3" user:bob)" --to pending --set f_delegate=user:bob
refuse dana "PERMISSION_DENIED NOT_ALLOWED" item transition "$(approval_of "$e3" user:bob)" --to pending --set f_delegate=user:sam
hand=$(approval_of "$e3" user:bob)
ok bob item transition "$hand" --to pending --set f_delegate=user:sam >/dev/null
expect "the approval is sam's" "$(fld "$hand" f_approver)" "user:sam"
refuse bob "PERMISSION_DENIED NOT_ALLOWED" item transition "$hand" --to approved
expect "the history says who handed over to whom" \
	"$(ok lea item history "$hand" --page-size 100 | j '[.events[] | select(.kind == "ITEM_EVENT_KIND_TRANSITIONED" and .from_state == "pending" and .to_state == "pending") | .actor] | join(" ")')" "user:bob"
# The documented weakness: handed to the requester, an approval waits for access_admin.
ok erin item transition "$(approval_of "$e3" user:erin)" --to pending --set f_delegate=user:ana >/dev/null
refuse ana "PERMISSION_DENIED NOT_ALLOWED" item transition "$(approval_of "$e3" user:ana)" --to approved
ok lea item transition "$(approval_of "$e3" user:ana)" --to pending --set f_delegate=user:erin >/dev/null
ok sam item transition "$hand" --to approved >/dev/null
ok erin item transition "$(approval_of "$e3" user:erin)" --to approved >/dev/null
wait_until 30 "the request approved" state_is lea "$e3" approved

step "E7: ana, requester and approver of the prod DB: she cannot approve her own request, nor change who does"
e4=$(request ana "$prod" "prod DB read access")
wait_until 30 "its approvals" has_approvals "$e4" 3
anas=$(approval_of "$e4" user:ana)
refuse ana "PERMISSION_DENIED NOT_ALLOWED" item transition "$anas" --to approved
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$e4" --set 'f_approvers=["user:ana"]'
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item update "$prod" --set f_required=1
refuse ana "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$anas" --move-to "$e1"
refuse ana "PERMISSION_DENIED PLACEMENT_NOT_WRITABLE" item update "$e4" --readers user:ana
refuse ana "PERMISSION_DENIED FIELD_NOT_WRITABLE" item create --type approval@1 --in "$e4" --set f_approver=user:ana

step "E8: the prod DB's target is 3 s: its approval goal breaches, sam is emailed, and the late decision is missed"
[ -n "$breach_hook2" ] || fail "sla_goal@2's running state has no acting timer with a hook"
wait_until 30 "the approval goal" has_child "$e4" sla_goal
g4=$(children_of "$e4" sla_goal)
wait_until 30 "the goal breached" fld_set "$g4" f_breached_at
wait_until 30 "sam emailed about the breach" sent_is "$g4" "$breach_hook2" user:sam
ok bob item transition "$(approval_of "$e4" user:bob)" --to approved >/dev/null
ok dana item transition "$(approval_of "$e4" user:dana)" --to approved >/dev/null
wait_until 30 "the request approved" state_is lea "$e4" approved
wait_until 30 "the goal stopped" state_is lea "$g4" completed
expect "a decision after the due instant: missed" "$(fld "$g4" f_outcome)" "missed"
wait_until 30 "ana's approval closed" state_is lea "$anas" canceled
expect "sam was emailed once" "$(sent "$g4" "$breach_hook2")" "user:sam"

step "D1: lea searches VPN in the active category"
vpn=$(issue lea story "VPN drops on wifi")
issue lea task "VPN setup guide" >/dev/null
ok lea item transition "$vpn" --to in_progress >/dev/null
expect "active VPN work" "$(ok lea item search VPN "$w" --category active | names)" "$vpn"

step "D2: lea pages through the open P1 stories, bugs and tasks, 20 per page, ordered by points"
want=""
n=45
i=1
while [ "$i" -le "$n" ]; do
	case $((i % 3)) in 0) typ=story ;; 1) typ=bug ;; *) typ=task ;; esac
	seeded=$(issue lea "$typ" "P1 seed $i" --set f_priority=P1 --set "f_points=$((i + 10))")
	want="$seeded${want:+ $want}" # most points first
	[ "$i" -ne 9 ] || plat9=$seeded
	i=$((i + 1))
done
unpointed=$(issue lea bug "P1 without points" --set f_priority=P1)
# PLAT-3 is P1 and open too, with 3 points: after every seed. The bug
# without points comes last, even though the order is descending.
want="$want $plat3 $unpointed"
got=""
token=""
pages=0
began=$(now_ms)
while :; do
	page=$(ok lea item search "$w" --type story --type bug --type task --where f_priority=P1 \
		--category todo --category active --order-by 'f_points desc' --page-size 20 ${token:+--page-token "$token"})
	pages=$((pages + 1))
	size=$(j '.items | length' <<<"$page")
	token=$(j '.next_page_token // ""' <<<"$page")
	got="$got${got:+ }$(in_order <<<"$page")"
	if [ -n "$token" ]; then expect "page $pages is full" "$size" "20"; else break; fi
	[ "$pages" -lt 10 ] || fail "more than 10 pages: the token does not advance"
done
echo "    $((n + 2)) Items in $pages pages took $(($(now_ms) - began)) ms"
expect "pages" "$pages" "3"
expect "every open P1 once, most points first, the one without points last" "$got" "$want"
# A token belongs to its order.
first=$(ok lea item search "$w" --type story --where f_priority=P1 --order-by f_points --page-size 2 | j .next_page_token)
refuse lea "INVALID_ARGUMENT INVALID_REQUEST" item search "$w" --type story --where f_priority=P1 \
	--order-by 'f_points desc' --page-size 2 --page-token "$first"

step "D3: ana watches PLAT-9: a Subscription linked to it by watches; removing the link unwatches"
sub=$(ok ana item create --type subscription@1 --narrow user:ana | j .item.name)
refuse ana "NOT_FOUND NOT_FOUND" item link "$sub" watches "$hidden"
refuse ana "NOT_FOUND NOT_FOUND" item link "$sub" watches "namespaces/acme/items/$(random_uuid)"
ok ana item link "$sub" watches "$plat9" >/dev/null
expect "what ana watches" "$(ok ana item search --linked "out:watches=$plat9" | names)" "$sub"
expect "PLAT-9's watchers as ana" \
	"$(ok ana item get "$plat9" | j '[.item.links[]? | select(.link_type == "watched_by") | .item.name] | join(" ")')" "$sub"
expect "PLAT-9's watchers as lea, who cannot see ana's subscription" \
	"$(ok lea item get "$plat9" | j '[.item.links[]? | select(.link_type == "watched_by")] | length')" "0"
refuse lea "NOT_FOUND NOT_FOUND" item get "$sub"
# A ticket can be watched too: the link targets any Type.
sub2=$(ok ana item create --type subscription@1 --narrow user:ana | j .item.name)
ok ana item link "$sub2" watches "$t1" >/dev/null
ok ana item unlink "$sub" watches "$plat9" >/dev/null
expect "PLAT-9's watchers after ana unwatches" \
	"$(ok ana item get "$plat9" | j '[.item.links[]? | select(.link_type == "watched_by")] | length')" "0"
expect "the subscription is kept, watching nothing" "$(ok ana item get "$sub" | j '.item.links // [] | length')" "0"

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
# Ventoux V2b: draft Types checked against every registered version, with
# nothing registered (pli type validate, ValidateTypes).
# ---------------------------------------------------------------------------

step "type validate: the repository's Types pass as drafts, and pli prints nothing"
run ana type validate "$repo/types"
[ "$rc" -eq 0 ] || fail "pli type validate types/ exited $rc, want 0" "$(cat "$tmp/out")"
[ ! -s "$tmp/out" ] || fail "pli type validate types/ printed: $(cat "$tmp/out")"

step "type validate: a changed ticket v1 and a new Type with a problem exit 1: the new Type's problem first, then VERSION_CHANGED once it is sound; nothing is registered"
mkdir -p "$tmp/drafts"
awk '{ print } /^fields:$/ { print "  - {id: f_extra, type: string}" }' "$repo/types/ticket.v1.yaml" >"$tmp/drafts/ticket.v1.yaml"
grep -q 'f_extra' "$tmp/drafts/ticket.v1.yaml" || fail "could not change the ticket v1 draft"
draft_id="vt_${w}"
printf 'id: %s\nversion: 1\nfields:\n  - {id: today, type: date}\n' "$draft_id" >"$tmp/drafts/new.v1.yaml"
run ana type validate "$tmp/drafts"
[ "$rc" -eq 1 ] || fail "pli type validate drafts/ exited $rc, want 1" "$(cat "$tmp/out")"
expect "problem lines" "$(wc -l <"$tmp/out" | tr -d ' ')" 1
grep -q "^$tmp/drafts/new.v1.yaml:4:6: RESERVED_NAME typedef $draft_id v1: " "$tmp/out" ||
	fail "no RESERVED_NAME line for the new Type:" "$(cat "$tmp/out")"
# VERSION_CHANGED waits for every draft to be sound on its own.
grep -q "VERSION_CHANGED" "$tmp/out" && fail "VERSION_CHANGED before the drafts are each sound:" "$(cat "$tmp/out")"
printf 'id: %s\nversion: 1\nfields:\n  - {id: f_today, type: date}\n' "$draft_id" >"$tmp/drafts/new.v1.yaml"
run ana type validate "$tmp/drafts"
[ "$rc" -eq 1 ] || fail "pli type validate drafts/ exited $rc, want 1" "$(cat "$tmp/out")"
expect "problem lines" "$(wc -l <"$tmp/out" | tr -d ' ')" 1
grep -q "^$tmp/drafts/ticket.v1.yaml:3:1: VERSION_CHANGED typedef ticket v1: " "$tmp/out" ||
	fail "no VERSION_CHANGED line for ticket v1:" "$(cat "$tmp/out")"
refuse ana "NOT_FOUND NOT_FOUND" type get "${draft_id}@1"

# ---------------------------------------------------------------------------
# Ventoux V1: action contracts. The catalogue an authoring tool chooses from
# (pli action list, ListActions), and hooks checked against it.
# ---------------------------------------------------------------------------

step "action list: the six actions, hr.manager_of@1 with its types, trust (declared, not enforced) and reasons"
run ana action list
[ "$rc" -eq 0 ] || fail "pli action list exited $rc, want 0" "$(cat "$tmp/err")"
expect "actions" "$(grep -c '^[a-z]' "$tmp/out" | tr -d ' ')" 6
for line in "hr.manager_of@1 (check; risk low; no side effects)" \
	"  in  createdBy: principal or null; trusted (declared, not enforced until Ventoux item 4)" \
	"  reasons: unknown_employee, no_eligible_approver" \
	"erp.create_invoice@1 (action; risk high; side effects)"; do
	grep -qxF "$line" "$tmp/out" || fail "pli action list has no line: $line" "$(cat "$tmp/out")"
done

step "action list --json: each digest is the one contracts/contracts.lock publishes"
listed=$(ok ana action list --json)
while read -r action digest; do
	case $action in "#"* | "") continue ;; esac
	expect "$action's digest" "$(jqx -r --arg a "$action" '.actions[] | select(.action == $a) | .digest' <<<"$listed")" "$digest"
done <"$repo/contracts/contracts.lock"

step "action list: a customer may not read the catalogue (NOT_ALLOWED)"
refuse kim "PERMISSION_DENIED NOT_ALLOWED" action list

step "type validate: a hook against its action's contract: the wrong kind (ACTION_KIND), a principal into a string input (WRONG_FIELD_TYPE)"
rm -rf "$tmp/drafts" && mkdir -p "$tmp/drafts"
cat >"$tmp/drafts/hooked.v1.yaml" <<YAML
id: vt_hooked_${w}
version: 1
initial_state: open
fields:
  - {id: f_ok, type: bool}
  - {id: f_plan, type: string}
states:
  - {id: open, category: todo}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
hooks:
  - {id: plan, on: transition, from: open, to: done, kind: check, action: crm.get_plan, reads: [created_by], writes: [f_plan]}
YAML
run ana type validate "$tmp/drafts"
[ "$rc" -eq 1 ] || fail "pli type validate exited $rc, want 1" "$(cat "$tmp/out")"
expect "problem lines" "$(wc -l <"$tmp/out" | tr -d ' ')" 2
grep -q "^$tmp/drafts/hooked.v1.yaml:13:54: ACTION_KIND typedef vt_hooked_${w} v1: " "$tmp/out" ||
	fail "no ACTION_KIND line:" "$(cat "$tmp/out")"
grep -q "^$tmp/drafts/hooked.v1.yaml:13:[0-9]*: WRONG_FIELD_TYPE typedef vt_hooked_${w} v1: .*created_by (principal) into input customerId of crm.get_plan@1" "$tmp/out" ||
	fail "no WRONG_FIELD_TYPE line for created_by:" "$(cat "$tmp/out")"

# ---------------------------------------------------------------------------
# Every reason of the error table a client can cause came back once.
# ---------------------------------------------------------------------------

step "every reachable refusal was seen"
for reason in UNAUTHENTICATED SESSION_INVALID INVALID_REQUEST NOT_FOUND NOT_ALLOWED NEEDS_APPROVAL \
	NO_TRANSITION MISSING_FIELDS GUARD_FAILED WIDEN_NOT_CONFIRMED STALE HELD \
	IDEMPOTENCY_KEY_REUSED NOT_CAUGHT_UP AUDIENCE_CHANGED \
	LINK_CARDINALITY ALREADY_LINKED NOT_LINKED LINK_TARGET_TYPE \
	FIELD_NOT_WRITABLE FIELD_FROZEN INVARIANT_FAILED LINK_NOT_WRITABLE TOO_MANY_VALUES APPROVAL_UNAVAILABLE \
	PLACEMENT_NOT_WRITABLE NOT_UNIQUE; do
	case $seen in *" $reason "*) ;; *) fail "no step triggered $reason" ;; esac
done
cat <<'EOF'
    Not triggered here (a client cannot cause them; Go tests cover each):
      NOT_INITIAL_STATE  a create cannot name a state through the API
      HOP_LIMIT          only chains of hook writes reach it
      EFFECT_FAILED      story@2's only effect is cause.at, which cannot
                         fail; a Type whose effect can is in the Go tests
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
