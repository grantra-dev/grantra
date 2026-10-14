# Releases and verifying them

Pushing a version tag (`v1.2.3`) runs [`.github/workflows/release.yml`](../.github/workflows/release.yml), which publishes:

| What | Where | Signed with | SBOM |
|---|---|---|---|
| Container image for linux/amd64 and linux/arm64 | `ghcr.io/grantra-dev/grantra:1.2.3` (and `:1.2`) | cosign, keyless | SPDX, attached to the image as a cosign attestation; BuildKit SBOM and provenance stored with the image too |
| Binaries for Linux, macOS and Windows | the GitHub release | cosign, keyless, over `checksums.txt` | `grantra.spdx.json` in the release, listed in `checksums.txt` |

Keyless signing means there is no long-lived signing key to leak: each release is signed with a short-lived certificate tied to the GitHub Actions workflow that built it, and recorded in Sigstore's public transparency log.

## Verify the image

```sh
cosign verify ghcr.io/grantra-dev/grantra:1.2.3 \
  --certificate-identity-regexp '^https://github.com/grantra-dev/grantra/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Read its SBOM:

```sh
cosign verify-attestation ghcr.io/grantra-dev/grantra:1.2.3 --type spdxjson \
  --certificate-identity-regexp '^https://github.com/grantra-dev/grantra/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  | jq -r .payload | base64 -d | jq .predicate
```

In Kubernetes, a policy controller such as Sigstore's policy-controller or Kyverno can refuse any Grantra image that fails the same check.

## Verify a binary

Download the archive, `checksums.txt` and `checksums.txt.sigstore.json` from the release, then:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp '^https://github.com/grantra-dev/grantra/.github/workflows/release.yml@refs/tags/v' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
sha256sum --check --ignore-missing checksums.txt
```
