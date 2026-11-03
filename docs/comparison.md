# Comparison with other tools

Several good tools exist in this space. This table states what each needs, factually, so you can choose.

| Tool | What it needs that `sops-sidecar` avoids | Pick it instead when |
|---|---|---|
| `compose-sops` (Compose "provider" service) | Docker Compose 2.36 or newer with provider support, plus host-side plugin tooling. Many appliance Docker builds ship older Compose. | Your Compose is recent and you prefer Compose to manage secret provisioning natively. |
| `sopsbox` | A CLI on the host that decrypts to `/run/secrets` before you run compose. | You are happy installing a host tool and want decryption as an explicit pre-step. |
| Dockform and similar | A separate orchestration tool wrapping compose. | You want one tool to manage the whole stack, not only secrets. |
| Docker Swarm secrets | Swarm mode. | You already run Swarm; its native secrets are the right choice. |
| `sops exec-env` | A host install of sops; secrets end up as environment variables, visible in `docker inspect`. | The secret is for a host process or a short-lived command rather than a long-running container. |

`sops-sidecar` needs only Docker and plain Compose (v2.20.0 and later), keeps secrets as files in tmpfs, and survives recreation of the consuming containers. The trade-off: it adds one running container per stack, and it does one thing (one SOPS file, age keys, top-level scalar values). It does not template, inject environment variables, or talk to Vault or cloud KMS.
