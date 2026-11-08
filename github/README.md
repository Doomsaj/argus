# argus GitHub Actions

Composite actions that mirror the GitLab components in [`ci-templates/`](../ci-templates/README.md) (spec: [docs/07-ci-templates.md](../docs/07-ci-templates.md)). Reference them as `OWNER/argus/github/actions/<name>@main`.

| Action | GitLab component | What it runs |
|---|---|---|
| `provider` | `provider` | optional spec hook, `argus spec lint`, `argus spec check`, `argus verify samples`; `argus spec publish` on push to the default branch |
| `verify-only` | `verify-only` | on `repository_dispatch` `pact-verify`: validate payload, check out `provider_version`, run your Pact verification, publish results |
| `consumer-go` | `consumer-go` | Go Pact consumer tests; `pact-broker publish` on push to the default branch |
| `deploy-gate` | `deploy-gate` | `argus can-i-deploy --to <env>` before deploy, `argus deploy record --env <env>` after |

## Runner

Self-hosted macOS runner with labels `self-hosted, argus-local`, and Go 1.26, Java 21, Maven and `pact-broker` (pact ruby standalone) on `PATH`. No Docker. The actions that need argus (`provider`, `deploy-gate`) build it from the action's own checkout: `go build -o "$RUNNER_TEMP/argus" ./cmd/argus`, run in the argus repo root (`$GITHUB_ACTION_PATH/../../..`). For pact-go consumer tests, `libpact_ffi` must be installed and on `DYLD_LIBRARY_PATH` ([docs/09](../docs/09-consumer-onboarding.md)).

## Secrets

| Repo secret | Action input | Used by |
|---|---|---|
| `ARGUS_REGISTRY_URL` | `registry-url` | provider, deploy-gate |
| `PACT_BROKER_BASE_URL` | `broker-url` | all |
| `ARGUS_RO_PACT_BROKER_USERNAME` / `_PASSWORD` | `broker-username` / `broker-password` | provider, deploy-gate |
| `ARGUS_RW_PACT_BROKER_USERNAME` / `_PASSWORD` | `broker-rw-username` / `broker-rw-password` | all |
| `GITHUB_TOKEN` (automatic) | `github-token` | provider, deploy-gate (exported as `ARGUS_GITHUB_TOKEN`), verify-only (checkout) |

## RO/RW mapping

The action picks the broker user and exports `PACT_BROKER_BASE_URL`, `PACT_BROKER_USERNAME`, `PACT_BROKER_PASSWORD` (and `ARGUS_REGISTRY_URL`) for argus, `pact-broker` and your own later steps:

| Event | Broker user | `ARGUS_PACT_PUBLISH` |
|---|---|---|
| `push` to the default branch (provider, consumer-go) | RW | `true` |
| `repository_dispatch` `pact-verify` (verify-only) | RW | `true` |
| `push` or `workflow_dispatch` on `deploy-branch` (deploy-gate) | RW | - |
| anything else (pull requests, feature branches) | RO | `false` |

If the RW inputs are empty, provider and deploy-gate fall back to RO. Writes then fail with 401/403, as in GitLab.

GitHub repo secrets are visible to every workflow run in the repo, except runs from forks. So unlike GitLab protected variables, this split is enforced only by the actions. To enforce it on GitHub's side, keep the RW secrets in an Environment that only the default branch may use.

The argus CLI reads GitLab's predefined names. The actions export `CI_COMMIT_SHA=$GITHUB_SHA` (deploy-gate: the resolved version), `CI_COMMIT_REF_NAME=${GITHUB_HEAD_REF:-$GITHUB_REF_NAME}` and `CI_PIPELINE_ID=$GITHUB_RUN_ID`.

## Inputs

### `provider`

| Input | Default | Required |
|---|---|---|
| `spec-command` | `""` | no. Mirrors `spec_job`: command that writes the spec and samples and runs Pact verification, with `PACT_BROKER_*` and `ARGUS_PACT_PUBLISH` set. Leave empty if an earlier step already wrote the files |
| `spec-path` | `target/openapi.json` | no |
| `samples-dir` | `target/argus-samples` | no |
| `against` | `production` | no |
| `argus-skip` | `""` | no |
| `registry-url`, `broker-url`, `broker-username`, `broker-password` | - | yes |
| `broker-rw-username`, `broker-rw-password`, `github-token` | `""` | no |

Lint, check and samples all run, and the step fails with the highest exit code. `argus-report.md` is appended to the job summary. Set `concurrency:` on the workflow to serialise publishes (GitLab `resource_group`). On `repository_dispatch` the action does nothing; use `verify-only` in its own workflow.

### `verify-only`

| Input | Default | Required |
|---|---|---|
| `pact-verify-command` | `""` (the step fails if empty) | no |
| `argus-skip` | `""` | no |
| `broker-url`, `broker-rw-username`, `broker-rw-password` | - | yes |
| `github-token` | `${{ github.token }}` | no |

