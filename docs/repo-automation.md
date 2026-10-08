# Repository Automation

What each piece of automation does, what it needs, and when it is safe to remove. The workflows do not repeat
this in header comments; this is the map.

## Workflows

| Workflow | What it does | Needs |
|----------|--------------|-------|
| `hygiene.yml` | Workflow lint, YAML lint, repository consistency, workflow security audit, action pin check, dependency review. Ends in a `required-checks` aggregate job. | nothing |
| `ci.yml` | Build, test, lint, tidy, licence check, vulnerability report, and a container image smoke test. Skips itself without `go.mod`. Vulnerabilities are reported in the job summary and as a sticky pull request comment listing the fixable ones; only a scan that produced no usable report fails. | nothing |
| `codeql.yml` | Static analysis of the workflows and the Go code. | public repo, or Advanced Security |
| `dco.yml` | Fails a pull request whose commits lack a sign-off. | nothing |
| `scorecard.yml` | OpenSSF Scorecard. Skips on private repositories. | `SCORECARD_TOKEN` to also score branch protection |
| `pr-title.yml` | Requires a conventional-commit pull request title. | nothing |
| `labeler.yml`, `pr-size-labeler.yml` | Label pull requests by path and by size. | nothing |
| `welcome.yml` | Greets first-time contributors. | nothing |
| `apply-settings.yml` | Applies `.github/settings.yml`, and checks for drift weekly. | `SETTINGS_TOKEN` |
| `release-please.yml` | Opens the app and chart release pull requests and triggers publishing. | nothing |
| `release-assets.yml` | Builds, attests and uploads binaries and their SBOM, from the commit the release tag resolves to. Called by `release-please.yml`: delete its `publish-release-assets` job with it. Does nothing without `go.mod`. | nothing |
| `publish-image.yml` | Builds, pushes to `8gears.container-registry.com/healthprobe/healthprobe`, verifies the platform set (`task image:verify`), signs and attests the image, from the commit the release tag resolves to. Called by `release-please.yml`: delete its `publish-image` job with it. | the federated Harbor robot, see [Image registry](#image-registry) |
| `pr-image.yml` | Builds, pushes to `8gears.container-registry.com/8gcr-dev/healthprobe`, signs and SBOM-attests a preview image per pull request, `pr-<N>`, and comments the reference. Runs when the diff against `main` touches an image input; in a stack only for the top pull request. | the federated Harbor robot; skipped for fork and Dependabot pull requests |

## Configuration

| File | Purpose |
|------|---------|
| `.github/settings.yml` | Repository settings, labels, security toggles and the branch ruleset, as code |
| `.github/labeler.yml` | Path to label mapping |
| `.github/dependabot.yml` | Dependency updates, including the action SHA pins |
| `.github/CODEOWNERS` | Automatic reviewer assignment |
| `.github/dco.yml` | dco2 app behaviour, if the app is installed |
| `versions.env` | Every tool version pin, read by both the Taskfile and CI |
| `.yamllint`, `.golangci.yaml` | Linter configuration |
| `.release-please/config-app.json`, `.release-please/manifest-app.json`, `version.txt` | Release state. The directory is excluded from releases, so a second release line (a chart, say) can exclude the app line's state the same way |
| `optional/renovate.json` | Renovate config: a replacement for Dependabot, or a complement limited to `versions.env` and the base image digest |

## Scripts

| File | Purpose |
|------|---------|
| `.github/scripts/apply-settings.js` | Applies, verifies or drift-checks `settings.yml`. Reads JSON the workflow converts, so it needs no YAML parser |
| `.github/scripts/repo-lint.py` | Repository consistency checks, also run by `task lint:repo` |

## Image registry

Images go to Harbor at `8gears.container-registry.com` (8gcr), never to GHCR. Both projects are public.

| What | Where | Pushed by |
|------|-------|-----------|
| Releases | `8gears.container-registry.com/healthprobe/healthprobe:vX.Y.Z` and `:latest` | `robot_gh-healthprobe-push` (id 1060202), push on `healthprobe` only |
| Pull request previews | `8gears.container-registry.com/8gcr-dev/healthprobe:pr-<N>` | `robot_gh-healthprobe-preview` (id 1060235), push on `8gcr-dev` only |

There is no registry secret. Each publishing job mints a GitHub OIDC token whose audience is `https://` plus the
registry address (`https://8gears.container-registry.com` by default) and logs in with it as the password, username
`jwt`. Harbor picks the federated robot whose claim rules all match the token, preferring the one with the most rules:

- The preview robot requires `repository == container-registry/healthprobe`.
- The release robot also requires `job_workflow_ref == container-registry/healthprobe/.github/workflows/publish-image.yml@refs/heads/main`.
  A workflow run from a pull request branch cannot produce that value, so it can never overwrite a release tag.

Releases carry a cosign signature, an SBOM attestation and a build provenance attestation, stored next to the
image. Previews carry the signature and the SBOM attestation only.

To publish elsewhere, set the `REGISTRY_ADDRESS`, `REGISTRY_PROJECT` and `PR_REGISTRY_PROJECT` repository variables.
The target registry needs federated robots of its own that trust GitHub's issuer, with the same claim split.

## Secrets

| Secret | Used by | Without it |
|--------|---------|------------|
| `SETTINGS_TOKEN` | `apply-settings.yml` | The job reports what it skipped and succeeds. `GITHUB_TOKEN` can only manage labels. |
| `SCORECARD_TOKEN` | `scorecard.yml` | Branch protection is not scored. Everything else works. |

Use a fine-grained personal access token scoped to this repository, with **Administration: read and write** and
**Metadata: read**.

## Things That Will Bite You

- **The release pull request's runs wait for approval.** GitHub holds every run on a branch pushed by
  `github-actions[bot]` at "action required" until a person approves it. See [RELEASES.md](RELEASES.md) before
  making any check required.
- **Never require a path-filtered workflow as a status check.** It does not report at all on a pull request that
  misses its filter, and the check waits forever. Require `required-checks` instead.
- **A `paths` filter sees one pull request's slice, not its stack.** In a native GitHub stack (`gh stack`) a
  path-filtered workflow runs for the top pull request only if its own diff matches. `pr-image.yml` and
  `pr-chart.yml` keep their allowlist in a job and diff against the stack base; only the top of a stack
  publishes, on the `stacked` event (GitHub links the stack after opening its pull requests) and on every
  push. `.github/actionlint.yaml` ignores actionlint's unknown-type error for `stacked`. Pull requests chained
  by hand without `gh stack` are not a stack: the bottom one is an ordinary pull request, the ones above it
  match no `branches: [main]` trigger.
- **`pull_request_target` workflows must never check out the pull request.** Three workflows use that trigger and
  say so; `repo-lint` fails the build if one ever gains a checkout step.
- **A `feat:` confined to an excluded path releases nothing.** `exclude-paths` is set to `docs`, `.github`, `.release-please`, `deploy` and `taskfile`,
  and it is evaluated per file: one file outside pulls the whole commit back in.
- **An open release pull request only refreshes when its body would change.** `always-update: true` in
  `.release-please/config-app.json` forces a rewrite on every push to `main`. Do not remove it.
