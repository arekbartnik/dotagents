# Source playbooks

The why skill spawns one investigator per available evidence category, each reading a single category playbook below. The playbooks are tool-agnostic. Adapt them to whichever MCP covers the category.

| Category                     | Playbook                                                   | Example tools                              |
| ---------------------------- | ---------------------------------------------------------- | ------------------------------------------ |
| Source control history       | [`code-archaeology.md`](./sources/code-archaeology.md)     | git, `gh`                                  |
| Issue / ticket tracker       | [`issue-tracker.md`](./sources/issue-tracker.md)           | Jira, GitHub Issues, Plane, Shortcut       |
| Long-form documents          | [`documents.md`](./sources/documents.md)                   | Confluence, Google Docs, Coda              |
| Real-time team chat          | [`team-chat.md`](./sources/team-chat.md)                   | Discord, Microsoft Teams, Mattermost       |
| Infrastructure observability | [`observability.md`](./sources/observability.md)           | New Relic, Honeycomb, Grafana, Splunk      |
| Error / exception tracking   | [`error-tracking.md`](./sources/error-tracking.md)         | Rollbar, Bugsnag, Airbrake                 |
| Product analytics warehouse  | [`analytics-warehouse.md`](./sources/analytics-warehouse.md) | Snowflake, BigQuery, ClickHouse, dbt     |

Cross-cutting:

- [`incident-postmortem.md`](./sources/incident-postmortem.md). Add this if the target code looks defensive (null checks, retry, timeout, rate limit, feature flag, egress guard, OOM handler).
