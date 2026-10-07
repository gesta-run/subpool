# CI/CD Pipeline Design

Status: Approved and implemented

## Context

Subpool currently has one manually triggered preproduction workflow. It builds
an image, publishes an immutable `sha-<commit>` tag to GHCR, and deploys that
image through AWS Systems Manager. Pull requests and main-branch changes do not
have an automated validation pipeline. New images will move to the same ECR
Public publishing model used by Gesta.

## Goals

- Give pull requests fast, deterministic Go and web validation.
- Prove that the production Dockerfile builds before changes are merged.
- Publish immutable commit images and readable release tags to ECR Public.
- Keep preproduction deployment explicit and compatible with the existing
  environment, secrets, image tags, and rollback process.
- Keep workflow time and runner usage reasonable as the repository grows.

## Non-goals

- Automatic production deployment.
- Kubernetes, Helm, or a new deployment platform.
- Changing application runtime configuration or secret names.
- Adding a new lint tool before the repository adopts one locally.

## Proposed workflows

### 1. `docker-image.yml`

Triggers:

- Pull requests targeting `main`.
- Pushes to `main`.

Validation jobs:

1. `go-test`
   - Use Go 1.24 with module caching.
   - Run `go test ./...`.
2. `web-test`
   - Use Node.js 22 with npm cache keyed by `web/package-lock.json`.
   - Run `npm ci`, `npm test`, and `npm run build` in `web`.
3. `build`
   - Depend on both test jobs.
   - Build the production Dockerfile for `linux/amd64` without pushing on pull
     requests.
   - Build and publish `linux/amd64` and `linux/arm64` on trusted refs.
   - Reuse the GitHub Actions BuildKit cache.

Publishing behavior:

- Build Linux `amd64` and `arm64` images with Buildx.
- Push `sha-<commit>` for every run.
- Also push `latest` and `main` for the default branch and the Git tag for
  releases.
- Attach OCI source and revision labels.
- Authenticate with the existing AWS credentials and
  `aws-actions/amazon-ecr-login`, matching Gesta's pipeline.
- Publish to `public.ecr.aws/cloudpilotai/subpool`.
- Use read-only GitHub permissions; ECR writes are authorized by AWS IAM.
- Never publish an image when Go or web validation fails.

The immutable SHA tag remains the deployment source of truth. Mutable tags are
only discovery aliases and are not used for rollback decisions.

Stale runs for the same pull request or branch are cancelled. Pull requests do
not receive AWS credentials. Branch protection should require all three jobs.

### 2. Existing `deploy-pre.yml`

Keep `workflow_dispatch` and the `preproduction` environment. Refactor it to
deploy a previously published immutable image instead of rebuilding source.
The image is the selected Git ref's `sha-<commit>` tag, so the same ref input
also supports rollback. Preserve the current secret names, remote Compose path,
health check, and AWS Systems Manager transport.

## Compatibility

- Existing deployments remain manual.
- Existing full `sha-<commit>` image tags continue to work; the default
  registry changes from GHCR to ECR Public.
- Existing GitHub environment and repository secrets are unchanged.
- Go 1.24, Node.js 22, Docker Compose, and the current AWS deployment path
  remain unchanged.
- No application or database migration is introduced.

## Performance and scale

- Go modules, npm packages, and Docker layers use lockfile-aware caches.
- Independent Go and web jobs run in parallel.
- Superseded pull-request runs are cancelled.
- Release images are built once and promoted by immutable digest or SHA tag;
  deployments do not repeat the expensive multi-platform build.
- If runner demand becomes material, path filtering may skip unrelated jobs,
  but the Docker build remains a required merge check to prevent integration
  drift.

## Security

- Pull-request CI receives no deployment secrets and cannot write packages.
- Publishing occurs only from trusted repository refs.
- Deployment secrets stay scoped to the protected `preproduction` environment.
- Actions are pinned to stable major versions initially; commit-SHA pinning can
  be adopted as a repository-wide hardening policy.

## Rollout

1. Add `docker-image.yml` and make its checks required.
2. Verify an immutable ECR Public image from `main`.
3. Refactor `deploy-pre.yml` to consume that image.
4. Exercise a preproduction deploy and a rollback to an earlier SHA.

## Acceptance criteria

- A pull request runs Go tests, web tests/build, and a production image build.
- A failing validation job prevents image publication.
- Main and release refs produce the documented ECR Public tags for both target
  architectures.
- Preproduction deploys an existing immutable image without rebuilding it.
- A previous immutable image can be selected for rollback.
- No existing Subpool runtime configuration or deployment secret must change.

## Review decision

Approve or revise these defaults before implementation:

- Preserve full compatibility with the current manual preproduction workflow.
- Publish to ECR Public on `main` and version tags, without automatic
  production deployment.
- Support both `amd64` and `arm64` images.
