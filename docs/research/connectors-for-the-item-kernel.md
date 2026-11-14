# Bind connectors to lifecycle, never to commits

The kernel's connector model should copy Bonita's lifecycle shape, Camunda's inbound discipline, the iPaaS split between code and connection, and the trackers' link record. It should then add two things no surveyed product ships: lineage, which records which event caused which, and connector-aware policy. Concretely, a **Connector** is versioned code plus a manifest of **actions** (outbound calls) and **sources** (inbound feeds). A **Connection** is a tenant-owned, revocable credential with an owner. A Type version contains **hooks** that bind an action to `on_enter`, `on_exit` or `on_complete`. It also contains **intakes** that bind a source to create or correlate Items, and **syncs** that pair both through an **external link** record. Nothing runs inside the write transaction. Bonita already proves the pattern: it commits the state, runs each connector outside any transaction, and saves each connector's output in its own transaction ([Bonita](https://documentation.ofelia.com/bonita/2026.1/runtime/execution-sequence-states-and-transactions)). That maps one-to-one onto the kernel's sequence: the operation commits with an outbox row, a Temporal activity runs on the Item's workflow, and a second kernel operation writes back the outputs. A hook that must finish before work proceeds should place a visible **hold** on the Item. A hold is the approval-completed-by-token mechanism the kernel already needs, with a connector run as the approver. Failure policy is per hook (fail, ignore, raise), and a failure lands as an incident with retry, skip and replay. Causation is exact inside the kernel. It reaches external systems only where they echo a marker back, such as Salesforce's `changeOrigin` or MCP's `_meta.traceparent`. Everywhere else, echo suppression has to come from link records and hashes of the last value the kernel wrote. The largest risks are not technical. One stolen connected-app token reached hundreds of Salesforce orgs in 2025, and connector-delivered text is the main prompt-injection channel into agents. Several vendor details below, such as Camunda listeners, Exalate internals, Stripe idempotency and some MCP products, could not be fetched and are flagged **UNVERIFIED**.

**How to read the evidence.** Unmarked claims come from pages a researcher fetched and read. **UNVERIFIED** marks claims from background knowledge or search-result titles only. The linked page is the right place to confirm them, but nobody re-read it. The recommendations are the report's own synthesis. No vendor validates the kernel-specific choices, such as the visible hold, lineage-based echo suppression, or the Type-scoped endpoint.

## Four product families split connectors the same three ways

Every family separates three things. The first is the **code** that talks to the external system. The second is the **authenticated instance** that holds credentials. The third is the **binding** that says when the code runs and where its results go. Only the names differ, and getting the vocabulary right matters because "connector" means the package in one product and the configured instance in another.

