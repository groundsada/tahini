# tahini roadmap

Features are implemented one at a time in this order. Each is deployed and verified in a local kind cluster before moving to the next.

| # | Feature | Status |
|---|---------|--------|
| 1 | **Agent connected indicator** — green/grey dot on workspace pages showing live agent status | pending |
| 2 | **Bundle xterm.js locally** — embed xterm JS/CSS in the binary; proper terminal with colors, cursor, scrollback | pending |
| 3 | **Workspace logs streaming** — replace 2s poll with SSE for live build log tail | pending |
| 4 | **Auto-stop on idle** — stop pod after N minutes with no agent heartbeat; configurable per workspace | pending |
| 5 | **Template variables form** — parse `variable` blocks from HCL; render key/value inputs on workspace create | pending |
| 6 | **Workspace edit params** — allow updating variable values on a stopped workspace | pending |
| 7 | **Workspace templates from Git** — pull HCL from a raw URL or git ref; sync button on template detail | pending |
| 8 | **Port forwarding via agent** — agent proxies workspace ports; accessible at `/workspaces/{id}/ports/{port}` | pending |
| 9 | **Multi-user with orgs** — user tiers: user, template admin, user admin, owner; orgs with per-org template libraries | pending |
