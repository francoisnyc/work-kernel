# Go wins the kernel; Java wins week one

**Write the kernel in Go.** But first test that choice with a two-week build of one complete slice, and switch to Java (JDK 25) if the slice goes badly. Both languages can run this platform. Temporal's Java and Go SDKs both offer every feature the kernel needs: entity workflows, timers, continueAsNew, child workflows, Updates and worker versioning. Official SDKs for Anthropic, OpenAI, Google and AWS exist in both. The decision therefore comes down to whether the product's long-term shape outweighs the founder's existing skills. The product's shape favors Go on several separate points:

- **Self-hosting:** Go ships as a single small binary, while a Spring Boot JVM process uses about **390 MB of memory at idle**.
- **Contract layer:** Go has a production Connect server, so the TypeScript UI can call the kernel directly. Java has no equivalent and usually needs an Envoy proxy in front.
- **Temporal:** Go is the SDK that gets new features first, usually **1–5 months ahead** of Java.
- **MCP:** the Go SDK already supports the newest MCP spec; the Java SDK is one spec version behind.

Java's real advantages are about people:

- the founder already knows it;
- the hiring pool is about **twice as large**;
- the agent-framework ecosystem (Spring AI, LangChain4j) is richer;
- the limited evidence suggests AI coding assistants write somewhat better Java than Go.

Two facts tip the balance toward Go. First, learning Go is a one-time cost that experience reports measure in weeks. The runtime, distribution and contract advantages pay off on every deployment for the life of the product. Second, Temporal is polyglot, so the agent workers do not have to share the kernel's language. That removes most of Java's agent-framework lead.

The evidence has real gaps:

- No credible benchmark compares Java and Go Temporal workers.
- No sourced figure exists for Go's memory use on this workload.
- The only AI-coding benchmark uses algorithm puzzles, not backend code.

## Both Temporal SDKs can carry the kernel, but Go leads

