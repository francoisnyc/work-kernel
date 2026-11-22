# An event stream from the write-ahead log, instead of a post-commit sequencer

Date: 2026-11-22. Written from web research; a claim that rests only on a
vendor's own page is marked as such, and anything not confirmed on a fetched
page is marked UNVERIFIED. `docs/design.md` is the spec: this report feeds a
decision it has not made yet.

## The question

`docs/design.md` ("Write path", "What that rules out") plans a **post-commit
sequencer**: one background process that reads events whose transaction ID is
below the snapshot's xmin, orders them, and stamps a gap-free position, so
that Watch, indexers and webhooks have a cursor meaning "nothing before this
point is missing". `commit_seq` cannot be that cursor, because transactions
take their number in one order and commit in another.

Separately, the relay (`internal/relay`) is a polling publisher: a single
process that every 200 ms delivers each Item's outbox rows to Temporal, one
Item at a time (see the journal's Later list for its known limits).

**Is there an existing, standard way to get an ordered, complete stream of
committed events out of Postgres, so the kernel does not build its own
sequencer and polling?**

## Short answer

Yes. Postgres **logical replication** (logical decoding) streams committed
changes **in commit order**, and a consumer resumes from the position it last
confirmed. That is the cursor the sequencer was designed to produce. It is the
mechanism behind the standard tools for the transactional outbox pattern
(Debezium, Sequin) and is available in Go through `jackc/pglogrepl`, built on
the same `pgx` the kernel already uses.

What no tool provides, and stays the kernel's job: deciding **who may see each
event** (`ListEvents`' projection through `policy.Project`).

## What the kernel already is, in pattern terms

| Kernel piece | Standard name |
| --- | --- |
| Item row, event and outbox row in one transaction | Transactional outbox |
| `internal/relay` reading the outbox every 200 ms | Polling publisher (the outbox's relay) |
| `item_events` beside the current Item row | An event log beside current state |
| Update ID `item:seq`, duplicates ignored | Idempotent consumer |
| Held reaction and its undo | Saga with compensation |
| One Temporal workflow per Item | Entity workflow (durable execution) |
| The planned sequencer | Change data capture (CDC), log-based |

The ordering problem behind the sequencer is the known weakness of a polled
outbox: with transactions running in parallel, the order of rows by sequence
or timestamp need not be the order in which they commit (Morling, "Revisiting
the outbox pattern"). Log-based CDC is the usual answer, and is recommended as
the preferred way to read an outbox whenever it is possible (same source).

## Findings

### Logical decoding gives commit order

- Postgres streams changes **in commit order**, not in the order they were
  made; within a transaction, changes come in LSN order (Andres Freund on
  pgsql-hackers; interdb.jp on the reorder buffer).
- The commit's position (its LSN) is what a consumer confirms. Only commit
  positions are guaranteed to increase across the stream; positions of the
  individual changes inside concurrent transactions are not (Toggl, building
  a Go consumer on `pglogrepl`: the first transaction to start can be the last
  streamed). **So the cursor is the commit position**, which is what the
  sequencer section asks for: "nothing before this point is missing".
- Positions are ordered, not consecutive integers. If a consumer needs dense
  numbers, the single process reading the stream can stamp them, trivially,
  because it already sees events in final order.

### What to stream: `item_events`, never the values

A publication names the tables it streams. Streaming **`item_events` only**
matches the kernel's rule that payloads carry the operation's shape and opaque
IDs, never field values ("Events and personal data"). Values stay in
`item_event_values` and reach a reader only through `ListEvents` and
`Project`, as `design.md` already requires of "any future event path (a watch,
a stream, a webhook)".

Postgres can also write a message straight into the WAL inside a transaction,
`pg_logical_emit_message()`, with no table at all; tools like Debezium read
those messages (InfoQ; SeatGeek, who load-tested it on a critical path). The
kernel needs the `item_events` table anyway, for history, so streaming its
inserts is the simpler fit; emitting messages would only matter if the outbox
table itself were to go.

### The operational cost: replication slots

- A slot keeps WAL until its consumer confirms it. A consumer that stops
  confirming makes WAL pile up; Debezium's own failure mode is a slot that
  fills up and can crash the database (Sequin's write-up of Debezium).
- `max_slot_wal_keep_size` (Postgres 13+) bounds this: past the limit,
  Postgres invalidates the slot and drops the old WAL (Morling, "Mastering
  Postgres replication slots"). The kernel then needs a **resync path**:
  re-read `item_events` from the last delivered event and resume. Reports of
  slots marked lost even with the limit unset exist on the mailing lists, so
  the resync path is needed regardless.
- **Failover.** Before Postgres 17, a standby did not know the primary's
  logical slots, so a failover reset the stream. Postgres 17 adds failover
  slots, synchronised to a standby (`failover = true`, `sync_replication_slots`,
  `synchronized_standby_slots`) (PostgreSQL 17 docs; Morling; AWS RDS docs).
  The stack already requires Postgres 17+.
- Managed Postgres must allow logical replication (it does on RDS, with
  settings; others UNVERIFIED).

## Options

| | A. In process, `pglogrepl` | B. Sequin, as a service | C. Debezium Server | D. The sequencer as designed |
| --- | --- | --- | --- | --- |
| What it is | A Go library for the replication protocol, on `pgx/v5` | A CDC service streaming Postgres changes to Kafka, SQS, NATS, Redis, HTTP and webhooks; MIT | The long-standing CDC tool, built around Kafka; has an outbox event router | A kernel process polling `item_events` below xmin |
| Order and completeness | Commit order, resume from confirmed position; the kernel writes the client | Its docs claim strict ordering and guaranteed delivery (vendor claim) | Strict ordering through logical replication | Correct by design, but it is the hand-built version |
| New moving parts | One dependency, no new service | A service with its own console and state | A Java service, usually Kafka | None |
| Fits `CLAUDE.md` | Standard library first; asks for one dependency | A second runtime to operate | Heaviest | Fully in house |
| Risk | Writing a correct consumer (commit positions, keepalives, slot handling) | Another system in the trust boundary | Operational weight | Polling latency; reinventing ordering |
| Delivery to others | The kernel's own (Watch, relay); a broker later if needed | Built in (webhooks, NATS, Kafka) | Through Kafka | The kernel's own |

## Recommendation

1. **Do not build the xmin sequencer.** Replace it in `design.md` with "a
   logical replication consumer of `item_events`": same guarantee, standard
   mechanism.
2. **Option A first**: one kernel process, the **event reader**, on
   `pglogrepl`, streaming `item_events` inserts in commit order, confirming
   its position after each batch is handed on, with a resync path from
   `item_events` when its slot is invalidated. It feeds:
   - **the relay**, replacing the 200 ms poll (the outbox can then shrink to
     "which Items have listeners", or go);
   - **Watch**, when built: each event passed through `ListEvents`' projection
     per audience (grouped by scope, not per subscriber);
   - later, a broker (NATS JetStream or Kafka) if outside consumers need one.
3. **Keep Option B in reserve** for when outside systems (a SIEM, a data
   warehouse, a customer's webhooks) need delivery the kernel should not
   build: it could read the same `item_events` table without touching the
   kernel.
4. **Before deciding, a spike**: a `pglogrepl` consumer of `item_events` on
   the dev stack, measured with `cmd/bench` (events per second, lag from commit
   to delivery), with a forced slot invalidation and resync, and a failover
   left for later.

The relay's other limits (one Item at a time, a single instance) are
independent of this and can be fixed first: deliver in parallel per Item,
wake on `LISTEN/NOTIFY` as an interim step, several relays with
`FOR UPDATE SKIP LOCKED` (journal, Later).

## Open questions for the owner

1. Adding `jackc/pglogrepl` is a new dependency (`CLAUDE.md`, "Ask before").
2. Is `wal_level = logical` acceptable for every place the kernel will run?
3. Does Watch's first version need outside delivery (option B), or only the
   kernel's own API?

### Answers so far (2026-11-22)

- **Its own Postgres:** yes. The kernel runs on its own native (self-managed)
  Postgres instance, so `wal_level = logical` is a setting in
  `postgresql.conf` and a restart, not a provider flag.
- **`wal_level = logical` on that instance:** to be confirmed at work.
- **An existing Postgres to BigQuery pipeline:** yes. Such pipelines usually
  read the replication stream (Datastream and Debezium both do), so logical
  decoding is likely already accepted elsewhere; which tool it uses is the
  next thing to ask. BigQuery is a destination for analytics, never the
  kernel's own store: its mutating transactions take table-level locks, where
  the kernel locks one Item's row per write.

## Sources

- Gunnar Morling, "Revisiting the outbox pattern": https://www.morling.dev/blog/revisiting-the-outbox-pattern/
- Gunnar Morling, "Mastering Postgres replication slots": https://morling.dev/blog/mastering-postgres-replication-slots
- Gunnar Morling, "Failover replication slots with Postgres 17": https://www.morling.dev/blog/failover-replication-slots-with-postgres-17/
- PostgreSQL 17 docs, logical replication failover: https://www.postgresql.org/docs/17/logical-replication-failover.html
- Andres Freund on commit order, pgsql-hackers: https://www.postgresql.org/message-id/20230731203927.6nurjhw5ltisfhc2%40awork3.anarazel.de
- interdb.jp, transaction reassembly: https://www.interdb.jp/pg/pgsql12/06.html
- Toggl, a journey into Postgres logical replication (Go, `pglogrepl`): https://engineering.toggl.com/blog/journey-into-postgresql-logical-replication
- `jackc/pglogrepl`: https://pkg.go.dev/github.com/jackc/pglogrepl
- Sequin: https://github.com/sequinstream/sequin
- Sequin on how Debezium captures changes: https://dev.to/sequin/how-debezium-captures-changes-from-postgres-35bn
- InfoQ, Postgres logical decoding messages: https://www.infoq.com/articles/wonders-of-postgres-logical-decoding-messages
- SeatGeek, the transactional outbox pattern: https://chairnerd.seatgeek.com/transactional-outbox-pattern
- AWS RDS, logical slot synchronisation: https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/Appendix.PostgreSQL.CommonDBATasks.pglogical.slot.synchronization.html
