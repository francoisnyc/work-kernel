# Standardize the hook wire, defer packages

Pli needs a **hook protocol now and does not need a package system yet**. What it does need is a thin, data-only notion of a "bundle": a namespaced, versioned directory of YAML Types and connector descriptors. Across eighteen platforms the pattern is consistent. The durable part of every extension model is a **small wire contract plus a versioned declarative descriptor**. Examples are Camunda's job type and element template, Bonita's definition and implementation, Terraform's protocol 6 plus registry metadata, and Kubernetes' AdmissionReview plus CRDs. Full package systems with publisher namespaces, dependency graphs and install/upgrade machinery appear only where a vendor hosts untrusted third-party code for many tenants: Salesforce, Power Platform, ServiceNow, Atlassian Forge. Even there, the hard part they solve is the lifecycle of declarative definitions, not code. The in-process connector model Pli has today is the one every infrastructure ecosystem has paid to leave. Kubernetes took from v1.7 to v1.31 and removed about 1.5 million lines to evict in-tree providers. Camunda dropped embedded delegates and engine plugins. Jenkins accumulated decades of class-loader workarounds. n8n's in-process community nodes were used in January 2026 to steal OAuth tokens with the host's own master key. Pli's near-term job is moving one trusted company's Bonita processes and Jira projects, not running a marketplace. So the deciding factors all point the same way: define the out-of-process contract before the connector count grows, keep first-party connectors behind the same interface, and make namespaces mandatory in the bundle format from day one. Do not build a dependency resolver, registry or code host until specific triggers, listed at the end, appear. The protocol itself should be a profile of existing standards rather than an invention:

- Kubernetes AdmissionReview semantics for check and enrich hooks.
- Camunda and Temporal semantics for retries, business errors and async completion, with Nexus RPC's sync/async split.
- Standard Webhooks signing.
- IETF Idempotency-Key error codes.
- MCP-style JSON Schema descriptors.

## Every platform splits a wire contract from a versioned descriptor

Read side by side, the platforms converge on a two-layer shape. The lower layer is a runtime contract: a job type, an RPC protocol, an HTTP request schema. The upper layer is a declarative descriptor that names the contract and its version, and describes inputs, outputs and configuration UI. What differs is where the code runs and how much lifecycle machinery surrounds the descriptor.

| Platform | Extension unit | Runtime contract | Packaging and versioning | Running instances on upgrade | Namespacing and dependencies | Isolation |
|---|---|---|---|---|---|---|
| **Bonita** | Java connector: `.def` + `.impl` | In-engine Java lifecycle (`validateInputParameters`, `executeBusinessLogic`) | Maven jar/zip with def and impl id+version; bundled in SCA archive | Implementation swappable after deploy (paid editions); live update deprecated | id+version only; no resolver | None; engine thread pool |
| **Camunda 8** | Job worker; Connector SDK jar + element template | Job type string; activate/complete/fail/throw-error; leases | `type` like `io.camunda:template:1`; template integer `version` + `engines` ranges | Instances stay on their version; explicit transactional migration plan | Type string; no resolver | Out of process (remote engine) |
| **Flowable** | Java service task or External Worker | Topic-based acquire/lock/complete over REST; clients in Go, Python, .NET, JS | Not documented in fetched pages | Not documented | Topic name | Out of process for external workers |
| **Temporal / Nexus** | Activity worker; Nexus Service/Operation | Task queues; timeouts, heartbeats, task tokens; Nexus RPC HTTP spec | Worker binaries; Worker Deployment versions (name + Build ID) | Pinned vs Auto-Upgrade; patching | Namespaces; Nexus endpoint registry with caller allowlists | Out of process, customer-hosted |
| **Jira** | Forge app (hosted); Connect (remote, retiring); Automation webhooks | Forge invocation limits; Automation token-in-header webhook | Forge manifest | Not documented | App identity | Hosted sandbox (Forge) |
| **ServiceNow** | Scoped app; IntegrationHub spoke | Flow actions; per-transaction licensing | Versioned releases in a private app repo or the Store | Not documented | Vendor-prefix scopes tied to ownership; repo resolves dependencies | Platform-hosted script |
| **YouTrack** | App package (JS rules, HTTP handlers, widgets) | Workflow rule API; scoped HTTP handlers | `manifest.json` name + semver + min/max YouTrack version | Upload overwrites by name | Name is identity | Platform-hosted JS |
| **Salesforce** | 1GP/2GP/unlocked package | Platform metadata and Apex | Namespaced packages, dependency matrix | Deprecate-not-delete on removal | Namespace; enforced deps | Platform-hosted |
| **Power Platform** | Managed/unmanaged solution | Dataverse components | Publisher prefix; update/patch/upgrade | Upgrade deletes missing components | Prefix; base-before-dependent enforced | Platform-hosted |
| **n8n** | npm community node | In-process node API | npm semver, one copy per instance | Upgrade "can break every workflow" | npm names; blocklist | None |
| **Zapier** | CLI app object | Schema-validated definition; hosted Node 22 | Semver + `platformVersion` | Staged migration within a major; cross-major refused | App identity | Vendor-hosted |
| **Activepieces** | npm "piece" | In-process piece API | Semver with explicit breaking rules | Separate sync page (not verified) | npm | Operator-chosen sandbox mode |
| **Windmill** | Script + JSON Schema resource type | Per-job worker process | Content-hashed immutable versions | Previous hash stays deployed | Paths; Hub sync | Opt-in nsjail |
| **Kubernetes** | CRD + controller; admission webhook; CSI driver | AdmissionReview over HTTPS; CSI gRPC | Helm/OLM; CRD `spec.versions` | Conversion webhooks; storage version | API groups; OLM deps (scaled back in v1) | Out of process |
| **Terraform** | Provider binary | Plugin protocol v5/v6 over gRPC | Registry namespace + lock file | `init -upgrade` moves forward explicitly | `registry/namespace/type` | Subprocess |
| **Grafana** | Backend plugin | Fixed capability RPCs over gRPC (go-plugin) | Plugin version | n/a | Plugin id | Subprocess |
| **Backstage** | Plugin + module (Node/React) | Extension points, in-process | npm; Red Hat dynamic plugins via OCI | Restart to load | npm | None |
| **Jenkins** | `.hpi` plugin | In-JVM extension points | Maven; `Plugin-Dependencies` | Instance-wide | Per-plugin classloader | None (shared JVM) |

