<!-- This file is auto-translated from docs/README.md. Do not edit manually. -->

# ElBot Documentation

User-facing documentation. Development plans, architecture, and internal design are in [`../devdocs/`](../devdocs/).

## Reading Order

1. [Quick Start](getting-started.md): Configure API Key → Start CLI → First conversation.
2. [Configuration Guide](configuration.md): Configuration file layout, path rules, Provider, runtime data, plugin directory.
3. [Command Reference](commands.md): slash commands and Session management.
4. [Core Concepts](concepts.md): chat / work mode, tool discovery, Session, Hook, Cron, Skill, security policies.
5. [Hook](hooks.md): Rule Hooks, action types, multi-segment output, exec scripts.
6. [Elnis listening hub](elnis.md): Elnis / Elwisp / Elvena and external event integration.
7. [Elnis Configuration and Usage](elnis-usage.md): Enable Elnis, configure Elwisp, deliver events.
8. [ELyph Task Notation](elyph.md): Task syntax shared by Skill and Cron task entries.
9. [Frontend API](frontend-api.md): WebSocket protocol, attachment interface, and structured management API.

## Documentation Maintenance

- Maintenance rules can be found in [`../AGENTS.md`](../AGENTS.md). This directory only contains user documentation; internal materials are placed in [`../devdocs/`](../devdocs/).
- `CHANGELOG.md` is the Chinese source file, and `CHANGELOG.en.md` is automatically translated by GitHub Actions; please do not edit it manually. Using versions (tags) as nodes: write routine changes into `## [Unreleased]`, and when releasing, change it to `## [vX.Y.Z] - YYYY-MM-DD` and create a new empty Unreleased.

