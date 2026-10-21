# Content Service documentation inventory

**Reviewed:** 2026-10-21. Scope covers the repository entry point, mdBook
navigation, Content Cache contract and adapters, operational lifecycle,
implementation/conformance plans, and evidence.
**Parent tracker:** [platform #368](https://github.com/synanton/platform/issues/368); **component workstream:** [content-service #25](https://github.com/synanton/content-service/issues/25).

| Document | Classification | Disposition / action |
|---|---|---|
| [`../README.md`](../README.md) | Current / repository entry point | Retain; now distinguishes canonical Content Cache 1.26 artifact storage from the platform ingestion-cache path and from Phase 1 end-to-end use. Maturity evidence remains dated 2026-10-14. |
| [`index.md`](index.md), [`SUMMARY.md`](SUMMARY.md) | Current / docs navigation | Retain; added a link to this inventory. |
| [`design.md`](design.md), [`architecture.md`](architecture.md), [`schema.md`](schema.md), [`schema.sql`](schema.sql) | Current / contract and implementation detail | Retain; together document behavior, architecture, and schema. Design 1.26 is the contract authority; the service-specific design explains this implementation. |
| [`api.md`](api.md) | Current / API reference | Retain; explicitly covers unauthenticated default operation and opt-in Security Provider context middleware. Tenant identifiers in the default mode are selectors, not verified caller identity. |
| [`runbook.md`](runbook.md) | Current / operations | Retain as operational source; README links the quick-start and job configuration. |
| [`conformance.md`](conformance.md), [`conformance-benchmark-plan.md`](conformance-benchmark-plan.md), [`benchmarks.md`](benchmarks.md) | Current / evidence and validation plan | Retain; distinguish the implemented 14-check Core conformance suite from unimplemented Extended suite and low-concurrency benchmarks from SLO/load acceptance. |
| [`IMPLEMENTATION.md`](IMPLEMENTATION.md), [`implementation-plan.md`](implementation-plan.md) | Current / plan and build guidance | Retain; plan owns work sequencing. README maturity table owns current implementation evidence summary. |
| [`reviews/2026-10-14-external-review-response.md`](reviews/2026-10-14-external-review-response.md) | Useful historical review record | Retain dated; not a replacement for current code/tests or later review evidence. |
| Platform [Design 1.26](https://github.com/synanton/platform/blob/main/docs/architecture/synanton-design-1.26.md) and component issue [#25](https://github.com/synanton/content-service/issues/25) | Normative cross-repo contract / workstream | Contract is platform-owned; this service is one reference implementation. Integration status must be linked to the platform's current acceptance evidence, not inferred from service-level tests. |

No contract or review history was deleted. Use Proposed, Approved (design),
Contract frozen, In progress, Implemented, Validated (with evidence/date), and
Superseded explicitly; never equate a component test with platform end-to-end
validation.
