# Prior art: a fast transactional clerk in front, durable queues and timers behind

2026-11-25. The question: is the kernel's high-level design new? In one
sentence, that design is **short local transactions on one Item at a time (the
item service, "the clerk"), with everything long-running (waits, timers,
reactions between Items, calls to other systems, compensation) carried by
durable, retried, idempotent messages and workflows, instead of long or
distributed transactions.**

**Short answer: no. It is one of the oldest ideas in transaction processing,
rediscovered in every generation.** What the kernel adds is elsewhere (last
section).

## The lineage

| When | What | The same idea |
|---|---|---|
| 1970s on | IBM CICS pseudo-conversational transactions, later with IBM MQ | A conversation with a person is a chain of short transactions; nothing is held while the person thinks. ([IBM](https://ibm.com/support/knowledgecenter/en/SSGMCP_5.3.0/com.ibm.cics.ts.applicationprogramming.doc/topics/dfhp3_concepts_conversation.html)) |
| 1987 | Sagas (Garcia-Molina, Salem) | A long transaction as a sequence of local ones, with compensations instead of a rollback. |
| 1991 | Dayal, Hsu, Ladin: "A Transactional Model for Long-Running Activities" (DEC) | Each step a transaction, steps chained reliably through **recoverable queues**; its own example is a request a manager approves and an accountant books. ([VLDB 1991](https://www.vldb.org/conf/1991/P113.PDF)) |
| 1990 to 1992 | ConTracts (Reuter, Wächter) | A long-lived computation as a script of transactional steps: sequence, fork and join, forward recovery after a crash, compensation steps. ([Encyclopedia of Database Systems](https://link.springer.com/referenceworkentry/10.1007/978-1-4899-7993-3_727-2)) |
| 2007 | Pat Helland, "Life beyond Distributed Transactions" | Transactions within an entity, never across; entities talk through asynchronous messages; handling must be idempotent; "activities" keep the per-partner state; workflow manages the uncertainty. ([CIDR 2007](https://ics.uci.edu/~cs223/papers/cidr07p15), [ACM Queue](https://queue.acm.org/detail.cfm?id=3025012)) |
| 2010s | NServiceBus sagas, timeouts and outbox; the transactional outbox pattern | Long-running processes as stateful message handlers, time as delayed messages, state and outgoing messages in one local transaction. ([Particular](https://docs.particular.net/nservicebus/sagas/)) |
| 2010s on | Actors with persistence (Akka, Orleans), Cloudflare Durable Objects | One stateful entity per key, one writer at a time. |
| 2019 on | Durable execution: Temporal's entity workflow; Restate's virtual objects | One durable, retried workflow or object per entity; Restate: "only one handler can run at a time" per key. ([Restate](https://restate.dev/programming-model)) |

Helland's entities and activities are the closest single description of the
kernel's shape: the Item is his entity, the outbox and the per-Item workflow
carry his messages and activities, and idempotency keys give his
"natural idempotence".

## Products nearest the whole kernel (pages read, not used)

- **Agent runtimes with durable state and human approval:** LangGraph (a
  state graph, checkpointed, interrupts for approval), Orkes Conductor
  (durable agents, human approval), Kestra (declarative YAML workflows with
  approvals). Each governs a *run* (an agent's or a process's steps), not
  work items people and agents share.
- **Durable-execution platforms:** Temporal, Restate, DBOS, Inngest, Hatchet,
  Dapr: the durability half, as code. No declarative rules for who may do
  what, no inbox.
- **Task pools and inboxes:** Holunda's Polyflow, TASKANA/KADAI, OpenNSW's
  taskflow (`task-inboxes-taskana-polyflow.md`): the queue half, with the
  process logic elsewhere.
- **Agent governance on Temporal:** Cognitive Fab's polyflow, Temporal's own
  agent work (`agent-governance-cognitive-fab-polyflow.md`): rules over an
  agent's steps.

None of those read combines all five points below. "Not found" is not "does
not exist": internal platforms at large companies may.

## What the kernel adds

The architecture is the classic one; the kernel's own part is what it puts
on top of it:

1. **A declarative, versioned rulebook for work items** (states, who may act,
   frozen fields, guards, hooks, reactions), not for process steps.
2. **One judge for every change,** from a person, an agent, a timer or a
   reaction, in one local transaction.
3. **Durability per Item for everything around the judge,** each step a
   request the judge re-checks (the effect re-check, PR #71).
4. **Agents as actors under the same rules,** fields closed to them by
   default, gates only people pass.
5. **Authoring for non-developers,** checked, read back and tested before it
   runs.

Building on a forty-year-old, well-understood architecture is a strength for
a system that must be trusted: the novelty is in what is easy and what is
safe, which is what the bake-offs measure.