Exports `PACT_URL`, `PACT_CONSUMER_VERSION`, `PACT_PROVIDER_VERSION`, `ARGUS_VERIFY_ONLY=true` and `ARGUS_PACT_PUBLISH=true` for your command. `provider_version` must be a 40-hex SHA, and `pact_url` must start with `broker-url`.

### `consumer-go`

| Input | Default | Required |
|---|---|---|
| `packages` | `./...` | no |
| `test-command` | `""`, meaning `go test -tags pact -run 'TestPact_' -count=1 -p 1 <packages>` | no |
| `pact-dir` | `pacts` | no |
| `argus-skip` | `""` | no |
| `broker-url` | - | yes |
| `broker-rw-username`, `broker-rw-password` | `""` | no (required on the default branch) |

Removes `pact-dir`, runs the tests with `CGO_ENABLED=1`, fails if no `pact-dir/*.json` was written. On push to the default branch: `pact-broker publish <pact-dir> --consumer-app-version $GITHUB_SHA --branch $GITHUB_REF_NAME`.

### `deploy-gate`

| Input | Default | Required |
|---|---|---|
| `command` | - | yes: `can-i-deploy` or `deploy-record` (replaces `record_after`: put the step after your rollout) |
| `environment` | `production` | no |
| `mode` | `pipeline` | no: `pipeline` or `release-mr` |
| `deploy-branch` | `main` | no |
| `version-source` | `merge-parent` | no: `merge-parent` or `commit` |
| `break-glass` | `""` | no: `ARGUS_BREAK_GLASS` |
| `argus-skip` | `""` | no |
| `registry-url`, `broker-url`, `broker-username`, `broker-password` | - | yes |
| `broker-rw-username`, `broker-rw-password`, `github-token` | `""` | no |

When each command runs:

* `can-i-deploy` in `pipeline` mode: on `push` or `workflow_dispatch` to `deploy-branch`.
* `can-i-deploy` in `release-mr` mode: on `pull_request` into `deploy-branch`. Make the check required in branch protection, the equivalent of "Pipelines must succeed".
* `deploy-record`: on `push` or `workflow_dispatch` to `deploy-branch`. It retries 3 times and uses `continue-on-error`.

On any other event the action does nothing. `merge-parent` uses `HEAD^2` when `HEAD` is a merge commit. On a shallow checkout the action fetches one more level of history to find it.

### `argus-skip` / `break-glass`

Both take `"<incident-id>: <reason>"`, usually from a `workflow_dispatch` input:

* `argus-skip` skips every argus step and logs an `ARGUS_AUDIT skip ...` line.
* `break-glass` makes `can-i-deploy` print its verdict and exit 0.

## Example workflows

Provider, `.github/workflows/argus.yml` (Maven):

```yaml
name: provider
on:
  pull_request:
  push:
    branches: [main]
concurrency:
  group: argus-${{ github.ref }}   # serialises spec publish on main (GitLab resource_group)
jobs:
  argus:
    runs-on: [self-hosted, argus-local]
    steps:
      - uses: actions/checkout@v4
      - uses: OWNER/argus/github/actions/provider@main
        with:
          spec-command: >-
            mvn -B verify
            -Dpact.provider.version=$GITHUB_SHA -Dpact.provider.branch=$GITHUB_REF_NAME
            -Dpactbroker.url=$PACT_BROKER_BASE_URL
            -Dpactbroker.auth.username=$PACT_BROKER_USERNAME -Dpactbroker.auth.password=$PACT_BROKER_PASSWORD
            -Dpact.verifier.publishResults=$ARGUS_PACT_PUBLISH
          registry-url: ${{ secrets.ARGUS_REGISTRY_URL }}
          broker-url: ${{ secrets.PACT_BROKER_BASE_URL }}
          broker-username: ${{ secrets.ARGUS_RO_PACT_BROKER_USERNAME }}
          broker-password: ${{ secrets.ARGUS_RO_PACT_BROKER_PASSWORD }}
          broker-rw-username: ${{ secrets.ARGUS_RW_PACT_BROKER_USERNAME }}
          broker-rw-password: ${{ secrets.ARGUS_RW_PACT_BROKER_PASSWORD }}
          github-token: ${{ secrets.GITHUB_TOKEN }}
```

Provider webhook verification, `.github/workflows/pact-verify.yml`:

```yaml
name: pact-verify
on:
  repository_dispatch:
    types: [pact-verify]
jobs:
  verify:
    runs-on: [self-hosted, argus-local]
    steps:
      - uses: OWNER/argus/github/actions/verify-only@main
        with:
          pact-verify-command: >-
            mvn -B -Dtest='*PactVerificationTest' -Dsurefire.failIfNoSpecifiedTests=false
            -Dpact.provider.version=$PACT_PROVIDER_VERSION
            -Dpactbroker.url=$PACT_BROKER_BASE_URL
            -Dpactbroker.auth.username=$PACT_BROKER_USERNAME -Dpactbroker.auth.password=$PACT_BROKER_PASSWORD
            -Dpact.filter.pacturl=$PACT_URL -Dpact.verifier.publishResults=$ARGUS_PACT_PUBLISH test
          broker-url: ${{ secrets.PACT_BROKER_BASE_URL }}
          broker-rw-username: ${{ secrets.ARGUS_RW_PACT_BROKER_USERNAME }}
          broker-rw-password: ${{ secrets.ARGUS_RW_PACT_BROKER_PASSWORD }}
```

