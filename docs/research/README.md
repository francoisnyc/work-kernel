# Research reports

Background research behind the design. Each report was written from
parallel web research; claims a researcher could not verify on a fetched
page are marked UNVERIFIED. `docs/design.md` is the spec; these explain why.

| Report | Date | Feeds |
| --- | --- | --- |
| [Search constraints for the Item kernel](search-constraints-for-item-kernel.md) | 2026-11-11 | Search |
| [Java vs Go for the kernel](java-vs-go-for-the-kernel.md) | 2026-11-11 | Language choice |
| [Types and lifecycle design](types-and-lifecycle-design.md) | 2026-11-11 | Types |
| [Policy, permissions and spike prep](policy-permissions-and-spike-prep.md) | 2026-11-11 | Policy, Agents |
| [Extension points in work platforms](extension-points-in-work-platforms.md) | 2026-11-12 | Hooks |
| [Connectors for the Item kernel](connectors-for-the-item-kernel.md) | 2026-11-12 | Connectors, holds (Steps 6–7) |
| [Inherited permissions at scale](inherited-permissions-at-scale.md) | 2026-11-14 | Policy (Steps A, A2) |
| [Hook protocol versus package system](hook-protocol-versus-package-system.md) | 2026-11-22 | The hook protocol (pli.hooks/v1); packages as bundles, later |
| [An event stream from the write-ahead log](event-stream-from-the-write-ahead-log.md) | 2026-11-22 | The sequencer, Watch, the relay |
| [Task inboxes: TASKANA (KADAI) and Polyflow](task-inboxes-taskana-polyflow.md) | 2026-11-23 | The approvals pilot: queues, the inbox as a projection |

Later decisions sometimes supersede a report; the decision log and
`docs/design.md` win. For example, the permissions report reverses the
earlier idea of giving a shared Item its own scope.
