# content-service developer documentation

`content-service` is the reference implementation of [Synanton Content Cache 1.26](https://raw.githubusercontent.com/synanton/platform/refs/heads/main/docs/architecture/synanton-design-1.26.md): tenant-scoped, content-addressable storage with integrity verification, retention lifecycle, and opaque references.

This book is built from the repository's `docs/` on every merge and published to [synanton.github.io/content-service](https://synanton.github.io/content-service/). Start with the section that matches your job:

- **Using the service** — the [HTTP and gRPC API](api.md) and the operator's [runbook](runbook.md).
- **Understanding the design** — [architecture](architecture.md), the full [design document](design.md), and the [schema reference](schema.md).
- **Contributing** — the [implementation guide](IMPLEMENTATION.md) (what to build next) and the [implementation plan](implementation-plan.md) (the plan of record).

The repository [README](https://github.com/synanton/content-service#readme) covers quick start, configuration, and current implementation status. The [documentation inventory](DOCUMENTATION-INVENTORY.md) records canonical sources and status dispositions.
