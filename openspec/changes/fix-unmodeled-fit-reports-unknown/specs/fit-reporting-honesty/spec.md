# Spec delta: fit-reporting-honesty

## ADDED Requirements

### Requirement: A fit level is only a verdict when a fit was computed

The fit report SHALL distinguish "measured and does not fit" from "could not be
measured", reporting the latter as `unknown` rather than `no`.

#### Scenario: A backend whose weights are not modeled

- **WHEN** a fit report is generated for a model whose backend has no weight model
- **THEN** its `fit_level` is `unknown`
- **AND** its `reason` states that fit is computed only for the supported backends

#### Scenario: Metadata that cannot be read

- **WHEN** the model's weight metadata cannot be parsed
- **THEN** `fit_level` is `unknown` and `reason` names the read failure

#### Scenario: A model that genuinely does not fit

- **WHEN** the weights and cache were measured and exceed what the host can provide
- **THEN** `fit_level` is still a real verdict, unchanged by this requirement

#### Scenario: A measured fit is unaffected

- **WHEN** a fit is computed successfully
- **THEN** the reported level is the computed one, never the placeholder
