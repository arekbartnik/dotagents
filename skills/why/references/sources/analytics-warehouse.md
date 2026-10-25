# Product Analytics Warehouse

## What this source contains

The analytics warehouse is the product-analytics and data-pipeline layer. It complements infrastructure observability. Observability is the *infra/runtime* view, the warehouse is the *product/data* view (what users did, which experiments ran, how feature usage evolved, where a threshold constant came from).

- **Product analytics events.** User behavior: feature invocations, clicks, accepts/rejects, submissions, client-reported errors.
- **Usage & billing events.** For cost- or volume-driven decisions.
- **Experiment / feature-flag data.** Exposure and outcome tables.
- **Query history and warehouse telemetry.** Answer "was this query expensive?", "how often did anyone run this?", "when did warehouse load spike?"
- **Pipeline lineage.** Transformation models reveal what pipelines depend on a table/field. Upstream changes frequently motivate consumer-code changes.
- **Notebooks.** Exploratory analyses engineers wrote before code changes. Often not queryable through a SQL interface. If you suspect the rationale lives in a notebook, name it as a gap.

## How to search it

Use the warehouse MCP available in this environment. Inspect its tool schema first. Use read-only queries.

**Orient before querying.** Schemas are company-specific. List the tables and describe their columns before trusting a table name.

**Time-bound every query.** These tables are huge and unconstrained scans time out. Filter on the timestamp column with a window bracketing the ship date, typically ~30 days before and after, wider only for strong reason.

**Prefer cleaned, typed models over raw event tables.** Raw tables often carry duplicates and untyped JSON properties. Drop to the raw table only when there's no cleaned model yet, or you need events from inside the pipeline's refresh lag.

### Investigation patterns that tend to pay off

Pick the table + column combination that matches the target:

1. **Event usage trajectory.** Daily counts on the relevant event table across a ±30d window around the PR merge. A step function from zero to steady volume within a day or two of the merge is strong circumstantial evidence the PR launched the feature. A decay to zero suggests a deprecation or deletion.
2. **Guard-rail / defensive-check origin.** Distribution (median / p99 / max) of the relevant property column in the 14 days *before* the PR. A p99 that matches the target's threshold constant suggests the number was chosen from data.
3. **Experiment / feature-flag lookup.** Find the exposure table, then pull exposure counts by variant for the relevant flag key near the PR date.
4. **Query-history evidence for migrations, backfills, or perf rewrites.** Query history filtered by the table or symbol name within a tight time window surfaces the expensive queries that likely motivated the change (sort by duration, or aggregate bytes read and run counts).
5. **Pipeline lineage.** If the target reads from or writes into a transformation model, the model's own git history often carries the rationale. Hand that lead back to the git investigator rather than chasing it yourself.

## What good evidence looks like here

Beyond the pattern shapes above:

- An error-classifying event's count drops to near zero in the days after a defensive-code PR. Suggests the PR resolved that error class
- An exposure table row names the target's feature-flag key with a "shipped" / "concluded" decision around the PR ship date

## Common pitfalls

- **Instrumented ≠ caused.** An event's existence means someone cared enough to log it, not that the target code exists *because* of it. Pair with a PR/commit citation from the git investigator before claiming causation.
- **Silent instrumentation changes.** A step function in event volume may mean a new event started being logged, not that user behavior changed. Check for instrumentation PRs in the same window before reading the ramp as a feature-launch signal.
- **Schema drift.** Event properties evolve. A column on the cleaned model today may not have existed when the target was written. Older data may carry the property only inside raw JSON.
- **Refresh lag.** Cleaned models are rebuilt on a schedule (often hourly/daily). For events from the last few hours, fall back to the raw table and deduplicate by event ID.
- **Company-specific tables.** Experiment, feature-flag, billing, and usage tables vary. Reporting a result from a table whose existence you never confirmed is a classic failure mode. Confirm the table exists first.
- **Retention cliff.** If the relevant window predates the table's retention or the model's creation date, that's a *gap*, not a null result. Name it explicitly so the synthesizer doesn't read "no results" as "no activity."
- **Notebooks aren't queryable.** A SQL interface can't see notebooks. If you suspect the rationale lives in one, return a gap.

## What to return

For each relevant finding:

- Type (product event / experiment exposure / usage or billing event / query-history row / transformation model)
- Fully-qualified table name and the exact query you ran
- Time window queried
- Compact numeric summary (counts, percentiles, first/last-seen timestamps). **Don't dump raw rows.**
- Temporal correlation with the target's ship date (e.g., "first row 2024-08-15, PR #49074 merged 2024-08-14")
- Relevance + strength: direct / circumstantial / weak
