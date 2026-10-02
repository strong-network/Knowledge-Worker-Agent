# Public repository CI and container releases

The workflows in `.github/workflows/` build, test and publish Knowledge Worker
Agent from the repository root. Image publishing targets both GHCR and Docker Hub
by default.

## Require a build before merging

`workflows/ci.yml` runs on every pull request, pushes to `main`/`master`, and merge
queue checks. It runs `make ci` (frontend type-check, Go vet/build/tests and
govulncheck), followed explicitly by **`make build`** to produce the Linux binary.

In the public repository, create an active branch ruleset for its default branch:

1. Require a pull request before merging.
2. Require status checks: select **Build & test** from the **CI** workflow after
   its first run. Require the branch to be up to date, or enable the merge queue.
3. Configure bypass permissions to match who may override this rule.

Workflow YAML runs the check; the repository ruleset makes it mandatory. Avoid
path filters on this required workflow so documentation-only PRs report a result.

## Primary registry: GitHub Container Registry

`workflows/publish-image.yml` publishes to:

```text
ghcr.io/strong-network/knowledge-worker-agent
```

Triggers:

- **Publish a GitHub release:** its tag must match `VERSION`, with an optional
  `v` prefix. A release tagged `v1.5.0` with `VERSION` at `1.5.0` is accepted.
- **Manual Run workflow on the default branch.**

**Both produce the same tags: the version from `VERSION`, and `latest`.** What you
pull therefore does not depend on how the publication was triggered. Earlier runs
tagged manual publications `sha-<12-character SHA>`, which is no longer the case.

Both paths first run the same CI and `make build` through a reusable workflow.
The image is built with `Dockerfile.release`, its pinned dependencies and embedded
opencode/dictation components. Currently it supports **linux/amd64** only.

Two consequences worth knowing. `latest` moves with every publication, including a
manual one, so it is a convenience tag rather than something to depend on. And
publishing twice without changing `VERSION` replaces the image at that version
tag: bump `VERSION` for anything consumers may already have pulled, and use the
digest printed in the run summary when you need an immutable reference.

The README documents the current version and the pull commands for both
registries. A test (`cmd/server/imageversion_test.go`) fails if they fall behind
`VERSION`, so bumping the version means updating the README in the same change.

GHCR authentication uses the public repository's **`GITHUB_TOKEN`**, with
`packages: write` scoped to the publishing job. No additional GHCR PAT is needed.
Ensure organization Actions/package policy allows this. If the package already
exists, grant this repository Actions access to it. After the first push, check
the package's **Package settings → Change visibility → Public**: a public source
repository does not guarantee a newly created package is public. Verify anonymous
pull access after setting visibility.

## Secondary registry: Docker Hub (enabled by default)

Every image publication also pushes the same tags to
`docker.io/strongnetwork/knowledge-worker-agent`. No repository variables are
needed to enable this default. The two Docker Hub secrets below are required;
missing credentials fail the mirror job rather than silently skipping it.

Configure these in the **public** repository's Actions settings:

| Type | Name | Value |
| --- | --- | --- |
| Variable (optional) | `DOCKERHUB_MIRROR_ENABLED` | Enabled when unset; set to `false` to skip Docker Hub publishing |
| Variable (optional) | `DOCKERHUB_IMAGE` | Defaults to `strongnetwork/knowledge-worker-agent`; set only to override with another owned `namespace/repository` |
| Secret | `DOCKERHUB_USERNAME` | Docker Hub account authorized to push |
| Secret | `DOCKERHUB_TOKEN` | Docker Hub access token with write access to the image repository |

Create that image repository and set it public if anonymous pulls are intended.
The mirror job copies the GHCR image by digest using Buildx, without rebuilding,
and applies the same version and `latest` tags. A mirror failure fails that job
but does not remove the already published GHCR image. Retry the failed mirror job
to copy the same digest.

The registry credentials are used only by the image workflow, never by PR build
checks. Publishing does not happen on pull requests or ordinary branch pushes.