### BPM engines: a typed job contract beneath a declarative template

**Bonita** has the cleanest split between what a connector promises and what it does. A definition "specifies the external interfaces for input and output, including the configuration wizard", an implementation is "the Java code that acts on the inputs", and one definition can have any number of implementations chosen when the process is configured ([Bonita 2026.1 connectivity](https://documentation.ofelia.com/bonita/2026.1/process/connectivity-overview)). The build emits `.def` and `.impl` files that each carry an id and version. The implementation names its `definitionId`/`definitionVersion`, main class and dependencies ([Bonita connector archetype](https://documentation.ofelia.com/bonita/2026.1/process/connector-archetype)). The runtime contract is weak. Connectors run inside the engine on their own thread pool, and a runaway Groovy loop can permanently occupy an executor thread even after the timeout releases the task ([Bonita connectors execution](https://documentation.ofelia.com/bonita/2024.3/runtime/connectors-execution)). Bonita's packaging moved toward whole archives: **"Live update APIs have been deprecated"**, and SCA packaging has been recommended since 2023.2 ([Bonita build for deployment](https://documentation.ofelia.com/bonita/2026.1/build-a-process-for-deployment)). The Bonita processes Pli is about to absorb therefore carry connectors that are, by design, in-process Java bound to a definition id. Migrating them means re-implementing each definition against Pli's contract, which is an argument for making that contract stable before the migration starts.

**Camunda 8** is the strongest BPM precedent because it already made the in-process-to-remote move. Zeebe "is always a remote resource for your application". Glue code moved from Spring-bean Java delegates to workers annotated `@JobWorker(type = "payment")`. Engine plugins were removed because they "can massively change the behavior or even harm the stability of the engine" ([Camunda conceptual differences](https://docs.camunda.io/docs/guides/migrating-from-camunda-7/conceptual-differences/)). The contract is the job:

- Workers activate jobs with a timeout and a list of variables to fetch.
- **Complete** merges variables into the process instance.
- **Fail** carries remaining retries. At zero it raises an incident that waits for a human.
- A timed-out job is reassigned without consuming a retry.
- Delivery is at least once, so "worker code must be idempotent".
- A newer `withLease` mode issues a lease token that complete and fail must present, which rejects stale workers safely.

([Camunda job workers](https://docs.camunda.io/docs/components/concepts/job-workers/)). Business errors are a separate verb that "reports a business error (non-technical)" and routes to BPMN error handling ([Camunda throw job error](https://docs.camunda.io/docs/apis-tools/orchestration-cluster-api-rest/specifications/throw-job-error/)).

Above the job sits the descriptor layer:

- A Connector SDK jar is discovered by Java SPI and identified by a versioned type string such as `io.camunda:template:1`.
- A JSON element template carries a required `id`, a strongly recommended integer `version` that forms an upgrade path the modeler suggests, `engines` semver ranges, and a `deprecated` flag.
- Recommended `camunda.secrets.*` references are resolved by the orchestration cluster "before the job reaches the connector".

([Connector SDK](https://docs.camunda.io/docs/components/connectors/custom-built-connectors/connector-sdk/); [template metadata](https://docs.camunda.io/docs/components/modeler/element-templates/template-metadata/)).

**Flowable** offers the same out-of-process option through External Worker tasks. Workers acquire and lock jobs by topic, with candidate users and groups restricting who sees a job, in/out mappings limiting the variables exchanged, and an exclusive flag preventing concurrent jobs per instance ([Flowable External Worker Task](https://documentation.flowable.com/latest/model/cmmn/reference/external-worker-task)). It ships a Go client whose handler returns success, fail, bpmnError or cmmnTerminate ([flowable-external-client-golang](https://pkg.go.dev/github.com/flowable/flowable-external-client-golang)). Its four-way result is almost exactly the outcome enum Pli needs.

**Temporal** has no package concept at all. Code ships as worker binaries polling task queues. Upgrades of running workflows go through Worker Deployments. **Pinned** workflows "are guaranteed to complete on a single Worker Deployment Version". **Auto-Upgrade** workflows move to new code and need patching. Rolling deploys are incompatible, so blue-green or rainbow deployment is required ([Temporal worker versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning)). **Nexus** adds the only open, versioned HTTP contract in this group. The MIT-licensed Nexus RPC spec defines:

- `POST /{service}/{operation}`, which returns 200 for a sync result, 201 with an operation token for async, or 424 when the operation failed.
- An idempotent cancel endpoint.
- `Operation-Timeout` headers.
- A structured failure object that separates operation errors from handler errors, with retryable overrides.

It also states plainly that "there's no enforced security for callback URLs at this time" ([Nexus RPC SPEC.md](https://github.com/nexus-rpc/api/blob/main/SPEC.md)). Sync Nexus operations must finish within a 10-second handler deadline, and async ones can run up to 60 days ([Temporal Nexus](https://docs.temporal.io/nexus)).

None of the four BPM and durable-execution platforms ships a package manager with inter-package dependency resolution. Their "package" is an id plus a version on a descriptor, bundled into an application archive at most.

### Trackers and ITSM: two tiers, and the trend toward hosting code

Every work tracker studied offers **two tiers**. The first is a no-package HTTP path. The second is a package that bundles declarative definitions with platform-hosted code.

Jira Automation's incoming webhook is the purest form of the first tier. Enabling a rule yields a URL and a secret sent in `X-Automation-Webhook-Token`. The token never expires, and regenerating it invalidates the old one immediately ([Atlassian incoming webhook](https://support.atlassian.com/cloud-automation/docs/configure-the-incoming-webhook-trigger-in-atlassian-automation/)). Even this path had a forced migration. Flows created before 28 January 2025 had to be migrated **by hand, one at a time, with no bulk tooling** before 30 May 2025 ([same](https://support.atlassian.com/cloud-automation/docs/configure-the-incoming-webhook-trigger-in-atlassian-automation/); [Atlassian Community](https://community.atlassian.com/t5/Automation-articles/Update-on-Incoming-Webhooks-Trigger-for-Atlassian-Automation/ba-p/2921233)). This is a small lesson for Pli. An unversioned, unsigned endpoint scheme is cheap to ship and expensive to change.

The package tier looks similar everywhere:

- **YouTrack** apps carry a `manifest.json` (name as identity, major.minor.bugfix version, min/max YouTrack version), a settings schema with secret settings, entity extensions declaring custom properties, widgets, and backend JS modules classified by export. Workflow rules, HTTP handlers and AI tools are all in that last group ([YouTrack app package](https://www.jetbrains.com/help/youtrack/devportal/app-overview.html.md); [manifest](https://jetbrains.com/help/youtrack/devportal/app-manifest.html)). The manifest's `minYouTrackVersion` is backward-compatible with an older `workflow-scripting-api` field, which shows standalone workflows being folded into the app format over time.
- **ServiceNow** spokes are "essentially a scoped application" grouping actions for one external system. Out-of-the-box spokes cannot be customised directly, only cloned or wrapped. Spokes can be generated from an OpenAPI spec or a Postman collection ([ServiceNow Community](https://www.servicenow.com/community/automation-engine-blog/integration-hub-spokes-and-how-to-build-them/ba-p/2788254); [ServiceNow docs](https://www.servicenow.com/docs/r/HGWDpzjgkfvaJWTdMzNy2w/YeT8ViZ88siNlNLsOHqL9g)).
- **Forge** runs code "inside Atlassian's secure environment" with tenant isolation, and publishes a precise limits table:
  - 55-second invocations, extendable to 900 s for async events.
  - 25 s for UI modules invoked through Forge Remote, 5 s for events invoked through it.
  - 512 MB to 1 GB memory and 5 MB payloads.
  - Per-installation rate limits with an `X-Ratelimit-Reset` header.

  ([Forge invocation limits](https://developer.atlassian.com/platform/forge/limits-invocation/)).

Read across these four platforms, the hook contract Pli needs is the part every platform has. A package tier exists where the vendor wants to host and sandbox the code.

### Integration platforms and infrastructure: in-process versus subprocess

The integration platforms split along the line that matters for Pli.

- **n8n** and **Activepieces** load npm packages into the host runtime. n8n's own docs say community nodes "have full access to the machine that n8n runs on, and can do anything, including malicious actions" ([n8n risks](https://docs.n8n.io/integrations/community-nodes/risks)).
- **Zapier** hosts a schema-validated `App` object on Node.js 22. The object carries both `version` and `platformVersion`, so the integration's own version and the runtime contract version are separate fields ([Zapier CLI](https://docs.zapier.com/platform/build-cli/overview)).
- **Windmill** runs scripts in per-job worker processes and makes integrations pure data. "There are no vendor-branded connectors - the resource type is the integration", and a resource type is just a JSON Schema ([Windmill resources](https://www.windmill.dev/docs/core_concepts/resources_and_types)).

The infrastructure ecosystems chose subprocesses deliberately.

- **Terraform**: plugins "are executed as a separate process and communicate with the main Terraform binary over an RPC interface". The protocol itself is versioned separately from providers. Protocol 6 works with CLI 1.0 and later, protocol 5 with 0.12 and later, and the registry publishes protocol version as compatibility metadata ([Terraform plugins](https://developer.hashicorp.com/terraform/plugin/how-terraform-works); [plugin protocol](https://developer.hashicorp.com/terraform/plugin/terraform-plugin-protocol)).
- **HashiCorp go-plugin**: "Plugins can't crash your host process." Go's `plugin` stdlib has "a number of limitations", and dynamic loading "is not acceptable for security reasons" for Vault. HashiCorp concedes shared libraries are faster but says it has not needed the throughput ([go-plugin](https://github.com/hashicorp/go-plugin)).
- **Grafana** builds backend plugins on go-plugin. The core launches each one as a subprocess exposing a fixed set of capability RPCs: QueryData, CallResource, CheckHealth, CollectMetrics and streaming. Each request carries full configuration, so plugins stay stateless ([Grafana backend plugins](https://grafana.com/developers/plugin-tools/key-concepts/backend-plugins/)).

For a Go kernel the inference is direct. Go has no good in-process plugin story, and Pli already runs connectors as Temporal activities, which are an out-of-process boundary. Moving third-party code out of Pli's worker binary costs Pli far less than it cost Camunda or Kubernetes.

## In-process connectors are the regret every ecosystem pays down

The strongest evidence in this research is not what platforms do today but what they spent years undoing.

**Kubernetes** started moving cloud providers out of tree around v1.7 and permanently removed them in v1.31. The migration removed "roughly 1.5 million lines of code", cut core binary sizes by about 40%, and each replacement subsystem "required several releases" to reach GA ([Kubernetes blog, May 2024](https://v1-33.docs.kubernetes.io/blog/2024/05/20/completing-cloud-provider-migration)). CSI was introduced in 1.9 specifically so storage vendors could ship "without the need to add to the core Kubernetes codebase" ([Kubernetes CSI blog](https://kubernetes.io/blog/2018/01/introducing-container-storage-interface/)).

**Camunda** listed the costs of embedded engines in its own words:

- "No isolation" between engine and application.
- Mixed dependencies that cause version conflicts.
- Patch updates that force a full app rebuild.
- "Embedded engines can support just one programming language."

It also accepted the costs of going remote: no shared ACID transactions, less convenient remote calls, and harder unit tests ([Camunda blog, 2022](https://camunda.com/blog/2022/02/moving-from-embedded-to-remote-workflow-engines/)). This is a vendor post, and its benefit claims are not independently measured.

**Jenkins** documents the end state of a shared-JVM plugin model. Its class-loading guidance includes:

- Plugins cannot supply classes to each other.
- Transitive Maven dependencies cause linkage errors.
- Library-wrapper plugins carry the major version in their artifactId.
- `JenkinsRule` tests can hide class-loading bugs, so a separate-JVM `RealJenkinsRule` is recommended.

([Jenkins class loading](https://jenkins.io/doc/developer/plugin-development/dependencies-and-class-loading/)).

**Backstage** statically compiles plugins into the application. Red Hat had to build "dynamic plugins", loaded from npm, tarballs or OCI images and declared in YAML, so customers could add plugins without rebuilding. Compatibility friction remains: plugins must target the right Backstage version ([Red Hat Developers, Nov 2025](https://developers.redhat.com/articles/2025/11/20/how-build-your-dynamic-plug-ins-developer-hub)).

The security case is the sharpest. In January 2026 a malicious npm package posing as a Google Ads node stored OAuth tokens normally. At runtime it called `this.getCredentials('googleAdsOAuth2Api')`, which "decrypts the credentials using n8n's master key", and exfiltrated the tokens to an attacker-controlled host. Endor Labs listed nine malicious packages ([Endor Labs](https://www.endorlabs.com/learn/n8mare-on-auth-street-supply-chain-attack-targets-n8n-ecosystem); [The Hacker News](https://thehackernews.com/2026/01/n8n-supply-chain-attack-abuses.html)). n8n responded with policy rather than isolation. Verified nodes must:

- Have no external dependencies.
- Never touch environment variables or files.
- Be published with GitHub-Action provenance, mandatory from 1 May 2026.

([n8n verification guidelines](https://docs.n8n.io/connect/create-nodes/build-your-node/reference/verification-guidelines)). Its actual isolation investment, task runners, covers only user-written Code-node scripts ([n8n task runners](https://docs.n8n.io/hosting/configuration/task-runners/)).

Activepieces and Windmill make sandboxing an operator choice and document its price:

- **Activepieces**: privileged containers or `CAP_SYS_ADMIN`, and cold boots per run ([Activepieces sandboxing](https://www.activepieces.com/docs/install/architecture/sandboxing)).
- **Windmill**: nsjail is disabled by default, and its 4 GiB address cap breaks Bun ([Windmill isolation](https://www.windmill.dev/docs/advanced/security_isolation)).

The lesson is that a verification program approximates, through code review, the boundary that a process boundary enforces for free.

### The Connect-to-Forge counterexample does not transfer to Pli

Atlassian moved the *other* way. It is replacing Connect, a pure remote-HTTP model, with Forge, a hosted package model. The reasons it gave:

- Trust, tenant isolation and data residency. Apps on Atlassian-hosted storage earn a "Runs on Atlassian" badge described as "a strong trust signal valued by customers".
- Connect's "tight coupling with our products".

([Atlassian, Jan 2025](https://www.atlassian.com/blog/developer/taking-the-ecosystem-forward-an-update-on-the-future-of-connect)).

The cost was large:

- In January 2025 Atlassian said it did "not plan to retire Connect as a whole at this stage". By March it had published a three-phase end-of-support timeline ([Atlassian, Mar 2025](https://www.atlassian.com/blog/developer/announcing-connect-end-of-support-timeline-and-next-steps)).
- In August 2026 end of support slipped to **31 January 2027** ([Atlassian Community](https://community.developer.atlassian.com/t/connect-end-of-support-extended-to-january-31-2027/102002)).
- Partners called the timeline "unreasonable and unrealistic short".
- One partner reported five Forge bugs or incidents across two of fifteen apps in early 2025.
- Partners complained there were "no means for developers to update or notify customers" across Forge major versions ([Atlassian Community thread](https://community.developer.atlassian.com/t/announcing-connect-end-of-support-timeline-and-next-steps/90381?page=2)).

Two things make this a non-precedent for Pli's current decision.

**The driver does not apply.** Atlassian's trigger was multi-tenant SaaS trust and residency, not anything Connect apps could not do over HTTP. A self-hosted open-source kernel whose first customer writes its own connectors has no "Runs on Atlassian" problem to solve.

**The remote option survived.** Atlassian kept remote services as a permanent option inside the package: "Forge remote is not going away." The Forge manifest was designed so existing Connect backends could be wrapped rather than rewritten ([Atlassian, Jan 2025](https://www.atlassian.com/blog/developer/taking-the-ecosystem-forward-an-update-on-the-future-of-connect)).

The transferable lesson is about sequencing. If Pli ever adds a package layer, it should *wrap* existing hook endpoints through a versioned descriptor, the way Forge wraps Connect backends. That is only cheap if the descriptor exists and is versioned from the start.

### The steelman for packages now

The best argument for building a package system early is that Pli is about to be open-sourced. Open-source extension ecosystems grow fast, and namespace collisions and dependency tangles are far harder to fix after the fact. Power Platform warns that you must set a publisher prefix first, because "you can't change the names of metadata items after they're created" ([Microsoft Learn](https://learn.microsoft.com/en-us/power-platform/alm/solution-concepts-alm)). Salesforce documents namespace-collision guidance even for "namespace-less" unlocked packages ([Salesforce](https://developer.salesforce.com/docs/platform/sfdx-dev/guide/sfdx-dev-unlocked-pkg-namespace-collisions.html)).

This argument is right about **namespaces** and wrong about everything else. Mandatory namespaces cost one field in a YAML file and should ship now. Dependency resolution, a registry and code hosting are costly and premature, and even mature ecosystems retreat from them. OLM v1 is "a comprehensive redesign" toward "less rigid automation". It supports only a subset of v0 content and offers no concrete migration path ([OLM v1](https://operator-framework.github.io/operator-controller/)).

## Package systems earn their keep on definitions, not code

Where package systems do pay off, they protect **declarative definitions that existing data depends on**, and they converge on the same four rules.

**1. Namespaces are owned and immutable.** ServiceNow ties vendor prefixes to ownership ([ServiceNow Community](https://www.servicenow.com/community/developer-forum/you-do-not-own-the-vendor-prefix-for-this-scope/td-p/3126691)). A Power Platform publisher owns its components, and ownership cannot move across publishers ([Microsoft Learn](https://learn.microsoft.com/en-us/power-platform/alm/solution-concepts-alm)).

**2. Adding is easy, removing is hard.** Power Platform's **update** and **patch** cannot delete components, while **upgrade** deletes components missing from the new version ([Microsoft Learn](https://learn.microsoft.com/en-us/power-platform/alm/solution-concepts-alm)). Salesforce marks removed components deprecated rather than deleting them in subscriber orgs. A later reuse of the same API name can break subscriber upgrades, so Salesforce advises recording removed names so they are never reused ([Salesforce removal considerations](https://developer.salesforce.com/docs/platform/pkg2-dev/guide/sfdx-dev-dev2gp-md-removal-considerations.html)).

**3. Dependencies are enforced at install and uninstall.** A dependent solution cannot install before its base, and the base cannot be removed while dependents remain ([Microsoft Learn](https://learn.microsoft.com/en-us/power-platform/alm/solution-layers-alm)). Salesforce's 2GP matrix forbids managed-2GP-on-1GP dependencies and advises small modular packages ([Salesforce 2GP dependencies](https://developer.salesforce.com/docs/atlas.en-us.264.0.pkg2_dev.meta/pkg2_dev/sfdx_dev_dev2gp_dependency_overview.htm)).

**4. One channel per artifact.** ServiceNow warns that deploying one app through both update sets and the repository causes "skipped changes, commit errors, and version tracking failures". It also says installed apps cannot be edited on production, so every change needs a new published version ([ServiceNow app repo](https://www.servicenow.com/docs/r/de-DE/application-development/building-applications/store-private-application-repositories.html)).

Pli's YAML Types are exactly the kind of artifact these rules protect. Items hold data against Type fields, so deleting a field from a Type is the same problem as Salesforce's deprecated `project__c`. These rules belong in Pli's bundle format whether or not a package manager ever exists.

### Running instances: pin the definition, migrate explicitly, never upgrade a shared copy

The platforms that handle in-flight work well treat versions as immutable and move consumers explicitly.

- **Camunda** leaves instances on their deployed version. Migration is transactional ("all active elements or nothing") and must map every active element ([Camunda migration](https://docs.camunda.io/docs/components/concepts/process-instance-migration/)).
- **Zapier** migrates active Zaps by percentage within a major and refuses cross-major migration. It blocks promotion of a version with breaking schema changes. Lifecycle states (Promoted, Legacy, Deprecating) drive migration, and a deprecation date must be at least three weeks out ([Zapier migrate](https://docs.zapier.com/platform/manage/migrate); [Zapier automatic migrations](https://docs.zapier.com/integrations/manage/automatic-migrations)).
- **Windmill** identifies script versions by hash and keeps the previous hash deployed ([Windmill versioning](https://www.windmill.dev/docs/core_concepts/versioning)).
- **Terraform** pins provider versions in a lock file until `init -upgrade` ([Terraform](https://developer.hashicorp.com/terraform/plugin/how-terraform-works)).

The anti-pattern is n8n. It keeps one copy of a node per instance, and "upgrading can break every workflow that uses the node" ([n8n risks](https://docs.n8n.io/integrations/community-nodes/risks)). Activepieces gives the rule that makes "within a major" checkable by machine: "Any removal is breaking, any new required addition is breaking" ([Activepieces versioning](https://www.activepieces.com/docs/build-pieces/piece-reference/piece-versioning.md)). None of the tracker and ITSM sources described what happens to in-flight flows on app upgrade, so Pli has no model to copy from that family.

Pli's current architecture makes this the most concrete deciding factor. Connectors compiled into the Temporal worker ship with the worker binary. Under Worker Versioning, a Pinned workflow stays on its Worker Deployment Version, so a connector bug fix in a new build does not reach Items already in flight there. Auto-Upgrade instead forces patching discipline on workflow code that never changed ([Temporal worker versioning](https://docs.temporal.io/production-deployment/worker-deployments/worker-versioning)). Activities are not replayed, so a connector reached over a protocol can be redeployed independently of the workflow version. **The hook protocol decouples connector releases from workflow versioning.** That matters most during a migration from Bonita, when connector fixes will be frequent.

## The Pli hook protocol v1, concretely

The protocol should be small, transport-light and assembled from existing specs, so connector authors in any language can use existing libraries. It has one envelope with three hook kinds. It has two bindings: plain HTTPS, and the existing Go interface for built-in connectors. Both bindings share the same descriptor format.

### Built-ins and remote connectors implement the same interface

The current Go connectors (HR directory, ERP, CRM, email) should be refactored behind a Go interface whose request and response types *are* the protocol's envelope: `Handle(ctx, HookRequest) (HookResponse, error)`. A built-in then becomes a connector that happens to be compiled in, and a Type cannot tell the difference. This is the Kubernetes in-tree/out-of-tree distinction designed in from the start. The company-specific HR and ERP adapters should move out of tree *before* open-sourcing. The open-source kernel then ships only generic connectors (HTTP, email, webhook), and the company's adapters become the first real users of the protocol.

For customer networks that cannot accept inbound HTTPS, a third binding can come later: a customer-hosted worker polling a tenant task queue, Flowable/Camunda style. That binding should reuse the same envelope.

### Request envelope

Every call carries one JSON envelope. Its identity fields follow CloudEvents, and its admission fields follow Kubernetes. CloudEvents requires `id`, `source`, `specversion` and `type`, and says `source` + `id` must be unique per distinct event and may be treated as duplicates when resent ([CloudEvents spec](https://github.com/cloudevents/spec/blob/main/cloudevents/spec.md)). AdmissionReview responses must echo the request `uid` and set `allowed`, and `dryRun` requests must not actuate side effects ([Kubernetes dynamic admission control](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)). The full AdmissionReview field list was not verified against a complete page.

```json
{
  "protocol": "pli.hooks/v1",
  "kind": "check",                       // enrich | check | action
  "requestId": "01JD…",                  // unique per attempt; echoed in response
  "idempotencyKey": "h:9f3c…",           // hash(tenant, hookBindingId, causationEventId); stable across retries
  "attempt": 2,
  "deadline": "2026-11-22T10:15:05Z",    // absolute; connector must stop work after it
  "dryRun": false,
  "hook": { "binding": "acme.hr/onboarding@3#verify_manager", "connector": "acme.hr-directory@2", "operation": "verify_manager" },
  "event": { "id": "evt_…", "source": "pli://tenant/acme", "type": "item.transition.requested", "time": "…", "subject": "item/ONB-812" },
  "actor": { "principal": "user:jdoe", "onBehalfOf": null, "agentSession": null },
  "item": { "id": "ONB-812", "type": "acme.hr/onboarding", "typeVersion": 3,
            "old": { "state": "draft", "fields": { … } },
            "new": { "state": "submitted", "fields": { … } } },   // only fields the descriptor's inputSchema names
  "transition": { "from": "draft", "to": "submitted" },
  "trace": { "traceparent": "00-…", "correlationId": "…", "causationId": "evt_…" },
  "connection": { "slot": "hr", "ref": "conn_hr_prod" }          // a reference, never a secret
}
```

Three choices are deliberate.

**`item` is a full snapshot for check and enrich, restricted to declared input fields.** A synchronous decision must be made against exactly the state being transitioned, as Kubernetes does. The field restriction mirrors Camunda's `fetchVariables` and keeps payloads small. Action hooks may receive a thin payload plus a kernel read API, following the thin-versus-snapshot choice that Stripe and Standard Webhooks both offer ([Stripe webhooks](https://docs.stripe.com/webhooks); [Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md)).

**The idempotency key is derived, not random.** Temporal recommends Workflow Run ID plus Activity ID as the key because both are stable across retries ([Temporal activity definition](https://docs.temporal.io/activity-definition)). Pli's equivalent is binding plus causing event. The key must contain no personal data, which Stripe warns against ([Stripe idempotency](https://docs.stripe.com/api/idempotent_requests)).

**Credentials never travel in v1.** The connection is a reference. A trusted, self-hosted connector service resolves its own external credentials from its own secret store. The n8n attack worked because plugin code shared a process with the master key. Keeping secrets out of the envelope keeps the blast radius of one compromised connector at that connector's own credentials. Kernel-brokered short-lived tokens, the Grafana per-request-config style, are a v2 extension for when Pli hosts Connections centrally.

### Response envelope per hook kind

The response separates **business outcomes**, which are never retried and never subject to failure policy, from **technical failures**, which are retried and then handled by policy. Kubernetes states the rule: "An explicit rejection, correctly transmitted, always denies the API request, regardless of the `failurePolicy` setting" ([Kubernetes](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)). MCP draws the same line between protocol errors and tool execution errors, which carry `isError: true` ([MCP tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)). Camunda draws it between fail and throw-error.

| Kind | Allowed `outcome` values | Payload on success | Kernel behaviour |
|---|---|---|---|
| **check** | `allow`, `deny`, `pending` | `reason{code,message,details}` on deny; `warnings[]` | `deny` blocks the transition and shows the reason. `pending` places a visible hold on the Item. Runs before commit of the target state; must have no side effects. |
| **enrich** | `ok`, `business_error`, `pending` | `patch`: field map limited to the descriptor's `outputSchema` | Kernel validates the patch against declared outputs, rejects anything outside, and writes it as an attributed update. Must be idempotent. |
| **action** | `ok`, `business_error`, `pending` | optional `outputs` (declared), `externalRefs[]` | Runs only after the transition commits, never inside a check phase. `business_error{code}` routes to a Type-declared error transition. |

All three kinds also return `requestId` (echoed), optional `warnings[]` capped in length the way Kubernetes caps warnings at 120 characters each, and on technical failure an RFC 9457 `application/problem+json` body. The rule that actions run only post-commit follows Kubernetes guidance. Webhooks with out-of-band side effects "must also have a reconciliation mechanism", because another admission step may reject the request later ([Kubernetes](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)). In Pli, a check can be followed by another hook's deny, so side effects belong after commit.

### Error classes and HTTP mapping

| Class | Signalled by | Retried? | Notes |
|---|---|---|---|
| `deny` / `business_error` | 200 with `outcome` | Never | Business outcome; failure policy does not apply |
| `transient` | 500, 502, 503, network error | Yes, exponential backoff within budget | Default Temporal policy is 1 s initial, ×2, max 100× ([Temporal retry policies](https://docs.temporal.io/encyclopedia/retry-policies)) |
| `rate_limited` | 429 + `Retry-After` | Yes, honour header | Standard Webhooks treats 429/502/504 as throttle signals ([Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md)) |
| `timeout` | Deadline exceeded | Yes | Recorded as "may have executed"; safe only because of the idempotency key |
| `in_flight` | 409 | Yes, after delay | IETF Idempotency-Key draft: same key still processing ([IETF draft-09](https://datatracker.ietf.org/doc/draft-ietf-httpapi-idempotency-key-header/)) |
| `key_conflict` | 422 | No | Same key reused with a different payload; indicates a kernel bug |
| `invalid` | 400, schema validation failure | No | Permanent; goes straight to failure policy |
| `config` | 401, 403 | No | Connection marked `needs_reauth`; owner notified |
| `gone` | 410 | No | Disable the binding, following Standard Webhooks |
| redirect | 3xx | No | Treated as failure; never followed, as Stripe and Standard Webhooks both advise |

A problem body may carry `retryable: false` to override the class. The Nexus failure object has the same override, and Temporal's `non_retryable` flag, which its docs say to use "sparingly", plays the same role ([Nexus SPEC](https://github.com/nexus-rpc/api/blob/main/SPEC.md); [Temporal retry policies](https://docs.temporal.io/encyclopedia/retry-policies)).

### Timeouts and failure policy live in the Type, not the response

What happens when a call fails or times out is declared per hook binding in the Type YAML and is never inferred from the response. Kubernetes sets `failurePolicy` to `Fail` by default, with a timeout of 1 to 30 seconds that defaults to 10 ([Kubernetes](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)). Its good-practices page nonetheless warns that rejecting by default "might result in compliant requests being rejected during webhook downtime", and recommends that mutating webhooks fail open ([Kubernetes good practices](https://kubernetes.io/docs/concepts/cluster-administration/admission-webhooks-good-practices/)). Envoy's ext_authz defaults to fail-closed with a 200 ms timeout. When configured to fail open, it stamps the request with `x-envoy-auth-failure-mode-allowed` and counts it ([Envoy ext_authz](https://www.envoyproxy.io/docs/envoy/latest/api-v3/extensions/filters/http/ext_authz/v3/ext_authz.proto)).

| Kind | Sync timeout default (cap) | Default failure policy | Allowed alternatives |
|---|---|---|---|
| check | 5 s (30 s) | `fail` (closed): transition refused with "check unavailable" | `ignore` with an audit mark on the Item, the Envoy header equivalent |
| enrich | 10 s (30 s) | `fail` → incident on the hold | `degrade(field := value)`, `ignore` (audited) |
| action | 30 s sync, else must return `pending` | retry within budget → **incident** with retry / skip / rerun | `raise(code → transition)`; `ignore` forbidden for outbound writes |

The retry budget should be expressed as total elapsed time, as Temporal recommends Schedule-to-Close over MaximumAttempts, not as an attempt count. Exhausted retries become an operator-visible incident, the way Camunda raises an incident at zero retries, and are never dropped. `retry` reuses the idempotency key, while `rerun` mints a new one and is audited, because a rerun can create a second invoice. A `matchConditions`-style CEL pre-filter on each binding avoids network calls that would be no-ops. Kubernetes allows up to 64 such conditions per webhook.

### Auth and replay protection: adopt Standard Webhooks in both directions

Pli should adopt Standard Webhooks v1.0.0 signing verbatim instead of inventing a scheme. The headers are `webhook-id`, which stays stable across retries, `webhook-timestamp` and `webhook-signature`. Signatures are HMAC-SHA256 over `id.timestamp.body` (`v1`), or ed25519 (`v1a`), which the spec prefers. Multiple signatures can be sent during key rotation ([Standard Webhooks](https://github.com/standard-webhooks/standard-webhooks/blob/main/spec/standard-webhooks.md)). Connector authors then get existing verification libraries in most languages.

Receivers enforce a **five-minute timestamp tolerance**, which Slack enforces at 300 seconds and Stripe's libraries use as their default. They compare in constant time, verify against the raw body, and ignore unknown signature schemes, which Stripe says prevents downgrade attacks ([Slack](https://docs.slack.dev/authentication/verifying-requests-from-slack); [Stripe webhooks](https://docs.stripe.com/webhooks)). They also dedupe on `webhook-id` within the tolerance window.

The same scheme signs the **callback direction**. Nexus RPC leaves callback URLs unauthenticated, and Pli should not. Async task IDs are bound to the connector identity that created them, as MCP requires tasks to be bound to their authorization context ([MCP tasks](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks)).

mTLS or mesh workload identity is a deployment-level alternative, as Slack offers mTLS beside signing. Connector URLs configured by admins pass through an SSRF-filtering egress proxy, as Standard Webhooks advises producers.

### Async completion and holds

A hook that cannot answer within its sync deadline returns **202** with this body:

```json
{ "outcome": "pending", "taskId": "…", "pollAfter": "PT30S", "expiresAt": "…", "message": "Awaiting HR approval" }
```

The connector then completes in one of two ways: a signed `POST /hooks/v1/tasks/{taskId}/complete` carrying the original idempotency key and a normal response envelope, or kernel polling of `GET {connector}/tasks/{taskId}`. A `heartbeat` endpoint can extend `expiresAt` up to the binding's declared `maxHold`, and an idempotent `cancel` endpoint follows Nexus. These patterns come straight from existing systems:

- **Temporal async completion.** The activity returns `ErrResultPending` and an external system later calls `CompleteActivity`. Because "retries can invalidate a Task Token", Temporal recommends completing by Activity ID and Workflow ID ([Temporal async completion](https://docs.temporal.io/develop/go/asynchronous-activity-completion); [Temporal activity execution](https://docs.temporal.io/activity-execution)). Pli's callback handler should therefore call `CompleteActivityByID` and never hand raw task tokens to connectors.
- **MCP tasks.** A pending task carries status, TTL and a poll interval, and terminal statuses never change ([MCP tasks](https://modelcontextprotocol.io/specification/2025-11-25/basic/utilities/tasks)).
- **Camunda.** A second completion gets `NOT FOUND`. Pli should make a second callback a no-op and reject callbacks after the hold expired or was cancelled, recording the discard as an event rather than applying stale data.

A check hook returning `pending` *is* the "hold a transition" case. It becomes the same visible hold, completed by a token, that Pli uses for approvals, with a declared `maxHold` analogous to Schedule-to-Close.

### Versioning the protocol, the connector and the Item schema separately

There are three version axes, and they must never be conflated.

**Protocol version.** `pli.hooks/v1` carries major and minor only, as CloudEvents' `specversion` does. Connector descriptors declare `supportedProtocolVersions` in preference order, and the kernel picks the first one it supports. This mirrors Kubernetes' required `admissionReviewVersions` list, under which webhooks must support a version known to the current and previous API server ([Kubernetes](https://kubernetes.io/docs/reference/access-authn-authz/extensible-admission-controllers/)). Within a major version, changes are additive only, and both sides must ignore unknown fields, as Stripe requires of signature-header entries ([Stripe webhooks](https://docs.stripe.com/webhooks)).

**Connector version.** Each connector has its own semver. Type bindings pin `name@major`, and implementations float within that major, Camunda-style. Activepieces' breaking-change rules are enforced by a lint at bundle publish.

**Item schema version.** The Type version pins the payload shape a binding was written against, so a connector written for `onboarding@3` keeps receiving `onboarding@3` Items. This is Stripe's practice of pinning event versions per destination.

### Connector descriptors

Each connector serves a descriptor at `GET /.well-known/pli-connector`, and the same file is vendored into the bundle so a bundle can be reviewed offline. The shape borrows MCP's tool definition (`name`, `title`, `description`, `inputSchema` in JSON Schema 2020-12, optional `outputSchema`, `execution.taskSupport`) ([MCP tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools)). It adds Kubernetes-style `sideEffects`, and Grafana-style fixed capabilities (`describe`, `health`) alongside the hook operations.

```yaml
connector: acme.hr-directory        # namespace.name; namespace = bundle owner
version: 2.4.1
supportedProtocolVersions: [pli.hooks/v1]
connectionSchema: { $ref: "#/schemas/HrConnection" }   # what an admin configures; secrets by reference
operations:
  - name: verify_manager
    kinds: [check]
    inputSchema:  { type: object, properties: { manager_email: { type: string } }, required: [manager_email], additionalProperties: false }
    sideEffects: none
    async: forbidden                 # forbidden | optional | required
    errors: []                       # declared business error codes
  - name: lookup_employee
    kinds: [enrich]
    inputSchema:  { … }
    outputSchema: { properties: { cost_center: {type: string}, department: {type: string} } }
    sideEffects: none
    async: optional
  - name: provision_account
    kinds: [action]
    inputSchema:  { … }
    outputSchema: { properties: { account_id: {type: string} } }
    sideEffects: some
    async: required
    errors: [EMPLOYEE_NOT_FOUND, LICENSE_EXHAUSTED]
```

Using JSON Schema 2020-12 has a side benefit. Each operation is trivially exposable as an MCP tool for agent sessions, and Windmill shows that much connector configuration can stay pure data.

### Testing support ships with the protocol

A protocol boundary is the natural mock seam, and the Jenkins lesson is that tests at the real boundary are more faithful than in-process harnesses. Pli should ship four pieces:

1. **A CLI.** `pli hooks call` sends signed fixture envelopes to a local connector, and `pli hooks serve-mock` plays the kernel side for callback testing. This follows Stripe's `listen --forward-to` and `trigger` ([Stripe webhooks](https://docs.stripe.com/webhooks)).
2. **A conformance suite.** Every connector runs it against its own descriptor. It checks request and response schemas, signature verification and rejection of stale timestamps, identical responses for a replayed idempotency key, no side effects under `dryRun`, and async completion with late-callback rejection.
3. **Golden fixtures** per operation, kept in the bundle.
4. **A delivery log** in the kernel with per-attempt status and manual replay, as Stripe, GitHub and Standard Webhooks all recommend.

For built-in Go connectors, Temporal's test environment already provides signature-checked activity mocks, time skipping for holds, and replay tests that should fail CI ([Temporal Go testing](https://docs.temporal.io/develop/go/testing-suite)).

## What "a package" means for Pli now, and what would justify more

For now, a Pli package should be a **bundle**: a git directory that is the single source of truth for one namespace. It is applied with `pli apply` and contains no executable code that the kernel loads.

```text
acme-hr/
  bundle.yaml          # name: acme.hr, version: 1.3.0, pli: ">=0.9 <2", protocols: [pli.hooks/v1]
  types/onboarding.yaml
  types/offboarding.yaml
  connectors/hr-directory.descriptor.yaml   # vendored from /.well-known; pinned by digest
  connectors/endpoints.yaml                  # per-environment URLs, slot→connection bindings
  fixtures/…            # golden envelopes for conformance tests
  bundle.lock          # resolved connector descriptor digests and (optional) OCI image digests
```

The bundle carries the rules that package systems paid to learn. The rules, and the precedent each comes from:

- **Mandatory namespace prefix** on Type, field and connector names, owned by the bundle (Power Platform, ServiceNow).
- **One channel per namespace**: a namespace managed by a bundle cannot also be edited in the UI on production (ServiceNow).
- **`apply` is additive by default.** Removing a Type, field or operation requires an explicit `--allow-removals` that marks the element deprecated, keeps existing Items readable, and records the name as reserved forever (Power Platform update vs upgrade; Salesforce removed names).
- **Version bounds** for Pli and for protocol majors (YouTrack min/max, Camunda `engines`).
- **A lockfile** pinning descriptor digests, so a running connector cannot silently change its contract (Terraform).

Connector *code* in a bundle is at most an OCI image reference that the operator deploys with the tools they already use. If connectors are services, packaging collapses to images plus a manifest, as Backstage dynamic plugins and OLM bundles show. No registry, dependency solver or kernel-hosted code is needed.

This fits the migration directly. Each Bonita process becomes a Type plus bindings in the company's `acme.*` bundle. Each Bonita connector definition becomes a descriptor operation, served by one of the out-of-tree Go services. Each Jira project's Automation webhooks become `action` and `check` bindings or inbound sources.

### Triggers that would justify a real package system

Build a package system with dependency resolution, a registry and signing when any of the following becomes true. Each condition is a judgment drawn from where other platforms added package machinery, not a measured threshold.

**1. Untrusted third parties ship connector code to other organisations through a shared catalogue.** Signing, provenance and review then become necessary. n8n made publish-from-CI provenance mandatory only after malicious packages appeared.

**2. Cross-bundle Type dependencies appear in practice.** An example is a shared `core.person` or `hr.employee` Type that several bundles extend. Dependency enforcement at install and uninstall then protects data (Power Platform, Salesforce). Even then, prefer a simple "requires X >= version" check over a solver, given OLM's retreat.

**3. Pli is offered as a multi-tenant hosted service.** Atlassian's trust and data-residency drivers then apply, and a Forge-like hosted, sandboxed runtime with declared egress becomes defensible.

**4. Extensions need to run code inside Pli's UI or transaction path.** Examples are widgets, or logic too latency-sensitive for a network hop. Before building hosted code for the transaction path, exhaust the in-process, non-Turing-complete tier, CEL. Kubernetes added CEL policies so the common validations no longer needed webhooks.

**5. Bundles are installed across many independent Pli deployments and upgrades start breaking Items in flight.** At that point Zapier-style lifecycle states and staged migration within a major are worth the machinery.

## Conclusion

The question "package system or hook protocol" turns out to be a question about **which half of a package system Pli needs**. Every mature package system bundles two things: namespaced, versioned *definitions* that data depends on, and *code* that someone must host, isolate and review. Pli needs the first half immediately, because Types hold Items and names cannot be taken back. It needs the second half only when it starts hosting other people's code, and for a self-hosted kernel whose first users write their own connectors, that is a future event. The useful move is therefore to split them. A bundle format carries definitions with Salesforce-grade naming discipline, and a protocol carries code at arm's length with Kubernetes-grade failure semantics.

The less obvious implication concerns timing. The cheapest moment to impose an out-of-process contract is before the first migration, not after it. Kubernetes and Atlassian both show that removing an extension model costs years once others depend on it. Pli's Bonita migration will create the first dozen real connectors, so they should be written against `pli.hooks/v1` from day one, with the existing Go connectors refactored behind the same interface and the company-specific ones moved out of tree before the code goes public. The open uncertainty is mostly evidentiary. Several Kubernetes webhook details came from truncated pages, and no source documented how ServiceNow or Forge treat in-flight work on upgrade. Pli's own in-flight story should therefore be designed from Temporal's and Camunda's documented models rather than borrowed from the trackers.
