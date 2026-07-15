# Tasks: plan-auto-update-strategy

Planning-stage tasks only. Implementation tasks will be added once this
change graduates from proposal to active work (and likely split: llama-skein
tasks here, a mirrored change opened in the skein repo for the orchestrator).

- [x] 1. Confirm no existing planning/duplicate work (`specsync scan`).
- [x] 2. Draft the proposal, surfacing the real open decisions rather than
       deciding them unilaterally (scope, cadence, safety gate, pinning,
       visibility).
- [x] 3. Get user decision on the core automation posture: event-driven
       checking + canary-first with a real generation test + pin-and-graduate
       (confirmed over scheduled-blind and manual-only).
- [x] 4. Resolve the architectural question this raises: canary/fleet
       coordination belongs in skein (fleet-aware), not llama-skein
       (per-host, fleet-unaware today) — llama-skein exposes primitives
       (`check`, `history`, the existing `upgrade`) that skein's scheduler
       drives.
- [ ] 5. Decide the remaining configuration choices before implementation:
       which host is the fleet canary (z4? rocky? proxmox?), the observation
       window duration between canary success and promoting to the rest, and
       the daily check schedule's exact time.
- [ ] 6. Once configuration choices are made: implement
       `GET /api/system/upgrade/check` in llama-skein (reuses
       `resolvePrebuiltSource`, no install side effects).
- [ ] 7. Implement `GET /api/system/upgrade/history` in llama-skein (per-host
       ring buffer of past upgrade attempts).
- [ ] 8. Open a mirrored change in the skein repo for the orchestrator
       (scheduled check → canary → real health check → observation window →
       promote-or-halt), referencing this change via `specsync link`.
- [ ] 9. Decide whether llama-skein self-updates (scope decision 1) becomes
       its own follow-up proposal once the engine auto-update has a track
       record — not blocking this change.