**BPM engines attach connectors to the lifecycle.** Bonita puts connectors on pools or tasks, as "connector in" at the start or "connector out" at the end ([Bonita connectivity overview](https://documentation.ofelia.com/bonita/next/process/connectivity-overview)). It also has the cleanest packaging split. A **definition** declares inputs, outputs and the configuration wizard. An **implementation** is Java code, and one definition can have any number of them. A **configuration** records how a specific process uses the connector, including which implementation it picks ([Bonita connectivity overview](https://documentation.ofelia.com/bonita/next/process/connectivity-overview)). In the Enterprise edition, an implementation can be swapped at runtime if it implements the same definition ([Bonita connector archetype](https://documentation.ofelia.com/bonita/2025.1/process/connector-archetype)). Camunda 8 treats connectors as activities rather than hooks. Outbound connectors are applied to a task or event through an element template ([Camunda outbound](https://docs.camunda.io/docs/components/connectors/use-connectors/outbound)). Inbound connectors are catch events: start, message start, intermediate, boundary, or receive task ([Camunda inbound](https://docs.camunda.io/docs/components/connectors/use-connectors/inbound)). Camunda, Flowable and Appian have no on-enter or on-exit connector hook on a human task. To get "connector in", you model a service task in front of the user task. Camunda 8's execution listeners and user task listeners come closest. They reportedly run as blocking remote jobs that hold the element until a worker answers, but that detail is **UNVERIFIED** in this round.

**iPaaS and data platforms converge on a four-part manifest.** Airbyte states it most formally. Every actor implements `spec` (declare its configuration) and `check` (test connectivity). Sources add `discover` and `read`, emitting RECORD and STATE messages. Destinations implement `write` ([Airbyte protocol](https://docs.airbyte.com/platform/understanding-airbyte/airbyte-protocol)). Workato's SDK splits triggers into `poll`, `closure` (the cursor), `dedup` (a signature per record) and webhook subscribe and unsubscribe hooks, with required input and output fields ([Workato SDK triggers](https://docs.workato.com/developing-connectors/sdk/sdk-reference/triggers.html)). Pipedream components are either sources, which emit events and get lifecycle hooks such as `activate` and `deactivate`, or actions, which return data ([Pipedream Component API](https://pipedream.com/docs/components/contributing/api.md)). MuleSoft has a reusable global configuration element for credentials, plus operations and optional sources ([MuleSoft](https://docs.mulesoft.com/connectors/introduction/intro-connector-configuration-overview)). Dapr bindings carry an explicit `direction` of input, output or both ([Dapr bindings API](https://docs.dapr.io/reference/api/bindings_api)).

**Trackers and ITSM add the link record.** Each product splits connectivity into four pieces: a credential or endpoint object kept apart from logic, outbound events, inbound APIs, and a record pairing a local item with a remote ID. Salesforce separates a **named credential** (the endpoint) from an **external credential** (how to authenticate). It also names a **principal**: either a shared named principal or per-user, granted through permission sets ([Salesforce Named Credentials glossary](https://developer.salesforce.com/docs/platform/named-credentials/references/named-credentials-reference/nc-glossary.html)). ShareLogic Unifi, a ServiceNow-native eBonding product, keeps a **Bond** per linked ticket. The Bond holds internal and external references, a state that gates messages, a health flag, an owner flag for which system controls the ticket, and full transaction history ([Unifi docs](https://docs.sharelogic.com/unifi/4.1/operate/bonding/bonds)). Linear stores the link to a GitHub issue as an **attachment**, and removing the attachment stops sync ([Linear docs mirror](https://linear-web.pages.dev/docs/github)). ServiceNow's IntegrationHub spokes, its connection-and-credential aliases, and the on-prem MID Server are **UNVERIFIED** because the ServiceNow docs pages render only in a browser.

**Agent-era platforms key everything to the end user.** Composio, Pipedream Connect and Merge all keep per-user connected accounts, indexed by the embedding product's own user ID. The platform runs OAuth, stores and refreshes tokens, and returns a connect link mid-conversation when a tool call has no connection ([Composio](https://docs.composio.dev/tool-router/users-and-sessions); [Pipedream Connect MCP](https://pipedream.com/docs/connect/mcp/developers.md)). Merge's Agent Handler groups tools into a **Tool Pack** per agent job. It sets credential scope per connector as Individual, Shared or Organization, and its advice is "If you're unsure, pick Individual" ([Merge tool packs](https://docs.merge.dev/merge-agent-handler/build/tools/tool-packs)). Every one of these platforms also exposes its catalog over MCP.

The vocabulary maps cleanly across families.

| Kernel term (proposed) | What it is | Bonita | Camunda 8 | iPaaS | Trackers, ITSM | Agent platforms |
|---|---|---|---|---|---|---|
| **Connector definition** | Versioned contract: config schema, auth kind, actions, sources, error codes | Definition (.def) | Element template + job type | Airbyte `spec`; Workato connector; Pipedream app | IntegrationHub spoke (UNVERIFIED) | Toolkit; MCP server |
| **Connector implementation** | Code that fulfils a definition major version | Implementation (.impl + class) | Connector runtime function | Airbyte image; Zapier app; Nango function | Spoke actions (UNVERIFIED) | Tool code; MCP tool |
| **Connection** | Authenticated, revocable instance with an owner | Connector inputs or parameters (secrets handling UNVERIFIED) | Secrets resolved by runtime (syntax UNVERIFIED) | Nango connection; Pipedream account; Merge Linked Account; MuleSoft config element | Salesforce named + external credential; ServiceNow alias (UNVERIFIED) | Connected account; credential scope |
| **Action** | Outbound call: query or modify | Connector "in" or "out" | Outbound connector | Action, operation, create, search, sink | REST message, spoke action | Tool |
| **Source** | Inbound feed: webhook, poll, stream, email | Email Reader, Kafka (beta) | Inbound connector | Trigger, source, stream, input binding | Webhook, mail handler, Import Set | Webhook trigger |
| **Hook** | Binding of an action to a lifecycle point in a Type version | Connector configuration on task or pool | Service task or listener | Recipe or Zap step | Business rule, flow | n/a |
| **Intake** | Binding of a source to create or correlate Items | Message correlation (UNVERIFIED) | Inbound element + correlation | Zap trigger | Inbound email action, transform map | n/a |
| **External link** | Item ↔ remote record pairing with sync state | none | none | Merge `remote_id`; Getint item map | Bond; `correlation_id`; Linear attachment | n/a |

Two naming notes follow from the earlier extension-point research. The kernel already uses "link" for Item-to-Item relations, so the sync record must be called an **external link**, never a bare link. The bare word "trigger" means incompatible things across products, so read-side units should be called **sources**. That matches the earlier vocabulary of *guard, check, derive, review, reaction, subscription, source, schedule*. A hook is a **reaction** whose effect is a connector action and whose outputs are written back.

## Bonita's in and out hooks map onto the outbox, one transaction per step

### Attach points: enter, exit and complete

Bonita's ordering is the precedent for the founder's framing. User task initialization sets default values, runs actor filters, **runs the on-enter connectors**, and evaluates the dynamic name. Only then does the task reach READY and wait for a user ([Bonita 2026.1 execution sequence](https://documentation.ofelia.com/bonita/2026.1/runtime/execution-sequence-states-and-transactions)). So the task is not in anyone's inbox until every on-enter connector has finished. Bonita does not show this gap to users. It appears only as a delay. Task execution validates the submission against the contract, runs operations, **runs the on-finish connectors**, and then completes the task. A task "connector out" can therefore use the user's form input ([Bonita connectivity overview](https://documentation.ofelia.com/bonita/next/process/connectivity-overview)). Pool connectors run when the instance starts and when it terminates "whatever the reason" ([Bonita connectivity overview](https://documentation.ofelia.com/bonita/next/process/connectivity-overview)).

The kernel should have three attach points, plus Item-level variants.

| Attach point | Fires when | Typical use | Bonita analogue |
|---|---|---|---|
| `on_enter(state)` | The transition into the state has committed | Fetch data that work in the state needs ("connector in") | Task connector in |
| `on_exit(state)` | A transition out of the state has been requested and its submission committed, before the target state is entered | Push data that must exist before the next state ("connector out" as a precondition) | Task connector out, before COMPLETED |
| `on_complete(step)` | A waiting step (an approval or a stage task) is completed by its token | Push the outcome of a decision, such as invoicing an approved request | Task connector out using form input |
| `on_create`, `on_close` | Item created; Item enters a done or canceled category | Provision or tear down external records | Pool in and out |

`on_complete` is distinct from `on_exit` because the kernel's approvals and stage tasks are waiting steps completed by a token, not states. An approval can complete without the Item leaving its state, and the invoice should follow the approval, not the state change.

### Blocking hooks hold the Item; Bonita's invisible gap should become visible

Each hook declares `blocking: true | false`. A non-blocking hook is an ordinary post-commit reaction. Its failure never stops the Item. A blocking hook places a **hold** on the Item, which works like a substate without adding states to the Type's graph. While an `on_enter` hold is open, the Item is in state S but not *available*. It is not shown in queues, not assignable, and transitions out of S are refused except the Type's declared escape transitions, such as cancel. An `on_exit` hold keeps the Item in S with the requested target recorded, and the second transaction enters the target only when the hook succeeds. Bonita has exactly this ordering for task connectors but no visible state for it. No surveyed vendor documents a user-visible "connector pending" substate on human tasks, so this is a design choice no source validates.

The kernel already has the machinery. The earlier research modeled approvals as waiting steps completed by a token, held on a Temporal wait with an SLA and an audit trail. It also proposed modeling remote allow, deny or correct checks as "an approval completed by a machine." A blocking hook is the same object. The hold is the waiting step, the connector run holds the token, and success, failure or timeout completes it. One mechanism then serves human approvals, agent approvals, remote checks and blocking connectors. The UI shows "Preparing: fetching customer plan from CRM" instead of an unexplained delay.

| Option for blocking behavior | How it works | Precedent | Gains | Costs |
|---|---|---|---|---|
| **A. Never block** | All hooks are post-commit reactions; outputs arrive whenever they arrive | Camunda outbound jobs; iPaaS recipes | Simplest; no stuck Items | Users act on Items with missing data; SLA and routing can run on blanks |
| **B. Invisible gate** | Item is unavailable until on-enter hooks finish; no state shown | Bonita task init | Faithful to Bonita | Silent delays; stuck Items look like bugs |
| **C. Visible hold per blocking hook (recommended)** | Hold reuses the approval-token wait; shown in UI; escape transitions cancel the run | Bonita ordering + Camunda 8 blocking listeners (UNVERIFIED) + kernel approvals | One mechanism for every wait; explainable; incidents attach to the hold | Two transactions per hook; must decide whether SLA clocks run during holds |
| **D. Model fetching as an explicit state** | Type author adds a "Enriching" state with an automatic transition | Camunda service task before user task | No new concept | Pollutes state graphs and categories; every Type re-invents it |

A cancelled hold creates a race. The connector result can arrive after the Item has moved on. Every write-back must therefore carry the hold's `run_id`. The kernel discards a result whose hold no longer exists, and it records the discard as an event instead of applying stale data.

### Execution never enters the write transaction

Bonita states the rule directly. The engine "commits the transaction, then submits connector execution as work." Connectors run outside any transaction so long runs do not hold locks, and **each connector's output is saved in its own transaction**. A state with connectors therefore needs at least three transactions: one before execution, one per connector, and one to finish the state ([Bonita 2026.1](https://documentation.ofelia.com/bonita/2026.1/runtime/execution-sequence-states-and-transactions)). Connectors run in declaration order on a dedicated thread pool ([Bonita connectors execution](https://documentation.ofelia.com/bonita/next/runtime/connectors-execution)). Flowable shows the alternative. Its HTTP task can run inline, where a failing status code "rolls back the current database transaction" ([Flowable HTTP task](https://documentation.flowable.com/latest/model/bpmn/reference/http-task)). Pega shows why the alternative is dangerous. A connector failure inside a data page propagated FAIL to its callers and could roll back the current interaction, so the fix was a transform that clears the step status ([Pega support article](https://community.pega.com/support/support-articles/data-page-throwing-connectorexception-propagates-fail-callers)).

The kernel sequence follows Bonita:

1. **Transaction 1.** The kernel commits the operation (transition, approval completion, create), the event, and an outbox row. For a blocking hook, it also writes the hold. A `hook_run` row records the hook ID, the causing event, an idempotency key, and status `pending`.
2. **Relay.** The per-Item relay delivers the event to `item-<id>` through Update-With-Start, as already designed.
3. **Activity.** The Item workflow schedules the action as a Temporal activity on the connector's own task queue. The activity asks the kernel for its input through `Project(hook_principal, item, purpose=connector:<def>)`. This keeps Temporal payloads ID-only, and it means a connector can never send a field its principal cannot read. The activity resolves the Connection's credential just in time and calls the external system with the idempotency key.
4. **Transaction 2.** The workflow submits the result to the kernel as a normal `update` (or `transition`, for an exit hold). The write carries full attribution. It passes through CEL checks like any other write, and it releases the hold.

Blocking hooks run sequentially in declaration order, as in Bonita. Parallel execution is an explicit opt-in. Outputs are written only on success, so a failing connector cannot leave half-written state.

Delivery is at least once by construction. Camunda warns that if no result arrives within the job timeout, Zeebe reassigns the job, "so a long call can run twice" ([Camunda outbound](https://docs.camunda.io/docs/components/connectors/use-connectors/outbound)). Bonita retries automatically when it cannot even mark a connector failed, and so it recommends idempotent implementations ([Bonita connectors execution](https://documentation.ofelia.com/bonita/next/runtime/connectors-execution)). The kernel should derive the idempotency key as `hash(tenant, hook_id, causation_event_id)`. It should pass that key to APIs that accept one, and keep an action-attempt row so Temporal retries dedupe on the kernel side. None of the surveyed iPaaS platforms were verified to provide platform-level idempotency keys for actions. Stripe's idempotency-key semantics are the canonical model: client-generated keys, kept for at least 24 hours, with the first response replayed. They are **UNVERIFIED** because docs.stripe.com could not be fetched. For APIs without idempotency, the fallback is to search before creating, using a kernel reference stored in a field on the remote record.

### Failure policies: fail, ignore, raise, plus incidents and replay

Bonita gives every connector one of three options. **FAIL** sends the task to FAILED and stops the instance. **IGNORE** completes the task normally. **THROW ERROR** aborts the task and throws a BPMN error event ([Bonita connectivity overview](https://documentation.ofelia.com/bonita/next/process/connectivity-overview)). Failed connectors can be inspected through the ConnectorFailure API and replayed manually through `activityReplay` ([Bonita connectors execution](https://documentation.ofelia.com/bonita/next/runtime/connectors-execution)). The Subscription edition applies a default 5-minute timeout. Bonita also documents a sharp edge: a thread that cannot be interrupted, such as an infinite Groovy loop, releases the task but keeps its executor busy, so that executor can run no other connectors ([Bonita connectors execution](https://documentation.ofelia.com/bonita/next/runtime/connectors-execution)). Flowable separates **handle** status codes, which become a catchable BPMN error, from **fail** status codes, which raise a technical exception ([Flowable](https://documentation.flowable.com/latest/model/bpmn/reference/http-task)). Camunda defaults to 3 retries with no delay, sets backoff through a `retryBackoff` header ([Camunda outbound](https://docs.camunda.io/docs/components/connectors/use-connectors/outbound)), and reportedly raises an incident when retries run out (incident details **UNVERIFIED**). Airbyte classifies errors as `system_error` or `config_error` ([Airbyte protocol](https://docs.airbyte.com/platform/understanding-airbyte/airbyte-protocol)). Kafka Connect's dead-letter queue covers only conversion and transformation failures, and "there is no dead letter queue for source connectors" ([Confluent](https://www.confluent.io/pt-br/blog/kafka-connect-deep-dive-error-handling-dead-letter-queues/)).

The kernel should combine these into two axes. The first is error class, which the implementation reports. The second is policy, which the Type author declares per hook.

| Error class (from the implementation) | Kernel behavior before policy applies |
|---|---|
| `transient` (5xx, network) | Temporal retry with exponential backoff, up to `retries` |
| `rate_limited` (429, quota) | Honor `Retry-After`; per-Connection token bucket; circuit breaker that pauses the Connection rather than hammering. Blind 429 retries just exhaust the quota again ([Truto](https://truto.one/blog/how-to-prevent-infinite-loops-in-bidirectional-api-syncs-a-developers-cookbook/)) |
| `config` (401, 403, missing scope) | No retry; Connection goes to `needs_reauth`; owner notified |
| `business` (a declared error code, such as "customer not found") | No retry; goes straight to policy |
| `timeout` | Treated as transient, but recorded as "may have executed", because the call might have succeeded |

| Policy (per hook) | Effect once retries are exhausted | When to use |
|---|---|---|
| `fail` (default for blocking hooks) | Hold becomes an **incident**: Item stays held, owner and Type admins notified; operations `retry` (same idempotency key), `skip` (release hold, record skipped), `rerun` (new key, explicit and audited) | Connectors that are true preconditions |
| `ignore` | Record a `hook_failed` event, release the hold, continue | Enrichment that has a safe default; **never** for outbound pushes, where it causes silent drift |
| `raise(code → transition)` | Route the Item along a Type-declared error transition | Business failures such as credit refused or customer unknown |
| `degrade(field := value)` | Like `ignore`, but write a declared fallback value | A blank CRM plan becomes `unknown`, so CEL routing still works |

Two refinements come from the sync research. A failure in two-way sync should land on the **external link's** health, not on the Item's business state, so a Jira outage does not block a helpdesk ticket. And `retry` versus `rerun` must stay distinct. Retrying with the same key is safe. A rerun with a new key can create a second invoice, so it must be an explicit, audited choice.

## Packaging separates code, contract, configuration and credentials

### Definition, implementation, configuration

The kernel should adopt Bonita's three-way split, Camunda's code-version pinning, and Airbyte's narrow message contract. A **definition** (`erp.invoices@2`) carries the config schema, auth kind, actions and sources, plus their input and output schemas and declared error codes. An **implementation** (`erp.invoices.netsuite@2.3.1`) declares which definition major version it fulfils. **Configuration** lives in the Type version. It names the hook point, connection slot, input mapping (CEL), output mapping, failure policy, retries, timeout and blocking flag, and it references secrets only through the Connection. This is the earlier rule "a Type is versioned data that names code," applied to connectors. The earlier research found that Camunda job types such as `io.camunda:http-json:1` carry a code version (**UNVERIFIED**). It also found that pinning means nothing if referenced code changes meaning, so **the Type pins the definition's major version and the implementation floats within it**.

Airbyte's CONTROL `CONNECTOR_CONFIG` message is worth copying for credentials. A connector can hand back updated configuration, such as a rotated token, and the host merges and persists it ([Airbyte protocol](https://docs.airbyte.com/platform/understanding-airbyte/airbyte-protocol)). Connector code therefore never writes secrets itself.

| Option for who writes connectors | Shape | Gains | Costs |
|---|---|---|---|
| **A. First-party only** | Kernel team ships CRM, ERP, Jira, Slack, email, webhook | Small attack surface, one quality bar | Customers route around gaps with raw webhooks |
| **B. SDK on Temporal workers (recommended core)** | Go or TypeScript activities on per-connector task queues, versioned by definition major | Fits the runtime; per-connector isolation; customer-hosted workers possible | Must operate a connector runtime and review third-party code |
| **C. Declarative HTTP templates** | Data-only definitions (endpoint, mapping, auth kind) run by a generic HTTP implementation | Admins add simple REST connectors without code | Limited; long-tail logic still needs B |
| **D. MCP-backed actions** | An action is a pinned tool on an external MCP server | Instant breadth from the MCP ecosystem | Tool descriptions are untrusted and can change (see the security section); weak per-tool governance |

Code location follows the same logic. The surveyed products run connectors vendor-hosted (Zapier, Pipedream, Merge), on a customer-hosted agent that only dials out, or in container isolation. Workato's on-prem agent "only makes outbound connections" over a mutually authenticated TLS WebSocket ([Workato on-prem](https://docs.workato.com/on-prem.html)). Airbyte runs actors as containers over STDIO or sockets ([Airbyte protocol](https://docs.airbyte.com/platform/understanding-airbyte/airbyte-protocol)). For the kernel, a customer-run Temporal worker polling a tenant-specific task queue gives the same shape as Workato's agent and ServiceNow's MID Server (MID details **UNVERIFIED**). It lets ERP calls run inside a customer network.

### Connections own credentials, and the host owns refresh

The host always stores and refreshes tokens, and connector code receives a fresh credential on each call. Nango refreshes tokens on demand and periodically, but "only if they have expired or are set to expire within 15 minutes." It holds locks so only one refresh runs at a time, stops automatic refresh after 3 consecutive days of failures, and warns that scripts must not cache credentials in variables ([Nango](https://docs.nango.dev/guides/api-authorization/credentials-refresh-and-validity)). The refresh-token-rotation race, where two workers refresh concurrently and the loser's token is invalidated, is a known pain with Salesforce and Xero. It is **UNVERIFIED** as documented behavior. n8n now encrypts credentials under per-instance data keys wrapped by a master key, rotates keys by default from 3.0, and re-encrypts lazily ([n8n](https://docs.n8n.io/hosting/securing/encryption-key-rotation/)). Dapr says flatly to never put secrets in component files ([Dapr](https://docs.dapr.io/reference/api/bindings_api)).

The recommended **Connection** row is `{id, tenant, definition@major, owner_kind (user | workspace | service), owner_principal, scopes, secret_ref (envelope-encrypted, key ID), status (active | needs_reauth | revoked | suspended), external_principal_id}`. Refresh runs in one place. Each Connection gets a small Temporal workflow or a row lock, so refreshes are serialized and run ahead of expiry, Nango-style. Types never name Connections directly, because Types are versioned and tenant-independent. A Type names a **connection slot**, such as `crm`, `erp` or `jira`. A container or tenant binds each slot to a Connection, as ServiceNow aliases reportedly do (**UNVERIFIED**) and as Salesforce named credentials do for endpoints. Credentials can then rotate, or differ per environment, without a new Type version.

### Per-user versus shared credentials is a per-action decision

Appian has a single switch, `usePerUserCredentials`, that selects system-wide credentials or each user's own ([Appian Connectors](https://docs.appian.com/suite/help/26.10/Connectors.html)). Salesforce distinguishes a named principal, where "the same credential … is shared by all users," from per-user principals. It stores per-user tokens in records that "can't be queried through SOQL, Apex, or the APIs" ([Salesforce](https://developer.salesforce.com/docs/platform/named-credentials/references/named-credentials-reference/nc-glossary.html)). Pipedream shows the cost of leaving the choice implicit. When a user has several accounts for one app, "Pipedream will use the most recently created account" ([Pipedream Connect users](https://pipedream.com/docs/connect/managed-auth/users.md)).

| Option | External system sees | Gains | Costs |
|---|---|---|---|
| **Shared service Connection** | One integration user | Works offline, at any hour, for any trigger; one place to revoke | Remote audit shows "integration user" for everything; the token is a high-value target; per-seat licence cost for integration users ([Truto](https://truto.one/blog/how-to-prevent-infinite-loops-in-bidirectional-api-syncs-a-developers-cookbook/)) |
| **Per-user Connection** | The actual human | True remote attribution (Linear: synced activity "correctly attributed to you" ([Linear](https://linear-web.pages.dev/docs/github))); remote permissions apply | Fails when the user has not connected, has left, or the hook fires at night; needs just-in-time connect prompts |
| **Per-user with service fallback (recommended, declared per hook)** | The human where possible, otherwise the service user with the human recorded kernel-side | Best attribution without stalling work | Two code paths; the fallback must be explicit and audited, never silent |

The kernel should make the hook declare `credential: service | user | user_or_service`. User credentials should be keyed by the human principal, never by an agent. An agent acting for a user borrows the user's Connection only within the agent's grant, which preserves *user ∩ agent grant*.

## Inbound needs a stable endpoint, a correlation key and a verdict for strays

### Sources, correlation and dedupe

Camunda's inbound model is the most explicit. A **correlation key (process)** is matched against a **correlation key (payload)** that the connector runtime evaluates. An **activation condition** filters events using the request context only, never process variables. A **message ID expression** enforces uniqueness on message start events ([Camunda inbound](https://docs.camunda.io/docs/components/connectors/use-connectors/inbound)). Flowable's event registry declares typed correlation parameters on each event definition ([Flowable Event Registry](https://flowable.com/open-source/docs/eventregistry/ch06-EventRegistry-Introduction)).

Polling dedupe falls into two families. **Seen-ID sets** come from Zapier, which records existing IDs on the first poll and fires only on unseen ones ([Zapier deduplication](https://docs.zapier.com/platform/build/deduplication)), and from Pipedream's `unique` strategy, a FIFO of the last 100 IDs ([Pipedream](https://pipedream.com/docs/components/contributing/api.md)). **Monotonic cursors** come from Airbyte, whose rule is that state passes to the next run only after the destination has emitted it, meaning "all records which came before it have been successfully written" ([Airbyte protocol](https://docs.airbyte.com/platform/understanding-airbyte/airbyte-protocol)). Kafka Connect's KIP-618 commits offsets "if and only if the source records for that batch were also written" ([KIP-618](https://cwiki.apache.org/confluence/display/KAFKA/KIP-618:+Exactly-Once+Support+for+Source+Connectors)). To catch update events, Zapier and Workato both build a composite key of `id` plus `updated_at` ([Zapier](https://docs.zapier.com/platform/build/deduplication); [Workato](https://docs.workato.com/developing-connectors/sdk/sdk-reference/triggers.html)). Merge recommends webhooks plus a 24-hour poll because "Webhooks can fail for a variety of reasons." For timestamp cursors, it says to store the time you *started* the last pull ([Merge](https://docs.merge.dev/basics/syncing-data/)).

The kernel design follows. An **intake** declares five things: the source, an activation condition (CEL over the payload), a correlation expression (CEL over the payload that yields an external key), a dedupe-key expression, and a verdict: `create`, `update`, `transition(to)`, or `comment`. Every inbound message first lands in an **inbox** table with a unique constraint on `(intake, dedupe_key)`. The relay then runs the Item operation. The Item write, the inbox row's `processed` flag and any polling cursor advance all commit in **one Postgres transaction**. That gives the kernel Airbyte's "state after destination ack" guarantee for free. Correlation keys live as indexed columns on the external link table, so lookup does not depend on custom fields.

| Option for webhook acknowledgment | Gains | Costs |
|---|---|---|
| Acknowledge after the Item write commits | Sender retries on any kernel failure | Slow handlers miss sender timeouts (GitHub expects a 2XX within 10 s, per the earlier research), causing duplicate deliveries |
| **Acknowledge after the raw event is durably in the inbox (recommended)** | Fast acks; the dedupe key absorbs sender retries; processing failures are kernel incidents with replay | Requires an inbox with retention and a replay UI |

### Unmatched events need an explicit verdict

Camunda's "Consume unmatched events" switch decides whether events that match no activation condition are acknowledged or rejected. A webhook returns 200 versus 422, Kafka commits versus withholds the offset, and SQS deletes versus keeps the message ([Camunda inbound](https://docs.camunda.io/docs/components/connectors/use-connectors/inbound)). The kernel should offer four verdicts per intake. `create` makes a new Item, the helpdesk default for email. `park` stores the event in an unmatched queue with a replay action, the default for update-type intakes. `reject` returns 422 or a nack. `drop` acknowledges and discards with a counter. Parking matters because correlation often fails for an innocent reason: the external link has not been written yet, since the outbound create is still in flight. A short re-match window, such as replaying parked events when a new external link is written, fixes most of these cases.

### Versioning with Type versions, and Camunda's webhook trap

Camunda's runtime keeps an inbound "executable" active for each element of a process definition that is either the latest version or "has instances waiting on message subscriptions." It deactivates the executable once the version is superseded and those subscriptions are gone ([Camunda inbound lifecycle](https://docs.camunda.io/docs/components/connectors/advanced-topics/inbound-lifecycle/)). The trap follows. If a new version reuses the same webhook path, **the new version's connector stays inactive until no instances remain on the older version**. Camunda's only workarounds are to finish the old instances first or to use a different path ([Camunda deduplication](https://docs.camunda.io/docs/components/connectors/advanced-topics/connector-deduplication/)). Shared endpoints need identical properties and mutually exclusive activation conditions, and deduplication IDs became cross-version only in 8.9 ([Camunda deduplication](https://docs.camunda.io/docs/components/connectors/advanced-topics/connector-deduplication/)).

The kernel can avoid the trap because its routing differs from Camunda's. The **endpoint belongs to the intake's stable identity on the Type**, not to a Type version. Routing then follows the kernel's existing version rules. A `create` verdict uses the Type's current version pointer, so ramps and cohorts apply. An `update` or `transition` verdict correlates to an Item first, then applies the intake configuration of **that Item's pinned Type version**. Several versions can therefore be live on one URL without draining.

| Part of the connector setup | Version behavior | Why |
|---|---|---|
| Hooks (attach point, mapping, policy, blocking) | **Pinned** with the lifecycle in the Type version | A hold must not change shape under an in-flight Item |
| Connector definition | Pinned by **major** version | Contract semantics |
| Implementation | Floats within the major version; hot-swappable, as in Bonita Enterprise | Bug fixes reach in-flight Items |
| Connection binding | Floats; tenant data, not Type data | Credential rotation, environment differences |
| Intake endpoint | Stable per Type; never per version | Avoids Camunda's drain trap |
| Intake mapping for updates | Resolved from the matched Item's pinned version | Old Items keep their semantics |
| Dedupe-key or cursor function | A change is a **breaking** change and needs a reset plan | Zapier warns that changing IDs re-fires items (doc title only, **UNVERIFIED**) |
| Policy, including field projection for connectors | Always latest | Compliance cannot wait for a drain |

Migration between Type versions should run with hooks **off** by default, as the earlier research recommended. A migrated Item that sits in a held state needs an explicit rule: either keep the hold and let the old run finish, or cancel it and re-fire under the new version.

## Two-way sync lives on an external link, and lineage stops at the vendor's edge

### The external link record

The sync tools agree on what to store. Unifi's Bond stores references, state, health, owner flag and history ([Unifi](https://docs.sharelogic.com/unifi/4.1/operate/bonding/bonds)). Getint keeps last-synced field data and tracks which comments and attachments have already synced. Its Hard Resync "treats all fields as modified" to repair drift ([Getint](https://docs.getint.io/getintio-platform/resync-and-hard-resync)). Unito shows per-item Success, Retrying or Failed status ([Unito](https://guide.unito.io/what-is-the-activity-log)). Linear's GitHub sync constrains scope on purpose. Only one repo per team can sync two ways. Only a designated synced thread crosses systems, and "Comments made not in the synced thread … will not get synced." Project-level custom statuses are not mapped ([Linear](https://linear-web.pages.dev/docs/github)).

The kernel needs three tables.

- `external_link(item_id, connection_id, sync_id, remote_type, remote_id, remote_key, remote_url, state, health, owner, last_remote_version)`. Here `state` is one of linked, paused, error, unlinked, remote_archived or remote_deleted. These are link states, deliberately distinct from Item states.
- `link_field_state(link_id, field, last_written_hash, last_seen_remote_hash, remote_version, synced_at)`.
- `link_child_map(link_id, local_child_id, remote_child_id)` for comments and attachments.

The Item's workflow already serializes everything for one Item, so all sync traffic for a link is naturally ordered.

Each sync declares a **field map** with a direction and an owner per field. It also declares a **status map** between remote statuses and kernel states or signals. The publish step should flag any status map that is not invertible, because A→B→A round trips then land in a different state. It should also require a rule for remote transitions that are illegal in the kernel graph: reject, queue as a suggestion, or force with an audit flag. Conflicts are rare if each field has one owner. For co-owned fields, the kernel compares remote and local versions against `link_field_state`. A conflict then becomes an explicit event, never a silent last-writer-wins. The research's precedents are Exalate's timestamp and version comparison (detail from a partially rendered page ([Exalate](https://docs.exalate.com/docs/prevent-sync-loops))) and the HubSpot–Salesforce per-field rules ("Prefer Salesforce unless blank", **UNVERIFIED**).

### Echo suppression must be layered and fail open

"The moment both systems can write to the same record, you open the door to infinite loops," and "No single pattern stops every loop" ([Truto](https://truto.one/blog/how-to-prevent-infinite-loops-in-bidirectional-api-syncs-a-developers-cookbook/)). Each technique fails somewhere. Integration-user filtering costs a paid seat and fails on payloads that carry no actor. Custom headers get stripped. Timestamps break on clock skew. State hashes break when they include volatile fields. In one OpenCTI case, a version field that defaulted to `now()` caused an unbounded replace loop ([Truto](https://truto.one/blog/how-to-prevent-infinite-loops-in-bidirectional-api-syncs-a-developers-cookbook/)). Exalate tags changes made by its incoming script as sync-generated, so "Changes that came from an incoming sync do not loop back" ([Exalate](https://docs.exalate.com/docs/prevent-sync-loops)). Getint syncs only real differences against its stored copy ([Getint](https://docs.getint.io/getintio-platform/resync-and-hard-resync)). The DIY ServiceNow eBonding sample business rule has no guard at all ([ServiceNow Community](https://www.servicenow.com/community/developer-forum/ebonding-between-two-servicenow-instances-a-comprehensive-guide/m-p/3065195)).

The kernel should stack five guards, cheapest first.

| Layer | Mechanism | Catches | Misses |
|---|---|---|---|
| 1. Kernel-side causation filter | The outbound sync skips any event whose causation chain starts at an inbound event of the same external link (a structured version of Exalate's tag) | Every local re-emission of a remote change | Remote echoes of the kernel's own writes |
| 2. Remote origin marker | Salesforce `Sforce-Call-Options client=<per-connection ID>`, returned as `changeOrigin`; Jira changelog author equal to the integration account; Smartsheet `Smartsheet-Change-Agent` | Remote echoes on the systems that support markers | HubSpot and others with no marker, which "requires timestamp-based filtering" ([Truto](https://truto.one/blog/how-to-prevent-infinite-loops-in-bidirectional-api-syncs-a-developers-cookbook/)) |
| 3. Last-written hash | An inbound value whose hash equals `last_written_hash` is an echo: attribute it to the original causation and drop it | Echoes on any system | Remote normalization, such as reformatted rich text, which then looks like a change |
| 4. No-op suppression | Emit no event when nothing changed | Ping-pong between writers converging on the same value | Divergent oscillation |
| 5. Hop and oscillation budget | N connector-originated writes per link per window; on breach, the **link** goes to `error`, not the Item | Everything else, eventually | The first N iterations |

Echo detection should **fail open**. A missed echo costs one redundant write, while a wrongly suppressed event silently loses data ([Truto](https://truto.one/blog/how-to-prevent-infinite-loops-in-bidirectional-api-syncs-a-developers-cookbook/)). Correctness should come from reconciliation: webhooks for latency, periodic polls with an overlapping window for completeness, and an idempotent hard resync per link.

### How far causation travels

Inside the kernel, causation is exact. Every operation carries `causation_id` (the triggering event) and `correlation_id` (the root), and Temporal headers carry them across the relay. Across external systems the picture is bleak, and the report should say so plainly. **No surveyed SaaS persists an arbitrary client-supplied correlation ID and returns it on later, unrelated webhooks.** The research looked for one and found none. The few markers that exist identify *which app* made a change, not *which causal chain*.

| Carrier | What travels | Scope | Source |
|---|---|---|---|
| Salesforce CDC `changeOrigin` | `client=<Client_ID>` from the writer's `Sforce-Call-Options` header, echoed in the change event; Salesforce says to "Use this field to detect whether your app initiated the change"; populated only for API apps and Lightning | App identity, not lineage. A per-Connection client ID gives per-Connection identity, and a second client reusing the ID would be suppressed too | [Salesforce CDC header](https://developer.salesforce.com/docs/atlas.en-us.change_data_capture.meta/change_data_capture/cdc_event_fields_header.htm) |
| Salesforce `transactionKey`, `sequenceNumber`, `commitUser` | Groups and orders changes from one Salesforce transaction | Within Salesforce | Same |
| MCP `_meta.traceparent`, `tracestate`, `baggage` | W3C trace context on every MCP request (SEP-414, Final, included in 2026-07-28) | The synchronous request path, plus spans the server chooses to link | [SEP-414](https://modelcontextprotocol.io/seps/414-request-meta) |
| Zendesk `via` on side-conversation events | Channel and source of a change | Origin kind, not chain | [Zendesk API](https://developer.zendesk.com/api-reference/ticketing/side_conversation/side_conversation/) |
| GitHub `GITHUB_TOKEN` rule | Events caused by the built-in token "will not create a new workflow run" | Structural loop break, not lineage | [GitHub Docs](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow) |
| Jira changelog author, Smartsheet change-agent header | Actor or agent identity | App identity | [Truto](https://truto.one/blog/how-to-prevent-infinite-loops-in-bidirectional-api-syncs-a-developers-cookbook/) |
| Idempotency keys | Dedupe of one retried call | One hop only; a new hop has a new key | Stripe semantics **UNVERIFIED** |
| W3C Trace Context over HTTP | `traceparent` on outbound requests | Nothing obliges a SaaS to attach it to a later webhook (**UNVERIFIED**; w3.org not fetched) | none fetched |

The design consequence is a split. **Lineage is the loop guard inside the kernel, and the external link plus last-written hash is the loop guard outside it.** Where a system offers a writer-chosen marker, the kernel should set it to a value that encodes the Connection. Where a system offers a writable field, such as a Jira entity property or a ServiceNow `correlation_id` (**UNVERIFIED** as a pattern), the kernel can store its last `causation_id` there to aid forensics. It should never rely on that field for correctness.

## Identity and audit: every connector write carries five fields

None of the BPM pages fetched define an on-behalf-of model for connectors. Connectors run as the engine with stored credentials ([Bonita connectors execution](https://documentation.ofelia.com/bonita/next/runtime/connectors-execution); [Camunda outbound](https://docs.camunda.io/docs/components/connectors/use-connectors/outbound)). Sync tools default to the integration user as author on both sides. Linear is the exception when people connect their accounts ([Linear](https://linear-web.pages.dev/docs/github)). The agent platforms record the end-user ID and an execution log ID, and Arcade lists "OpenTelemetry audit logs" ([Arcade](https://docs.arcade.dev/home/auth/how-arcade-helps)), but none of the fetched pages shows a first-class on-behalf-of field. The kernel's attribution model therefore leads the field. It should extend the fields the earlier research fixed (`actor`, `on_behalf_of`, `agent_session`) with the hook fields.

| Field | Outbound hook write-back | Inbound intake write | MCP tool call into the kernel |
|---|---|---|---|
| `principal` (the policy report's `actor`): whose rights were checked | The hook's own principal, `hook:<type>@<ver>/<hook_id>`, limited to the fields the hook declares it writes | The intake's service principal, limited to its declared operations and fields | The human from the verified token `sub`, intersected with the agent grant |
| `on_behalf_of` | Principal of the triggering event (human, agent or another hook's root) | Mapped kernel user for the remote actor, through an `external_identity` table; otherwise null, with the raw remote actor stored | The human (delegated mode) |
| `agent_session` | Carried from the triggering event if an agent caused it | n/a | A kernel-minted handle bound to the user, per MCP's state-handle rule |
| `originating_hook` | `type@version/hook_id` + definition@major + implementation@semver + `connection_id` | `type/intake_id` + source + `connection_id` | `mcp:<tool name>` + OAuth `client_id` or CIMD URL |
| `causation_id`, `correlation_id`, `hop` | The event that fired the hook; root carried; hop + 1 | Inbox row ID (the remote event); new correlation root unless the echo filter maps it to an existing one | From `_meta.traceparent` recorded as a **claim**, never as identity |

Three rules keep this honest. First, the hook's rights are its **declared writes**, checked at Type publish like YouTrack's `requires`. They are not the triggering user's rights, because a Type-defined hook is not the user's choice. Second, connector inputs go through `Project` under the hook's principal, so the single projection function also covers connectors. Third, agent attribution survives the round trip. When an agent's transition fires an ERP hook, the invoice event names the hook as principal, the user as `on_behalf_of`, and the agent session. Investigators can then answer both "who could" and "why".

## Credentials and connector text are the real blast radius

### Drift and UNC6040: integration identities are the target

According to Google's Threat Intelligence Group, between August 8 and at least August 18, 2025, UNC6395 "targeted Salesforce customer instances through compromised OAuth tokens associated with the Salesloft Drift third-party application." The actor exported Account, Opportunity, User and Case data and then searched it for AWS `AKIA` keys, Snowflake tokens and the strings "password", "secret" and "key". It deleted query jobs, "however logs were not impacted." Salesloft and Salesforce revoked all tokens on August 20, and by August 28 GTIG said other integrations, including Drift Email tokens for Google Workspace, were affected too ([GTIG Drift advisory](https://cloud.google.com/blog/topics/threat-intelligence/data-theft-salesforce-instances-via-salesloft-drift?e=48754805)). The earlier research cited **700+ potentially impacted organizations** ([The Hacker News](https://thehackernews.com/2025/08/salesloft-oauth-breach-via-drift-ai.html)). How the tokens were first stolen is **UNVERIFIED**. Press reports describe access to Salesloft's GitHub months earlier, leading into Drift's AWS environment. UNC6040 needed no stolen token. Callers persuaded employees to authorize "a malicious connected app... often a modified version of Salesforce's Data Loader," and data left immediately. "In all observed cases, attackers relied on manipulating end users, not exploiting any vulnerability inherent to Salesforce" ([GTIG UNC6040](https://cloud.google.com/blog/topics/threat-intelligence/unc6040-proactive-hardening-recommendations)). GTIG's hardening list reads like a requirements document for kernel Connections:

- deny-by-default API access, with allowlisted connected apps only
- per-app IP, MFA, session and refresh-token limits
- revoking every token and session when an app is revoked
- a detection rule: an OAuth success followed within 10 minutes by bulk access, with a starting threshold of 50 or more API calls and 100 MB or more in an hour

([GTIG UNC6040](https://cloud.google.com/blog/topics/threat-intelligence/unc6040-proactive-hardening-recommendations))

The kernel takes six lessons from these breaches:

- **Every Connection is its own revocable principal with minimum scopes.** Revoking it cancels pending runs and destroys its tokens.
- **Creating a Connection for a connector definition the tenant has not allowlisted requires admin approval.** This is the kernel's answer to UNC6040's user-approved app.
- **Each Connection gets a volume baseline and alerts on anomalies.** The `principal` and `originating_hook` fields make this cheap.
- **Connector audit lives in the kernel's append-only log, which the connector principal cannot write or delete.** Drift's attacker deleted jobs but not logs.
- **Secret-shaped values are scanned for and refused in connector outputs before they land in Item fields.** The attackers mined CRM records for embedded credentials.
- **Credentials are encrypted with per-tenant keys that rotate.** n8n's earlier single-key model is the cautionary case: leaking one key plus the database exposed everything. That claim is **UNVERIFIED**; GitGuardian's analysis was not fetched.

### Prompt injection and tool poisoning arrive through connectors

The GitHub MCP case of May 2025 showed that trusted tools are enough. A malicious public issue made an agent using the official server read private repos and leak them into a public PR. Invariant Labs called it "a fundamental architectural issue that must be addressed at the agent system level" and recommended restricting an agent "to working with only one repository per session" ([Invariant Labs](https://invariantlabs.ai/blog/mcp-github-vulnerability)). **Tool poisoning** puts instructions in tool descriptions that are "invisible to users but visible to AI models." **Rug pulls** change a description after approval. **Cross-server shadowing** lets a malicious server redirect a trusted `send_email` tool. The mitigations are to show full descriptions, pin server versions and tool hashes, and enforce "dataflow controls between different MCP servers" ([Invariant Labs](https://invariantlabs.ai/blog/mcp-security-notification)). The earlier research added GitLost (July 2026), where the one-word prefix "Additionally" got past threat detection ([The Hacker News](https://thehackernews.com/2026/07/public-github-issue-could-trick-github.html)). It also added Supabase/Cursor, where a support ticket told an agent running as `service_role` to post integration tokens back into the ticket ([Simon Willison](https://simonwillison.net/2025/Jul/6/supabase-mcp-lethal-trifecta)). Later MCP incidents, such as the Asana cross-tenant exposure, the malicious `postmark-mcp` package and the mcp-remote CVE-2025-6514, were not verified in this round's fetches.

For connectors, this means **every Item field written by an intake, a sync or a query hook is externally writable, and it is tagged untrusted**, exactly like customer-written fields in the earlier policy model. An agent session that has read a tainted field cannot act autonomously on egress, and outbound hooks count as egress. A Jira comment that arrives through sync is untrusted text. A CRM plan enum validated against the schema is low risk. A free-text CRM "account notes" field is not. The Type should declare which connector outputs are free text, so taint is precise rather than blanket.

## MCP: expose Items as a server, consume external servers through a pinning gate

The 2026-07-28 MCP revision makes the protocol stateless. It removes sessions and the `initialize` handshake. It replaces server-initiated elicitation and sampling with Multi Round-Trip Requests, where the server returns `input_required` and the client retries. It moves Tasks into an extension and standardizes W3C trace context in `_meta` ([MCP changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog.md)). Authorization is OAuth 2.1. The server is a resource server that MUST publish RFC 9728 metadata and MUST validate that tokens were issued for it. "The MCP server MUST NOT pass through the token it received" ([MCP authorization](https://modelcontextprotocol.io/specification/2025-11-25/basic/authorization)). Client ID Metadata Documents are preferred, and Dynamic Client Registration is deprecated. Clients must validate `iss` and key credentials by issuer ([changelog](https://modelcontextprotocol.io/specification/2026-07-28/changelog.md)). Servers "MUST NOT treat possession of a state handle as authentication" ([MCP security best practices](https://modelcontextprotocol.io/specification/latest/basic/security_best_practices)). The hosts have converged on MCP. OpenAI's Responses API connects to any remote MCP server, its built-in `connector_id` connectors are deprecated for models released after September 1, 2026, and approval is requested by default ([OpenAI](https://developers.openai.com/api/docs/guides/tools-connectors-mcp/index.html)). Copilot Studio exposes both MCP servers and Power Platform connectors. Its governance gap is instructive: admins can block individual connector actions, but "if an MCP server is allowed, its full tool surface is allowed" ([Copilot Studio CAT blog](https://microsoft.github.io/mcscatblog/posts/compare-mcp-servers-pp-connectors)). Claude's connector support is **UNVERIFIED** in this round.

| Option | Gains | Costs |
|---|---|---|
| Expose only | Agents in every host can work on Items | Kernel agents cannot reach the external tool ecosystem |
| Consume only | Kernel agents gain breadth | Kernel is invisible to external agents, which is now table stakes |
| **Both, asymmetrically (recommended)** | Reach in both directions | Two security postures to run |

**Exposing Items** should use a narrow, typed tool surface: `item.get`, `item.search`, `item.comment`, one tool per allowed transition, and `approval.complete`. There should be no generic `update`. Hosts that can only allow or deny whole tools can then still govern per action. Every result passes through `Project`. Long operations and approvals map onto MRTR `input_required` rather than server-initiated prompts. The kernel enforces approval server-side and never trusts a host's "Always Allow". Item and workflow IDs serve as state handles, checked against the token's subject on every call. The token supplies `principal`, the client ID or CIMD URL supplies the agent identity, and `traceparent` becomes a recorded claim on the event.

**Consuming external MCP servers** is a fourth implementation kind for connector actions. It is used mainly inside agent sessions, not for deterministic lifecycle hooks, because tool contracts can change under the kernel. It needs:

- a per-tenant allowlist of servers
- per-tool selection, like OpenAI's `allowed_tools`
- description and schema hashes pinned at approval, with re-approval on any change, to counter rug pulls
- per-user tokens obtained through the kernel's Connection vault, never by forwarding the kernel's inbound token
- `traceparent` injected so downstream spans link back to the kernel event
- every result labeled untrusted

A2A is complementary to MCP and covers agent-to-agent delegation ([SecureW2, secondary](https://securew2.com/blog/a2a-protocol-security)). It is not a connector mechanism for Items.

## The recommended model in one place

```yaml
# Type: support_ticket@7 (excerpt; connector sections only)
connection_slots:
  crm:  { definition: crm.accounts@3,  credential: service }
  erp:  { definition: erp.invoices@2,  credential: service }
  jira: { definition: jira.issues@4,   credential: user_or_service }

hooks:
  - id: fetch_plan
    on_enter: triage
    action: crm.get_account_plan          # kind: query
    input:  { account_id: "item.fields.customer_account_id" }   # CEL over Project(hook, item)
    output: { "fields.customer_plan": "result.plan" }           # validated by schema; enum
    blocking: true                         # Item held: "Preparing: fetching plan"
    timeout: 10s
    retries: { max: 3, backoff: 2s }
    on_failure: { degrade: { "fields.customer_plan": "unknown" } }
    sla_during_hold: paused

  - id: invoice
    on_complete: approval.billing          # fires when the approval token completes
    when: "approval.outcome == 'approved'"
    action: erp.create_invoice             # kind: modify; idempotency key passed
    output: { "fields.invoice_id": "result.id", "fields.invoice_url": "result.url" }
    blocking: true
    timeout: 60s
    on_failure: { fail: incident }         # retry / skip / rerun, never silent
    errors: { CUSTOMER_ON_HOLD: { raise: to_state.billing_blocked } }

syncs:
  - id: jira_escalation
    slot: jira
    link_on: transition.escalate           # outbound create; writes external_link
    fields:
      title:        { dir: out,  owner: kernel }
      description:  { dir: out_on_create }
      engineering_status: { dir: in, owner: jira }
      comments:     { dir: both, thread: engineering, inbound_scope: members_only }
    status_map: { "Done": signal.engineering_resolved, "Won't Do": signal.engineering_declined }
    echo: [causation, origin_marker, last_written_hash, noop, budget(20/h)]
    reconcile: { poll: 6h, overlap: 15m }

intakes:
  - id: jira_webhook                       # endpoint stable on the Type, not the version
    source: jira.issues@4/webhook
    correlate: "payload.issue.id"          # → external_link.remote_id
    dedupe: "payload.issue.id + ':' + payload.timestamp"
    activation: "payload.webhookEvent in ['jira:issue_updated','comment_created']"
    unmatched: park
```

The open choices, and the recommended pick for each, are summarized here. The sections above give the evidence.

| Choice | Options | Recommended | Main trade-off |
|---|---|---|---|
| Attach points | enter/exit only; enter/exit/complete; arbitrary events | `on_enter`, `on_exit`, `on_complete(step)`, `on_create`, `on_close` | `on_complete` adds a concept but matches token-completed approvals |
| Blocking | never; invisible gate; visible hold; explicit state | Visible hold reusing the approval-token wait | Two transactions per hook; SLA-during-hold must be declared |
| Exit hook timing | Before the transition commits (reject on failure); hold after submission commits (Bonita); purely post-commit | Hold after submission commits | The user's input is saved even when the push fails, which is usually desirable |
| Transaction placement | Inline (Flowable); after commit (Bonita) | After commit, output in its own kernel write | At-least-once; requires idempotency keys |
| Default failure policy | fail; ignore | `fail` for blocking and modify actions; `degrade` allowed for query actions; `ignore` disallowed for outbound pushes | Strictness versus stuck Items |
| Output landing | Item fields; connector side table; external link | Item fields through a normal write; sync state on the external link | Fields stay queryable and policy-checked |
| Hook rights | Triggering user; hook's own grant; intersection | Hook's declared writes; triggering user recorded as `on_behalf_of` | Hooks can write fields the user cannot, by Type design and audited |
| Credential owner | Shared; per-user; per-user with fallback | Declared per slot: `service`, `user`, `user_or_service` | Attribution versus availability |
| Packaging | First-party; SDK; declarative HTTP; MCP-backed | SDK on Temporal workers as core, declarative HTTP for the long tail, MCP for agent sessions | A runtime to operate |
| Inbound ack | After the Item write; after the inbox write | After the inbox write | Needs inbox retention and replay |
| Unmatched events | create; park; reject; drop | Per intake; `park` default for updates | A parked queue needs an owner |
| Endpoint versioning | Per version (Camunda); per Type | Per Type; updates use the Item's pinned version | Intake identity must be stable across versions |
| Echo suppression | Any single guard; layered | Five layers, failing open, plus reconciliation | Complexity; no external lineage |
| Conflicts | Last-writer-wins; per-field owner; manual | Per-field owner; co-owned fields raise conflict events | Authoring effort per sync |
| MCP | Expose; consume; both | Both, asymmetrically | Two security postures |

## Worked example: a helpdesk ticket with CRM, ERP and Jira

A customer emails support. The email intake creates `T-812` of `support_ticket@7` in state `triage`. Transaction 1 writes the Item, the event `e1` and the outbox row. Because `fetch_plan` is a blocking `on_enter(triage)` hook, it also writes a hold, "Preparing: fetching customer plan". The ticket is not yet in the triage queue, and its SLA is paused. The relay delivers `e1` to `item-T-812`. The workflow runs `crm.get_account_plan` on the CRM connector's task queue. The activity asks the kernel for `Project(hook:support_ticket@7/fetch_plan, T-812, connector:crm.accounts@3)`, which returns `customer_account_id` and nothing else. The activity uses the workspace's shared CRM Connection (read-only scopes) and gets `plan = "enterprise"`. Transaction 2 is an `update` that writes `customer_plan`, releases the hold, and starts the SLA. CEL routing then puts the ticket in the enterprise queue. If the CRM had timed out three times, the `degrade` policy would have written `unknown` and released the hold anyway, with a `hook_failed` event explaining why.

The agent working the ticket finds that the customer wants paid onboarding and requests the `billing` approval. A finance lead completes the approval token with `approved`. That completion is event `e7`, and it fires `on_complete(approval.billing)`. The kernel holds the ticket as "Creating invoice". The ERP activity sends `Idempotency-Key = hash(tenant, invoice, e7)`. A Temporal retry after a network blip therefore cannot create a second invoice. If the ERP lacks idempotency support, the implementation first searches for an invoice whose external reference is `T-812`. The ERP answers with `INV-5531`. Transaction 2 writes `invoice_id` and `invoice_url` and releases the hold. If the ERP had returned `CUSTOMER_ON_HOLD`, the `raise` policy would have moved the ticket to `billing_blocked`, a state the Type declares in its "waiting" category. A 500 after retries would instead have left the hold as an incident, offering finance admins **retry** (same key), **skip**, or an audited **rerun** (new key).

The agent then escalates to engineering. The `escalate` transition links the `jira_escalation` sync. The agent has connected a personal Jira account, so `user_or_service` resolves to the agent's own Jira credential, and the Jira issue shows a real reporter. The outbound create writes an external link `{T-812 ↔ ENG-4410, remote_id 100233, state linked}` and records the title hash in `link_field_state`. When Jira's webhook later reports the issue's creation and its first edit, the dedupe key absorbs Jira's retries. The changelog author equals the Connection's account, and the hash matches `last_written_hash`, so both are recognized as echoes and dropped. An engineer then comments in Jira. That comment correlates through `payload.issue.id` to the external link. It has a new hash and a non-integration author, so it is a genuine change. It lands as a comment in the ticket's **members-only** engineering thread, mirroring Linear's single synced thread. It is tagged untrusted, so an agent drafting the customer reply cannot paste it outward without passing the egress gate. When the engineer moves the issue to Done, the status map emits `signal.engineering_resolved`. That moves the ticket from `escalated` back to `in_progress` with a task to reply to the customer. It does not resolve the ticket, because "Done in Jira" and "resolved for the customer" are different facts. If the Jira Connection loses its token, the external link's health goes to `error` and the Connection to `needs_reauth`. The ticket itself keeps moving.

The attribution trail shows how the fields work together.

| Event | Operation | principal | on_behalf_of | originating_hook | causation_id |
|---|---|---|---|---|---|
| e1 | create T-812 | `intake:support_ticket/email` | mapped customer contact | `support_ticket/email_intake` + inbox row | inbox `m-9f2` |
| e2 | update `customer_plan`, release hold | `hook:support_ticket@7/fetch_plan` | customer contact (root of e1) | `fetch_plan` · `crm.accounts@3` · `crm.salesforce@3.4.0` · conn `crm-shared` | e1 |
| e7 | complete approval `billing` | finance lead | — | — | e6 (request) |
| e8 | update `invoice_id`, release hold | `hook:support_ticket@7/invoice` | finance lead | `invoice` · `erp.invoices@2` · `erp.netsuite@2.3.1` · conn `erp-svc` | e7 |
| e9 | transition `escalate` + link | support agent (or agent ∩ grant, with `agent_session`) | support agent's user | — | user action |
| e12 | comment (inbound) | `intake:support_ticket/jira_webhook` | engineer mapped through `external_identity` | `jira_escalation` · conn `jira-user-agent` | inbox `j-77a` (new correlation root) |
| e14 | transition `in_progress` via signal | `sync:support_ticket@7/jira_escalation` | engineer | status map `Done → engineering_resolved` | e13 (inbound status) |

## Open questions

These questions remain open for the spike and design review:

- **Should SLA clocks pause during holds?** Pausing protects agents from connector latency. Not pausing protects customers. The answer may differ between `on_enter` enrichment and `on_exit` pushes.
- **Can a hook's declared writes exceed the triggering user's rights?** This report says yes, by Type design and with audit. A stricter tenant might require intersection, which breaks enrichment for low-privilege requesters.
- **How do Item migration and open holds interact?** Should a migrated Item keep the old run, or cancel and re-fire under the new version? Camunda's listener-migration bug (#53122, title only) suggests this is where edge cases live.
- **Is the per-link echo budget a fixed number or adaptive?** The Truto advice to fail open conflicts with an aggressive budget.
- **Which external systems actually echo a writer-chosen marker?** Salesforce `changeOrigin` is documented. Jira, GitHub, ServiceNow and HubSpot echo actor identity at best (**UNVERIFIED**). A short verification pass per launch connector should decide whether each relies on markers or hashes.
- **Do kernel-side idempotency keys reach the ERP?** Stripe-style semantics are **UNVERIFIED**, and many ERPs have no idempotency header. The search-before-create fallback needs a writable reference field on each target.
- **Should consumed MCP tools ever back deterministic lifecycle hooks,** or only agent sessions? Pinning description hashes helps, but MCP servers can change behavior without changing descriptions.
- **Where do customer-hosted workers get credentials?** Inside the customer network (their vault), or from the kernel's vault over the tunnel? The first is safer. The second keeps refresh in one place.
- **How much of this needs re-verification?** Camunda 8 listener semantics, Exalate's loop guard, ServiceNow IntegrationHub aliases and the MID Server, and the MCP Registry's status were all unverified. They should be checked before any of them becomes a load-bearing precedent.

## Conclusion

The founder's framing of "connector in on enter, connector out on exit or complete, inbound creates or updates" holds up against every product family surveyed, but only with two corrections. First, "connector in" must be an explicit, visible hold rather than an implicit block. Implemented as the same token-completed wait the kernel uses for approvals, it adds no new runtime concept. Bonita's invisible gap and Camunda's drain trap are what happens when the lifecycle and the connector runtime are designed separately. Second, two-way sync is not a pair of connectors. It is a third object, the external link, with its own state, health and per-field memory. That object is what lets failures land on the integration instead of the ticket. It is also what makes echo suppression possible across systems that will never carry the kernel's causation IDs.

The broader shift is that connectors are now an identity and data-flow problem more than an integration problem. Drift and UNC6040 were not protocol bugs. They were legitimate integration principals used illegitimately. The agent incidents were legitimate tools fed hostile text. The kernel's existing primitives, namely five attributed operations, one projection function, user ∩ agent grants and taint on externally writable fields, already contain most of the defense. The connector model's job is to make sure that every connector path goes through them. No convenience path should be allowed to skip them.

## Verification addendum (2026-11-12)

A follow-up pass checked the three precedents the report relied on most but had marked UNVERIFIED.

**Camunda 8 execution listeners: confirmed, and closer to our hooks than expected.** Source: [docs.camunda.io execution listeners](https://docs.camunda.io/docs/components/concepts/execution-listeners/).
- **Events:**
  - Listeners attach to tasks, events, gateways, processes and subprocesses.
  - The events are `start`, `end`, `cancel` (process only) and `beforeAll` (multi-instance only).
- **Blocking and ordering:**
  - Listeners are blocking: the element does not advance until the listener's job completes.
  - Several listeners on one event run in model order.
- **Variables:**
  - Start listeners can set local variables.
  - End listeners can set variables that propagate to the parent scope.
- **Failure:** jobs retry according to `retries`. When retries run out, an incident halts the instance, and after resolution only the failed listener reruns.
- **Restrictions:**
  - Listeners cannot throw a BPMN error, so a "raise to an error state" policy has no direct equivalent.
  - Start events, boundary events, gateways and error end events cannot carry start or end listeners.

What this means for us: Camunda's on-enter and on-exit hooks are blocking by design, and the user sees nothing. That supports making our hold visible.

**Camunda 8 user task listeners: confirmed.** Sources: [user task listeners](https://docs.camunda.io/docs/components/concepts/user-task-listeners/), [8.7 version](https://docs.camunda.io/docs/8.7/components/concepts/user-task-listeners).
- **Events:** creating, assigning, updating, completing and canceling.
- **What a listener can do:**
  - Correct the assignee, due date, follow-up date, candidate users and groups, and priority before the transition is finalized.
  - Deny the transition, which rolls the task back. When a transition is denied, no corrections are applied.
  - Creation and cancelation cannot be denied.
- **Global user task listeners**, defined once per cluster, exist in the upcoming release (docs/next) with the same semantics.

This is a working precedent for our "allow / deny / correct" check that a machine completes.

**Exalate loop prevention: confirmed at the level of mechanism, but not the internals.** Source: [How does Exalate prevent sync loops?](https://docs.exalate.com/docs/prevent-sync-loops)
- Changes applied by the incoming side are marked as sync-generated, so the outgoing side on the same instance skips them. Only genuine local changes sync out.
- A `firstSync` flag separates the first sync from later updates.
- Timestamps and version tracking detect when both sides changed since the last sync.
- Simultaneous changes are processed one at a time through a sync queue, which also stops queued or just-completed updates from being reprocessed.
- No default conflict policy and no numeric limits are documented; conflict resolution is left to the customer's scripts.

This confirms that the "origin marker on our own writes" layer is industry practice. The marker is kept locally, though: Exalate does not send it to the other system.

**ServiceNow IntegrationHub: partly confirmed.** Sources: [Explore credentials, connections, and aliases](https://www.servicenow.com/docs/bundle/zurich-platform-security/page/product/credentials/concept/credentials-connections-alias.html), [spoke-building guidance (ServiceNow community)](https://www.servicenow.com/community/automation-engine-blog/integration-hub-spokes-and-how-to-build-them/ba-p/2788254).
- **What ServiceNow separates:**
  - A connection: the endpoint.
  - A credential: the authentication data.
  - An alias: a named reference that flows use and that resolves to a connection and credential at runtime.
  - Child aliases allow several connections per integration.
  - Connection and credential records can be restricted by scope.
- **Spokes:** a spoke is a scoped application for one integration. It is a package of actions and subflows, and every spoke ships a default alias. An admin must bind that alias to a real connection before the spoke can be used.
- **Guidance:** one integration call per action. Use a MID Server to reach systems behind a firewall. Inbound integrations are Scripted REST webhooks, scheduled polling flows or inbound email.
- **Versioning:** ServiceNow certifies its own spokes per release. Customers clone actions rather than editing them, and must re-test their clones when a new version ships.

ServiceNow's alias is the same concept as our "connection slot", which gives the slot a strong precedent. Still unverified: the identity an action runs as, and the details of the eBonding spoke.
