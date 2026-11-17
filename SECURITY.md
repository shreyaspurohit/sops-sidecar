# Security policy

## Supported versions

Only the latest minor release receives security fixes.

## Reporting a vulnerability

Please report privately through GitHub Security Advisories:
<https://github.com/shreyaspurohit/sops-sidecar/security/advisories/new>

Do not open a public issue for a vulnerability. Include:

- the version or image tag (`sops-sidecar version`),
- what you did, what you expected, and what happened,
- a minimal reproduction (compose file, map file; never real secrets or keys),
- the impact as you understand it.

This is a volunteer project. I aim to acknowledge reports within a few days and to fix confirmed issues as soon as I can, on a best-effort basis. Reporters are credited unless they prefer otherwise.

## Scope and verification

[docs/security-model.md](docs/security-model.md) describes what the sidecar does and does not protect.

Released images are signed with cosign keyless signing. Verify before use:

```sh
cosign verify ghcr.io/shreyaspurohit/sops-sidecar:<tag> \
  --certificate-identity-regexp 'https://github.com/shreyaspurohit/sops-sidecar/.github/workflows/release.yml@refs/tags/v.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```