Every Temporal primitive the kernel relies on is stable in both SDKs. These are entity workflows driven by signals and Updates, durable timers, continueAsNew (which restarts a long-lived workflow with a fresh history so it doesn't grow without limit), child workflows, and Worker Deployments for safe code rollout. Their stabilization dates line up almost exactly. Worker versioning lost its experimental label in Go on 10 February 2026 and in Java on 9 February 2026. Fairness keys merged in both SDKs on the same day ([sdk-go](https://github.com/temporalio/sdk-go), [sdk-java](https://github.com/temporalio/sdk-java) git history). Neither SDK is built on Temporal's Rust core. That core underpins TypeScript, Python, .NET and Ruby, and no migration of Go or Java onto it has been announced ([sdk-core](https://github.com/temporalio/sdk-core)). Both are independent, long-lived implementations.

The difference is pace. **Go is the lead SDK** for most new features. Over the past year it had **403 commits from 80 authors**, against Java's **246 from 64**. Go's latest release, v1.51.0, came out on 10 November 2026; Java's, v1.40.0, on 29 September ([sdk-go tags](https://github.com/temporalio/sdk-go/tags), [sdk-java tags](https://github.com/temporalio/sdk-java/tags)). Several features likely to matter to this platform reached Go first.

| Feature (why it matters here) | Go | Java | Gap |
|---|---|---|---|
| External Storage (offloading large LLM payloads from history) | Mar 2026 | Jun–Sep 2026 | ~5 months |
| Standalone Activities | Feb 2026 | Apr 2026 | ~2.5 months |
| Serverless (AWS Lambda) workers | Mar 2026 | Jul 2026 | ~3 months |
| Environment config | Apr 2025 | Sep 2025 | ~6 months |
| Static determinism checker (`workflowcheck`) | Available for years | First release Feb 2026 | Years |
| Poller autoscaling | Jul 2025 | Jun 2025 | Java first |
| Task queue priority | Apr 2025 | Mar 2025 | Java first, by about a week |

Sources: git logs of [sdk-go](https://github.com/temporalio/sdk-go) and [sdk-java](https://github.com/temporalio/sdk-java). Dates are merge dates; release-note text could not be retrieved.

The more practical difference for an entity-per-item design is how many live workflows one worker can keep in memory. Temporal workers keep a "sticky cache" of recently active workflows so they can avoid replaying history. Java has historically backed each cached workflow with an operating-system thread. As a result, **Java defaults to 600 cached workflows and 200 task slots, while Go defaults to 10,000 and 1,000** ([Temporal worker tuning reference](https://docs.temporal.io/develop/worker-tuning-reference)). That limit has now been relaxed: **virtual workflow threads became stable in Java SDK v1.37.0 (July 2026)** on JDK 21+ ([newreleases v1.37.0](https://newreleases.io/project/github/temporalio/sdk-java/release/v1.37.0)). A Temporal engineer says turning them on is a single worker option with no change to workflow code. The same forum thread also includes reports of the feature not working ([community.temporal.io](https://community.temporal.io/t/temporal-workflows-with-java-21-and-virtual-threads-sample-projects-for-reference/17229)). So Java can probably now hold many thousands of hot item workflows. That is an inference, though: nobody has published a memory-per-workflow benchmark for either SDK. Go gets the same result with its default settings.

Ergonomics are mostly a matter of taste. Java declares workflows as annotated interfaces with typed stubs, which gives compile-time contracts for signal, update and query handlers. Go uses plain functions and string-named handler registration. It also requires explicitly draining signals before continueAsNew, which Java handles more naturally with one thread per handler. Both SDKs have time-skipping test environments and history replayers. Go's `workflowcheck` linter for catching non-deterministic code is the more mature of the two. The Java version is less than a year old ([sdk-java #2767](https://github.com/temporalio/sdk-java/pull/2767)).

## The JVM costs 250–390 MB per instance; Go ships as one binary

For a stateless service that validates requests and writes to Postgres, raw language speed hardly matters. The database connection pool is the bottleneck in both languages. InfoQ's July 2026 measurements on JDK 25 show this. Switching a Spring Boot JDBC endpoint to virtual threads raised throughput from **624 to 1,539 req/s** and cut p99 latency from 1,363 to 613 ms. Yet the error rate stayed at about 37% either way, because the bottleneck simply moved to the pool ([InfoQ](https://infoq.com/articles/virtual-threads-after-jdk24)). Garbage collection no longer separates the two either. ZGC is always generational from JDK 24 on ([JEP 490](https://openjdk.org/jeps/490)), and Go 1.26 made its new Green Tea collector the default, which cuts GC overhead by 10–40% ([Go 1.26 notes](https://go.dev/doc/go1.26)).

The differences that do matter are memory, startup time and packaging. They decide what a self-hoster's small VM can run and what each managed-cloud replica costs.

| Java 25 setup | Startup | Peak memory (RSS) |
|---|---|---|
| Spring Boot 3.5, JVM | 1.9 s | 389 MB |
| Quarkus 3.28, JVM | 1.15 s | 271 MB |
| Micronaut 4.5, JVM | 0.66 s | 253 MB |
| Spring Boot, native image | 0.10 s | 149 MB |
| Quarkus, native image | 0.05 s | 71 MB |

Source: [gillius.org, Oct 2025](https://gillius.org/blog/2025/10/java-25-framework-startup.html). The heap after GC was only 3–28 MB, so most of the memory is runtime overhead, not application data.

No source in the research measured an equivalent Go service. The best evidence is anecdotal. Glasskube rewrote a Kotlin service in Go after it idled above **2 GB**, and found Go services "barely consumed any memory or CPU" and started in **under 100 ms**, against about 8 s for its Spring Boot app ([Glasskube](https://glasskube.dev/blog/from-java-to-go)). CockroachDB similarly reported that porting a large Java system to Go "greatly reduced memory footprint and GC overhead" ([Cockroach Labs](https://www.cockroachlabs.com/blog/why-go-was-the-right-choice-for-cockroachdb/)).

Java's usual answer to the memory gap is GraalVM native image, which has become a weaker option. In September 2025 Oracle announced that **GraalVM for JDK 24 was the last release supported as part of Java SE**, and that native image "will no longer be part of Java SE products." Startup work moves to OpenJDK's Project Leyden ([Oracle](https://blogs.oracle.com/java/post/detaching-graalvm-from-the-java-ecosystem-train), [ADTmag](https://adtmag.com/Articles/2025/09/30/Oracle-shifts-GraalVM-focus-away-from-Java.aspx)). Leyden's AOT cache is real but modest: the JDK 25 profiling feature cut runtime from 90 to 73 ms in its own example ([JEP 515](https://openjdk.org/jeps/515)). The Temporal Java SDK supports native image only on a best-effort basis. Its workflow stubs use dynamic proxies, and gRPC/Protobuf rely on reflection. The application therefore has to keep tracing-agent configuration up to date, and when that configuration is wrong the failure shows up at runtime, not at build time ([sdk-java AOT doc](https://github.com/temporalio/sdk-java/blob/master/docs/AOT-native-image.md)). Keycloak, the flagship Java self-hosted server, moved to Quarkus but still ships on the JVM rather than as a native binary ([Keycloak](https://www.keycloak.org/nightly/migration/migrating-to-quarkus)). Self-host operators are used to the Go pattern from Temporal, Grafana and CockroachDB: one static binary, a small container image and a Helm chart.

Virtual threads are production-ready on JDK 25, with caveats. JEP 491 (JDK 24) stopped `synchronized` blocks from pinning carrier threads. That pinning caused Netflix's JDK 21 stalls ([JEP 491](https://openjdk.org/jeps/491)). Some cases still pin: native frames, class initializers and Linux file I/O. Diagnosis now goes through JFR events ([InfoQ](https://infoq.com/articles/virtual-threads-after-jdk24)). HikariCP has merged no virtual-thread-specific changes. Spring's virtual-thread switch applies to Tomcat and Jetty, not Netty, so grpc-java needs a virtual-thread executor configured separately. None of this blocks Java, but it is extra configuration that goroutines never need.

The Postgres drivers are close, with one difference that matters here. **pgx** supports COPY, batching and native LISTEN/NOTIFY, and its companion library `pglogrepl` covers logical replication ([pgx](https://github.com/Jackc/pgx)). **pgjdbc** has replication and COPY APIs, but it "cannot receive asynchronous notifications and must poll the backend" ([pgjdbc docs](https://jdbc.postgresql.org/documentation/server-prepare/)). If the event log uses NOTIFY to wake consumers, Java needs a dedicated connection polled from a virtual thread. That is cheap enough, but less tidy.

## Connect-go lets the TypeScript UI call the kernel directly

The API-contract layer is where Go's lead is clearest. **connect-go is a stable, production-used server that speaks gRPC, gRPC-Web and the Connect protocol from a single handler.** That means a browser client built on connect-es can call the kernel "without a translating proxy such as Envoy." On the JVM, the only Connect implementation is a beta Kotlin client aimed at mobile, and no Connect server is documented ([connectrpc.com](https://connectrpc.com/docs/introduction)). A Java kernel built on grpc-java would normally put Envoy's gRPC-Web and JSON-transcoding filters in front. That is one more component in every self-hosted deployment, and one more thing to document and debug. The research did not check JVM alternatives such as Armeria or Spring gRPC, which may narrow this gap. Buf's lint, breaking-change detection and code generation work with any language, and Buf publishes generated SDKs for Maven as well as Go ([Buf](https://buf.build/docs/bsr/generated-sdks/maven/)). So the generated clients are fine either way. Most OpenAPI generators from protos are Go tools, such as grpc-gateway, whose OpenAPI v3 output is still alpha ([grpc-gateway](https://grpc-ecosystem.github.io/grpc-gateway/docs/mapping/openapi_v3/)). They run through buf plugins whatever language the server uses.

AI tooling looks different at each layer, and the layers point in different directions.

| Layer | Java | Go | Edge |
|---|---|---|---|
| Official LLM SDKs | Anthropic 3.8.0, OpenAI, Google Gen AI, AWS SDK 2.x | Anthropic 2.9.0, OpenAI v3, Google Gen AI, AWS SDK v2 | Even ([Claude docs](https://platform.claude.com/docs/en/cli-sdks-libraries/overview), [openai-go](https://github.com/openai/openai-go), [Google](https://aistudio.google.com/docs/libraries)) |
| MCP SDK | v2.0.1; supports spec 2025-11-25 | v1.8.0; supports the newest spec, 2026-07-28 | Go ([go-sdk](https://github.com/modelcontextprotocol/go-sdk/releases), [java-sdk](https://github.com/modelcontextprotocol/java-sdk/releases)) |
| Agent frameworks | Spring AI 2.0 GA, LangChain4j 1.21, ADK Java 1.11 | ADK Go 1.0, Genkit Go 1.0, Eino (pre-1.0) | Java ([Spring AI](https://spring.io/blog/2026/06/12/spring-ai-2-0-0-GA-available-now/), [InfoQ](https://infoq.com/news/2026/10/java-news-roundup-sep28-2026/)) |
| Temporal AI integration | Spring AI, Public Preview, no streaming | Google ADK, GA, with MCP toolsets | Slight Go ([Java](https://docs.temporal.io/develop/java/integrations/spring-ai.md), [Go](https://docs.temporal.io/develop/go/integrations/google-adk.md)) |

Temporal's own AI investment is mostly in **Python and TypeScript**. The OpenAI Agents SDK, Claude Agent SDK, MCP, LangGraph and Pydantic AI integrations are all Python-only or Python/TS-only ([Temporal blog](https://temporal.io/blog/ai-framework-integrations), [Temporal integrations](https://docs.temporal.io/integrations.md)). This changes how much the agent tooling should count. Any Temporal worker in any language can run on a task queue that the kernel's workflows call into. So "agent workers" do not have to be written in the kernel's language. The founder already knows Python, which has the richest Temporal agent integrations of all. In the kernel itself, the simple pattern works equally well in both languages: the agent loop runs in a workflow, and LLM and MCP calls run as activities. Java's lead in agent frameworks matters only if all agent code has to live in the kernel's language, and nothing requires that.

## Java's real advantage is the founder, not the runtime

Comparable projects fall into two groups, and each choice puts the product in one of them. **Go** is the language of cloud-native, self-hosted infrastructure: Temporal, Cadence, OpenFGA, SpiceDB, Grafana, Mattermost and Gitea ([temporalio/temporal](https://github.com/temporalio/temporal), [openfga](https://github.com/openfga/openfga), [grafana](https://github.com/grafana/grafana)). **Java** is the language of workflow and BPM engines: Camunda/Zeebe, Conductor and Kestra ([camunda](https://github.com/camunda/camunda), [conductor-oss](https://github.com/conductor-oss/conductor)). Kestra kept Java through its September 2026 2.0 rewrite, which made Postgres its reference backend ([Kestra](https://kestra.io/blogs/kestra-2-0-backend-choice)). Direct work-tracking peers such as Plane and Huly chose Python or TypeScript instead ([plane](https://github.com/makeplane/plane), [huly](https://github.com/hcengineering/platform)). Neither choice would look odd.

| Signal | Java | Go | Source |
|---|---|---|---|
| Developers using it (SO 2025) | 29.4% | 16.4% | [Stack Overflow](https://survey.stackoverflow.co/2025/technology/) |
| "Admired" (SO 2025) | 41.8% | 56.5% | [Stack Overflow](https://survey.stackoverflow.co/2025/technology/) |
| Share of US backend job listings | 38% | 18% | [SeekerScore](https://www.seekerscore.com/insights/backend-engineer-market-2026) |
| Measured usage growth (2026) | Outflow to Python/TS | Only predicted riser that actually grew | [JetBrains Research](https://blog.jetbrains.com/research/2026/08/programming-language-migration/) |
| Developer satisfaction | n/a | 91% satisfied | [Go survey 2025](https://go.dev/blog/survey2025) |

Java has twice the pool of potential hires, and those hires are cheaper. Go roles "tend to pay above" the $145–185K US mid-to-senior backend range and cluster in infrastructure and platform work ([SeekerScore](https://www.seekerscore.com/insights/backend-engineer-market-2026)). For an open-source infrastructure product, the people who contribute to self-hosted tools probably lean toward Go. That is plausible but unmeasured. CockroachDB is the best-documented case. It chose Go even though "nearly everyone, including the founders" was new to it, found it easy for contributors to pick up, and grew past 600 contributors ([Cockroach Labs](https://www.cockroachlabs.com/blog/why-go-was-the-right-choice-for-cockroachdb/)).

The founder's own learning curve is the biggest cost, and the evidence on it is all anecdotal. Glasskube's Kotlin team found "the learning curve was not as steep as expected" and that velocity "quickly nearly matched" what it had before. They still missed IntelliJ-quality stack traces, Spring-style dependency injection and hot reload ([Glasskube](https://glasskube.dev/blog/from-java-to-go)). In the Go survey, 33% of Go developers name following idioms as their top challenge, and 28% name missing features such as enums, sum types and nil safety. The survey notes that switching between these styles "adds cognitive load, especially for developers coming from Java" ([Go survey 2025](https://go.dev/blog/survey2025)). The founder would face a second learning curve at the same time. Temporal's determinism rules are hard to debug in any language, and harder in one you are still learning.

AI coding assistants do not close the gap, and may widen it slightly. In the June 2026 Multi-LCB benchmark, several models scored lower on Go than on Java. One model scored **60.0 on Go against 85.9 on Java** across 1,055 tasks. But these are competitive-programming puzzles, frontier closed models were not tested separately, and one open model showed near parity ([arXiv 2606.20517](https://arxiv.org/pdf/2606.20517)). Go developers' own satisfaction with AI tools is only 55%, and their top complaint is non-working code ([Go survey 2025](https://go.dev/blog/survey2025)). This is weak evidence. It does not support the common claim that Go's simplicity makes it easier for AI to write.

## Recommendation: Go, with a two-week spike as the tripwire

Choose Go for the kernel. The reason is not that Go is faster. Four structural factors decide it, and each one gets more valuable as the product grows.

| Deciding factor | Why it favors Go | How strong the evidence is |
|---|---|---|
| Contract layer for the TypeScript UI | connect-go serves gRPC, gRPC-Web and Connect with no proxy; the JVM has no Connect server | Strong (official docs) |
| Self-host distribution and per-replica cost | Single static binary vs. 250–390 MB JVM; native image is losing Oracle support | Strong for Java numbers; Go figures anecdotal |
| Temporal fit | Lead SDK, 10k-workflow cache by default, mature determinism linter | Strong for feature timing; no performance benchmark |
| MCP and agent workers | Go MCP SDK supports the newest spec; agent frameworks can live in Python workers | Moderate |
| Founder velocity (favors Java) | Known language, Spring/Quarkus with everything included, slightly better AI output | Strong that a cost exists; its size is unmeasured |

Several things would change this to Java:

- **No time for a learning dip.** If the founder needs paying customers within about three months and cannot afford one, switch. Experience reports suggest weeks, not months, but none of them are rigorous.
- **Self-hosting is not central.** If buyers turn out to be enterprises that run JVM fleets and prefer Spring, or if the product becomes cloud-only, much of Go's distribution advantage disappears.
- **Early hires or co-founders are Java engineers.**
- **The spike fails.** If the two-week spike described below ends with the founder still fighting the language rather than the domain, switch.

If Java is chosen, use JDK 25 on the JVM with Quarkus or Spring Boot. Don't use native image. Turn on Temporal virtual workflow threads and raise the cache size. Budget for Envoy (or check Armeria) in front of grpc-java. Run the Postgres LISTEN loop on a dedicated connection.

To reduce Go's weaknesses, start with the spike. Spend two weeks building one complete slice in Go:

1. A Connect handler validates a schema and policy.
2. It writes the item and an event-log row through pgx in a single transaction.
3. It starts an entity workflow that sets a timer, continues as new, and spawns a child agent workflow.
4. The child workflow calls an LLM and an MCP tool as activities.

Nothing is lost by running the spike: it is the first slice the product needs in either language, so even a failed Go spike gives the founder a working design to rebuild in Java. It tests every hard part of the decision. Then fix the stack early so there are fewer decisions to make: connect-go, pgx with sqlc, a single migration tool, `workflowcheck` and golangci-lint in CI, and testify with Temporal's test suite. Use Protobuf `oneof` and generated types to stand in for the sum types Go lacks. Wire dependencies by hand in `main` instead of looking for a Spring replacement. Write tests before asking an AI assistant for code, given the evidence that it produces non-working Go more often. Keep a Python worker pool available for agent work that needs Temporal's Python-only integrations.

## Where the evidence runs out

Several important questions have no good data. No credible benchmark compares Temporal worker throughput or memory per cached workflow between Java (with virtual threads) and Go. The research found no sourced idle-memory figure for a Go gRPC+pgx+Temporal service, so the size of the memory gap rests on Java measurements and Go anecdotes. No study measures how long Java developers take to become productive in Go. The AI-coding evidence comes from puzzles, not backend code. Live GitHub statistics were unavailable, and the SDK languages used by Temporal's big customers (Netflix, Stripe, Coinbase) are not named in their public case studies ([Netflix](https://temporal.io/resources/case-studies/netflix-increases-developer-productivity), [Stripe](https://temporal.io/stripe)). The research also did not check JVM gRPC-Web options such as Armeria. If one serves browsers cleanly, Go's contract-layer advantage gets smaller.

## Conclusion

This choice is less "Java vs Go" than it first appears. Temporal lets the kernel and the agent workers use different languages, so the real decision is narrower: which language owns the validation, write path and contract layer that every self-hoster runs and every client calls. For that job, Go's advantages (no proxy in front of the UI, a single binary, Temporal's lead SDK) are built into the product, while Java's advantage is the founder's head start. That head start shrinks every month, and the architectural advantages do not.

The founder also does not have to bet blindly. Because the choice is reversible early and expensive later, the two-week spike is the right tool. Running it in Go turns the main uncertainty, how much the learning curve costs, into something measured before the codebase makes switching costly.
