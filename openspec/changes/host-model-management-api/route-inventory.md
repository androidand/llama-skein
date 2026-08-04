# Route inventory vs. OpenAPI source (task 1.2)

Every implemented model inventory/detail/fit/storage/pull/load/unload/remove/
config route (`internal/server/server.go`), cross-checked against
`contracts/llama-skein.openapi.json` as of commit `fd1354ab0` (skein) /
`51456ac` (llama-skein).

## In the contract already (no action needed for this change's baseline)

| Route | Handler |
|---|---|
| `GET /api/fit` | `handleAPIFitReport` |
| `GET /api/fit/{model}` | `handleAPIModelFit` |
| `POST /api/fit/hypothetical` | `handleAPIHypotheticalFit` |
| `GET /api/models` | `handleAPIListModels` |
| `GET /api/models/offload/{model}` | `handleAPIOffloadRecommendation` |
| `GET /api/config/info` | `handleAPIConfigInfo` |
| `POST /api/config/models` | `handleAPIConfigAddModel` |
| `GET, PATCH, DELETE /api/config/models/{id}` | `handleAPIConfigGetModel` / `PatchModel` / `RemoveModel` |
| `PATCH /api/config/groups/{id}` | `handleAPIConfigPatchGroup` |
| `POST /api/config/reload` | `handleAPIConfigReload` |
| `POST /api/config/validate` | `handleAPIConfigValidate` |
| `GET /api/config/history` | `handleAPIConfigHistory` |
| `POST /api/config/rollback` | `handleAPIConfigRollback` |
| `GET, PUT, DELETE /api/config/default-model` | `handleAPIConfigGetDefaultModel` / `Set` / `Clear` |
| `GET, POST /api/skein/config` | `handleAPIGetProfile` / `handleAPISetProfile` |
| `GET /api/skein/config/default` | `handleAPIProfileDefault` |
| `GET /v1/models` | (OpenAI-compat list) |

## Implemented, but NOT in the OpenAPI contract yet

This is the real gap, and it is exactly this change's reason for existing —
these are the ad-hoc, pre-contract-first routes that sections 2-6 replace with
a typed, operation-ID-based, resumable install/lifecycle model. Recorded here
as the concrete starting inventory, not a surprise finding.

| Route | Handler | Section that formalizes/replaces it |
|---|---|---|
| `GET /api/models/{model}` (detail) | `handleAPIGetModel` | 5.1 (inventory/lifecycle detail) |
| `DELETE /api/models/{model}` | `handleAPIDeleteModel` | 5.3 (removal) |
| `POST /api/models/load/{model}` | `handleAPILoadModel` | 5.2 (load/unload) |
| `POST /api/models/unload` (all) | `handleAPIUnloadAll` | 5.2 |
| `POST /api/models/unload/{model}` | `handleAPIUnloadModel` | 5.2 |
| `POST /api/models/pull` | `handleAPIPullModel` | 2.1-2.5, 4.1-4.7 (operation state machine + resumable install) — this is the connection-bound handwritten DTO section 6.4 removes once every caller has migrated |
| `GET /api/models/context/{model...}` | `handleAPIContextRecommendation` | not explicitly owned by any current section; flag for whoever picks up 1.3's schema work |
| `GET /api/storage` and `GET /api/hardware/storage` | `handleAPIHardwareStorage` (same handler, two paths) | out of this change's scope (hardware/storage reporting, not model lifecycle) — noted because `/api/storage` came up while auditing, not because it needs to move |
| `GET /unload` (legacy, no `/api` prefix) | (legacy route) | pre-dates the `/api` namespace entirely; confirm during 6.4 whether anything still depends on it before removal |

## Notes for section 2-3 authors

- `POST /api/models/pull`'s current implementation is the literal target of
  task 6.4 ("remove the old connection-bound handwritten pull DTO and route
  after every supported caller has migrated") — its request/response shape is
  a reasonable starting point for what 1.3's install-plan schema needs to
  supersede, not something to design from scratch.
- `/api/storage` and `/api/hardware/storage` being two paths to one handler is
  pre-existing, unrelated to this change; not touched here.
