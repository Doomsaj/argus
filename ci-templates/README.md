# argus/ci-templates

GitLab CI/CD components that wire argus into provider and consumer pipelines. The spec, including the rationale for every rule, is [docs/07-ci-templates.md](../docs/07-ci-templates.md). In production this directory is the separate repo `argus/ci-templates`, published to the CI/CD Catalog.

| Component | Who includes it | Jobs |
|---|---|---|
| `provider` | every provider | `argus-spec-lint`, `argus-spec-check`, `argus-verify-samples`, `argus-spec-publish`, `argus-pact-verify-only`, `argus-skipped` |
| `verify-only` | providers with Pact but not yet the full `provider` component | `argus-pact-verify-only` |
| `consumer-go` | Go consumers | `argus-pact-consumer`, `argus-pact-publish` |
| `deploy-gate` | providers and consumers, once per deploy flow | `argus-can-i-deploy`, `argus-deploy-record` |

Hidden templates: `.argus-credentials` (RO/RW mapping + `ARGUS_PACT_PUBLISH`), `.argus-rules-skip-verify-only`, `.argus-can-i-deploy`, `.argus-deploy-record`.

## Include

```yaml
include:
  - component: git.digikala.com/argus/ci-templates/provider@1.0.0
    inputs:
      spec_job: build
```

Without CI/CD components: `- project: argus/ci-templates`, `ref: v1.0.0`, `file: templates/provider.yml`, same `inputs:`.

## Inputs

### `provider`

| Input | Default | Meaning |
|---|---|---|
| `argus_image` | `artifactory.digikala.com/argus/argus-cli:1.0.0` | CLI image |
| `stage` | `test` | stage for all provider jobs |
| `spec_job` | required | your job whose artifacts hold the spec and samples |
| `spec_path` | `target/openapi.json` | must match `spec.path` in `argus.yaml` |
| `samples_dir` | `target/argus-samples` | `<operationId>/<n>.json` dumps |
| `against` | `production` | baseline for `argus spec check` (`production`, `staging`, `main`) |
| `pact_verify_image` | `artifactory.digikala.com/docker/debian:bookworm-slim` | image for `argus-pact-verify-only` |
| `pact_verify_command` | `""` | runs only your Pact verification test |

### `verify-only`

| Input | Default |
|---|---|
| `stage` | `test` |
| `pact_verify_image` | `artifactory.digikala.com/docker/debian:bookworm-slim` |
| `pact_verify_command` | `""` |

### `consumer-go`

| Input | Default | Meaning |
|---|---|---|
| `golang_pact_image` | `artifactory.digikala.com/argus/golang-pact:1.26` | Debian golang + `libpact_ffi` (never Alpine) |
| `argus_image` | `artifactory.digikala.com/argus/argus-cli:1.0.0` | provides `pact-broker` for publishing |
| `stage` | `test` | |
| `packages` | `./...` | packages for the default command |
| `test_command` | `""` | overrides `go test -tags pact -run Pact -count=1 -p 1 <packages>` |
| `pact_dir` | `pacts` | PactDir your tests write to |

### `deploy-gate`

| Input | Default | Meaning |
|---|---|---|
| `argus_image` | `artifactory.digikala.com/argus/argus-cli:1.0.0` | |
| `environment` | `production` | `staging` or `production` |
| `mode` | `pipeline` | `pipeline` or `release-mr` |
| `deploy_branch` | `main` | branch whose pipeline deploys, or target of the release MR |
| `stage` | `deploy` | |
| `record_after` | `verify_rollout` | job that proves the rollout is healthy |
| `version_source` | `merge-parent` | `merge-parent` (use `HEAD^2` on a merge commit) or `commit` |

## Variables (set once by the platform team)

| Variable | Protected | Purpose |
|---|---|---|
| `ARGUS_REGISTRY_URL`, `ARGUS_OIDC_TOKEN_URL`, `PACT_BROKER_BASE_URL` | no | endpoints |
| `ARGUS_RO_OIDC_CLIENT_ID/SECRET`, `ARGUS_RO_PACT_BROKER_USERNAME/PASSWORD` | no | read-only, used on MRs and feature branches |
| `ARGUS_RW_OIDC_CLIENT_ID/SECRET`, `ARGUS_RW_PACT_BROKER_USERNAME/PASSWORD` | **yes** | read-write, used on protected refs |
| `ARGUS_GITLAB_TOKEN` | no | MR notes, waiver approvals, break-glass note |

`.argus-credentials` maps them to `ARGUS_OIDC_CLIENT_*` / `PACT_BROKER_*`: RW when `CI_COMMIT_REF_PROTECTED=true` and the RW value is visible, RO otherwise. `ARGUS_PACT_PUBLISH=true` only on the default branch and in webhook verify-only pipelines.

Per run: `ARGUS_BREAK_GLASS="<incident-id>: <reason>"` (can-i-deploy prints its verdict and exits 0), `ARGUS_SKIP="<incident-id>: <reason>"` (removes every argus job; `argus-skipped` logs the audit line).

## Examples

`examples/` holds docs/07 section 9 as files: `java-maven/`, `php-laravel/`, `go-huma/` (providers) and `go-consumer/` (marcopolo fragments: `.gitlab-ci.yml` include, `.gitlab/ci/argus.yml`, `.gitlab/ci/deploy.yml`, `argus.yaml`).

## Repo CI

`.gitlab-ci.yml` checks that `argus-pact-verify-only` is identical in `provider.yml` and `verify-only.yml`, lints each example through the CI Lint API, and creates a release from a SemVer tag.
