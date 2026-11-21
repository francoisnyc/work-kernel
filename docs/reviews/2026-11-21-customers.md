# Review: Jira part 2, step 4a, customers (2026-11-21)

An adversarial review of main at 4eec859. Its question: how can a `customer:` principal, or an agent
acting for one, learn, read or change what the design says they must not, and do staff lose anything
they had before this step? Each finding below has a failing test on this branch, named `TestReview_…`.
Nothing in the code is changed.

## Summary

No finding gives a customer another customer's request, an internal note, an SLA goal, a staff-only
field value, a portal they are not admitted to, or anything in another namespace. Five findings leak
less than that: link names and counts, and how busy the system is. A sixth leaves an incident opened
"for" a customer hidden from that customer. One of the five leaks is proven only as a load rule.
No staff regression was found.

| # | Finding | Severity | Test |
| --- | --- | --- | --- |
| 1 | A Search link condition takes a link name the customer may not see, and shows them the link | leak (low) | `itemservice` `TestReview_Search_ALinkConditionOnAnUnlistedLinkName_RevealsTheLink` |
| 2 | `links_truncated` counts links the customer never sees | leak (low) | `itemservice` `TestReview_Get_LinksTruncated_CountsLinksTheCustomerCannotSee` |
| 3 | `commit_seq` in a customer's write answers counts everyone else's writes | leak (low, the counter predates the step) | `itemservice` `TestReview_CommitSeq_CountsOtherCustomersWrites` |
| 4 | `requester` together with a narrowing that leaves the customer out is accepted | hardening | `itemservice` `TestReview_Requester_WithANarrowingThatHidesTheCustomer_IsAccepted` |
| 5 | A listed rollup may count over an unlisted link | hardening (a leak once a Type does it) | `typedef` `TestReview_CustomerFields_ARollupOverAnUnlistedLink_IsRefusedAtLoad` |

## Findings

### 1. A Search link condition on an unlisted link name (leak, low)

**The design says** a customer sees a link only if its name is listed in `customer_fields` and the
other end is visible to them.

**What happens:** `Get` follows that rule (`policy.Project`, `itemLinks`). `resolveFilter` →
`linkCond` does not. It checks only that the condition's Item is visible to the customer, then
filters on any declared link name.

**The attack:** sam links kim's two tickets with `relates`, which is not listed for customers. kim
searches `kticket` with the condition `{relates, outgoing, t2}` and gets t1 back. She now knows t1
relates to t2, a link `Get` hides from her. With an incoming name she learns the inverse the same way.

**Fix:** in `linkCond`, for a customer (`policy.CustomerOf(p) != ""`), accept a name only if some
version of a Type that declares it lists it in `customer_fields`. Outgoing, that is the declared name;
incoming, the declaring Type's name. Otherwise refuse with the same `InvalidError` as an undeclared
name, so a hidden name and a missing name look alike. That is the rule `typesFor` already applies to
field filters.

### 2. `links_truncated` counts links a customer never sees (leak, low)

**What happens:** `itemLinks` reads 201 links per direction, restricted and unlisted ones included,
and sets `LinksTruncated` from that count. `projectForCustomer` drops those links but keeps the flag.

**The attack:** sam links kim's incident to 201 bugs she cannot see. She gets no link, and
`links_truncated = true`, which tells her the incident has more than 200 links she can't see.

**Fix:** in `projectForCustomer`, set `LinksTruncated` from what is left after filtering. Better:
for a customer, apply the name list and drop restricted links in the SQL, so the 200-link limit counts
only links she sees.

### 3. `commit_seq` counts other people's writes (leak, low)

**What happens:** every write answers its `commit_seq`, taken from one sequence shared by every Item
and namespace.

**The attack:** kim writes, lee writes five replies, kim writes again. kim's two numbers are 455 and
461: she can tell that exactly five writes happened in between. In general the gap measures the
activity of every other customer, of staff, and of other namespaces. The design accepts that
`item_seq`, `updated_at` and the etag reveal "that something hidden changed" on an Item the customer
sees. It does not say anything about a counter covering the whole system. `min_seq` on Search is the
same counter used as a probe.

**Fix:** for a customer, answer `commit_seq` as 0 (or leave it out). Keep their read-your-writes by
giving them a token that stands for "this write" and that only the server can read. Or accept the leak
in design.md, next to the `item_seq` note. This is the smallest decision to make; the counter itself
predates this step.

### 4. `requester` with a narrowing that leaves the customer out (hardening)

**What happens:** sam creates an incident with `requester: customer:kim` and `narrow: [sam]`. It is
accepted. kim is recorded as its requester, but she cannot see it, cancel it, reopen it or reply to it.
Nothing leaks; the request just makes no sense. `checkReaders` already refuses a narrowing that would
hide the Item from the caller; the requester gets no such protection.

**Fix:** in `checkRequester`, when the request also narrows, require the requester among the readers.
Otherwise answer the same `InvalidError` on `requester`.

### 5. A listed rollup over an unlisted link (hardening)

**What happens:** `checkCustomers` accepts `customer_fields: [progress]` when the rollup `progress`
counts over `blocks` and `blocks` is not listed. Such a rollup tells a customer how many hidden links
exist, and in which states. No Type in `types/` does this today.

**Fix:** in `checkCustomers`, refuse a listed rollup whose link name (its `over`, or the declared name
behind an incoming one) is not listed too.

## Unproven (no test)