Go consumer (default branch `develop`), `.github/workflows/argus.yml`:

```yaml
name: consumer
on:
  pull_request:
  push:
    branches: [develop]
jobs:
  pact:
    runs-on: [self-hosted, argus-local]
    steps:
      - uses: actions/checkout@v4
      - uses: OWNER/argus/github/actions/consumer-go@main
        with:
          packages: ./modules/application/internal/product/...
          broker-url: ${{ secrets.PACT_BROKER_BASE_URL }}
          broker-rw-username: ${{ secrets.ARGUS_RW_PACT_BROKER_USERNAME }}
          broker-rw-password: ${{ secrets.ARGUS_RW_PACT_BROKER_PASSWORD }}
```

Deploy with gate and record, `.github/workflows/deploy.yml`:

```yaml
name: deploy
on:
  push:
    branches: [main]
  workflow_dispatch:
    inputs:
      break-glass:
        description: '"<incident-id>: <reason>" to deploy despite a red gate'
        required: false
        default: ""
jobs:
  deploy:
    runs-on: [self-hosted, argus-local]
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 10   # version-source merge-parent needs HEAD^2
      - uses: OWNER/argus/github/actions/deploy-gate@main
        with:
          command: can-i-deploy
          environment: production
          deploy-branch: main
          break-glass: ${{ inputs.break-glass }}
          registry-url: ${{ secrets.ARGUS_REGISTRY_URL }}
          broker-url: ${{ secrets.PACT_BROKER_BASE_URL }}
          broker-username: ${{ secrets.ARGUS_RO_PACT_BROKER_USERNAME }}
          broker-password: ${{ secrets.ARGUS_RO_PACT_BROKER_PASSWORD }}
          broker-rw-username: ${{ secrets.ARGUS_RW_PACT_BROKER_USERNAME }}
          broker-rw-password: ${{ secrets.ARGUS_RW_PACT_BROKER_PASSWORD }}
          github-token: ${{ secrets.GITHUB_TOKEN }}
      - run: ./deploy.sh production   # your rollout; wait until healthy
      - uses: OWNER/argus/github/actions/deploy-gate@main
        with:
          command: deploy-record
          environment: production
          deploy-branch: main
          registry-url: ${{ secrets.ARGUS_REGISTRY_URL }}
          broker-url: ${{ secrets.PACT_BROKER_BASE_URL }}
          broker-username: ${{ secrets.ARGUS_RO_PACT_BROKER_USERNAME }}
          broker-password: ${{ secrets.ARGUS_RO_PACT_BROKER_PASSWORD }}
          broker-rw-username: ${{ secrets.ARGUS_RW_PACT_BROKER_USERNAME }}
          broker-rw-password: ${{ secrets.ARGUS_RW_PACT_BROKER_PASSWORD }}
```

## Pact Broker webhook for GitHub

The Broker sends `contract_requiring_verification_published` to GitHub's [repository dispatch API](https://docs.github.com/en/rest/repos/repos#create-a-repository-dispatch-event): `POST https://api.github.com/repos/OWNER/REPO/dispatches`. The workflow runs on the default branch, and `verify-only` checks out `provider_version`.

```json
{
  "description": "argus: verify changed pacts for product-detail (GitHub repository_dispatch)",
  "provider": { "name": "product-detail" },
  "events": [ { "name": "contract_requiring_verification_published" } ],
  "enabled": true,
  "request": {
    "method": "POST",
    "url": "https://api.github.com/repos/OWNER/REPO/dispatches",
    "headers": {
      "Content-Type": "application/json",
      "Accept": "application/vnd.github+json",
      "Authorization": "Bearer <token>"
    },
    "body": {
      "event_type": "pact-verify",
      "client_payload": {
        "pact_url": "${pactbroker.pactUrl}",
        "consumer_version": "${pactbroker.consumerVersionNumber}",
        "provider_version": "${pactbroker.providerVersionNumber}"
      }
    }
  }
}
```

* `<token>` is a fine-grained PAT that can access only the provider repo, with "Contents: read and write" (required by the dispatches endpoint). The Broker stores it in plain text in the webhook definition ([docs/05](../docs/05-platform-pact-broker.md)), so keep its scope minimal.
* The Broker's webhook host whitelist must allow `api.github.com`, scheme `https`, method `POST`.
* Apply with the RW broker user: `PUT $PACT_BROKER_BASE_URL/webhooks/<uuid>` with the body above. Test with `POST .../webhooks/<uuid>/execute`.