- **Time of check against time of use on a customer's create and transition.**
  `checkCustomerCreate` and `Transition`'s pre-check apply the customer rule with `visibleItem`
  *before* the write transaction. Inside the transaction only the scope test runs again. Suppose staff
  move kim's incident out of its portal, into the old desk or another customer's incident, between the
  two. kim's reply, or her cancel, then lands on an Item that is no longer hers. Proving it needs a
  hook between the two reads (the write path has `SetTestHook` only inside the transaction).
  **Fix:** run `checkCustomerSees` again inside the transaction, after the row lock (container or
  Item).
- **An agent for a customer cannot be approved.** The agent write rule may need `ApproveWiden` from
  the user the agent acts for. For a customer, `ApproveWiden` refuses the approver (`user:` only).
  Suppose a portal bot reads a narrowed incident and then writes into a wider scope. That write can
  never be approved: a wrong refusal with no way out. **Fix:** let a customer approve for their own
  agent, within what they can see. Or refuse at session start the reads that would need it, and say
  which.
- **The web UI's Inbox for a customer.** `inboxQueries` searches every `field:X` principal field with
  `me`. For a customer those fields are hidden, so the server refuses with `INVALID_ARGUMENT`, and the
  page shows a refusal instead of an empty inbox. This was not run in Playwright. **Fix:** no Inbox
  for a customer, or skip a field the customer view does not list.
- **Migration 00013 down.** It restores the key `(namespace_id, actor, key)` and fails if an agent
  used one key for two people after the migration. It fails loudly, which is acceptable for a down
  migration, but its comment should say so.

## What I tried that held

- **Reads.**
  - `Get`, `List`, `Search` (no words, words, filters, `order_by`, page sizes 1, 2, 3 and 50, and a
    page token reused by another customer): only her Items, only her fields.
  - Field filters, `order_by` and `me` on hidden fields: refused exactly as an unknown field.
  - Words: matched against the fields she sees, so "sam" (the assignee) finds nothing.
  - History: the event kinds, the values shown, and the cause references.
  - Rollups: unlisted ones are not computed, and listed ones count only what she sees.
  - `GetType` is open by design.
  - Hidden and missing Items give the same bytes on get, list, events and create-inside, and on a
    search by key.
  - Refusals carry no metadata, apart from `FIELD_NOT_WRITABLE` naming a field of a public Type.
  - The answers to writes (`CreateItem`, `TransitionItem`, link) are read back through `Get`, so they
    are projected.
- **Writes.**
  - Containers that admit nothing, hidden or narrowed containers, a Type that is not admitted,
    nesting deeper than one level, the top level, narrowing at create, hidden fields at create: each
    is refused, with `NOT_FOUND` before `NOT_ALLOWED`, so nothing hidden is confirmed.
  - Updates, moves, policy changes and links on a hidden Item answer `NOT_FOUND`; on a visible one,
    `NOT_ALLOWED`.
  - Transitions: an empty `allowed`, a role, and a `principal_roles` row added by hand for a customer
    are all refused.
  - `requester`: from an agent, from a customer, naming a non-customer, on a Type the container does
    not own, and with a hidden or non-existent customer (same bytes).
  - Idempotency across two people one agent acts for, for both staff and customers.
  - Sessions and grants for a customer: the fixed set of powers, enforced in the kernel and in the
    dev-token file.
  - Staff moving and narrowing between two reads by kim: the next read follows.
- **Earlier steps.**
  - `created_inside` reactions with `by: creator` through a portal bot: the bot counts as the
    requester.
  - The kernel's own actors carry a customer in `on_behalf_of` for attribution, but are never treated
    as customers (`CustomerOf`).
  - SLA goals and breach mail: a customer is never a principal field's value. That includes a set
    operation on a multi-value principal field (checked, holds).
  - A customer can be a reader of a story but still can't see it.
  - Narrowed and inherited scopes.
  - Other namespaces: every relation and predicate is bounded by the namespace, or by the container,
    which is in the same namespace.
- **Staff regressions.**
  - For staff, `seeArgs` sends an empty customer and empty arrays, so the customer condition is
    constant true. `pageSQL` adds nothing for staff, and the Postgres 17 plans in the journal are
    unchanged for them.
  - `authorize`'s shortcut for an empty `allowed` still applies to staff.
  - The new key for agents: one person, one key, the same answer, and old rows were backfilled.
  - `Project` with or without a Type gives staff the whole Item; the step's policy tests cover this.
  - Agents acting for users: unchanged.
  - The whole suite was green under `-race` in the step itself. Nothing found.

## What the customer property test never generates

`TestProperty_CustomerRule_GetSearchAndListMatchTheEvaluator`. Its generator never produces:

- **Agents** acting for a customer. The predicate and the relation are only checked for
  `customer:` actors, not for `agent:` + `on_behalf_of`.
- **Links and rollups.** It never checks them at all (findings 1, 2 and 5 live there).
- **Narrowing or moving a reply or a note.** Only portals and incidents are narrowed. Incidents move
  only to portals, the old desk or the space; never into another incident, nor below the top level.
- **Children created by customers through an agent,** or comments and notes written by a customer.
  Those are refused, so it does not check that the refusal has no side effect.
- **Old Type versions** in a portal (an `incident@1` there via staff or a requester), or a Type
  version with no `customer_fields`.
- **More than one namespace.** Every principal is in one namespace, so cross-namespace isolation is not
  part of the property.
- **Search with words, field filters or `order_by`, and List with any filter.** It only searches with
  the plain Type filter, paged two at a time.
- **History.**
- **Concurrent changes.** The steps run one at a time; the scope property test does run concurrent
  changes.

Worth adding first: agents for customers; link conditions and `Get` links, against an evaluator that
knows `customer_fields`; and words, against an evaluator over the customer fields.
