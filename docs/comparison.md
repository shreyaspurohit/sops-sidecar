# Comparison with other tools

Several good tools exist in this space. This page states what each one needs and how its secrets reach a container, so you can choose. Facts were checked against each project's current README, source, or official docs in November 2026; links are inline.

## At a glance

| Tool | Runs where | Needs | Secrets reach the container as | Rotation |
|---|---|---|---|---|
| `sops-sidecar` | A container in the stack | Docker and Compose v2.20.0+ | Files in a tmpfs named volume, mounted read-only | `SIGHUP`, files replaced in place |
| [`compose-sops`](https://github.com/greboid/compose-sops) | Host binary run by Compose (provider service) | Compose 2.36+ and the `compose-sops` binary on the host `PATH` of whatever runs Compose | Files on host tmpfs, bind-mounted per service through Compose `secrets:` | `down` then `up` (a changed secret fails `up`) |
| [`docker-vals`](https://github.com/estie-inc/docker-vals) (v0.2.0) | Docker CLI plugin run by Compose (provider service) | Compose v5.2.0+, the plugin in `~/.docker/cli-plugins`, backend credentials on the host | Environment variables | Recreate the consumers |
| [Dockform](https://github.com/gcstr/dockform) (v0.10.0) | CLI on the operator's machine or CI | `dockform`, `sops`, and `age` (or GnuPG) where it runs | Values handed to Compose for interpolation, so wherever the compose file puts them (typically `environment:`) | Re-run `dockform apply` |
| [`sopsbox`](https://github.com/brittonr/sopsbox) | CLI on the host, before `compose up` | The `sopsbox` binary on the host | Files in `/run/secrets` on the host, which you mount yourself | Re-run it, then restart or recreate consumers |
| [Swarm secrets](https://docs.docker.com/engine/swarm/secrets/) | Docker Engine in swarm mode | Swarm mode and services (`docker stack deploy`) | Files on tmpfs at `/run/secrets/<name>` | Secrets are immutable: create a new one and `service update --secret-rm/--secret-add` |
| [`sops exec-env` / `exec-file`](https://getsops.io/docs/usage/advanced/) | Host shell | `sops` on the host | Environment of a child process, or a FIFO or temp file path | Re-run the command |

## Notes on each

**`compose-sops`.** It is the closest relative: decryption is in-process with the SOPS and age Go libraries, the MAC is verified, and each secret becomes one `0400` file on tmpfs. It is a Compose [provider service](https://docs.docker.com/compose/how-tos/provider-services/): a service with `provider: type: compose-sops` runs no container, and Compose instead executes a `compose-sops` binary it finds on the host when you run `up` and `down`. That gives it two things `sops-sidecar` lacks:

- **Per-service grants.** Each consumer sees only the secrets it lists under `secrets:`. Every container that mounts the `sops-sidecar` volume sees every file in it.
- **Key protection at rest.** An optional `setup` command seals the age key with systemd-creds (host- or TPM-bound) and decrypts it into a runtime tmpfs at boot.

The costs are a newer Compose, a binary to install and keep current on the host, and the fact that it only acts when the Compose that runs your stack can find that binary. A web UI or a containerised manager such as Portainer runs its own Compose and does not see binaries you installed in your shell. Rotation needs `down` and `up`, because Compose file secrets are single-file bind mounts that keep the old inode.

**`docker-vals`.** A provider service too, but its job is different: it resolves `ref+<backend>://` references through [vals](https://github.com/helmfile/vals) (SOPS, Vault, AWS SSM, GCP Secret Manager, 1Password, and more) and injects the values as environment variables. Pick it when your secrets live in a secret manager rather than a SOPS file in the repo. Environment variables show in `docker inspect`, which [Docker's own secrets guide](https://docs.docker.com/compose/how-tos/use-secrets/) warns against for passwords.

**Dockform.** A declarative layer over Compose that manages stacks, volumes, file sets, and SOPS secrets across one or more Docker hosts. It calls the `sops` binary where it runs and passes the decrypted values to Compose, so secrets live wherever the compose file interpolates them. Pick it when you want one tool to manage the whole deployment, not just secrets.

**`sopsbox`.** A small CLI that decrypts a SOPS file into one file per key under `/run/secrets` on the host. It has no releases, and its last commit was in March 2024. It is a reasonable pattern if you are happy to decrypt as an explicit host pre-step.

**Swarm secrets.** The native Docker answer, with encrypted storage in the Raft log and in-memory delivery. They are [only available to swarm services](https://docs.docker.com/engine/swarm/secrets/), not to containers started with `docker compose up`. If you already run Swarm, use them.

**`sops exec-env` / `exec-file`.** Built into SOPS. `exec-env` puts the values in a child process's environment, so wrapping `docker compose up` with it pushes them into Compose interpolation and, usually, into container environment. `exec-file` passes a FIFO (or a temp file with `--no-fifo`) path to the child. Good for host processes and one-off commands; it does nothing for a long-running container after the command exits.

## Where `sops-sidecar` fits

It needs only Docker and plain Compose from v2.20.0. There is nothing to install or keep current on the host. It works the same whether Compose runs from your shell, a web UI, or Portainer. Secrets stay as files on tmpfs and never appear in `docker inspect`. They survive recreation of the consuming containers, and `SIGHUP` reloads them in place.

What it trades away:

- **One more running container per stack.**
- **All-or-nothing visibility.** Every container that mounts the volume sees every secret file in it. To split secrets between services, run a second sidecar with its own SOPS file and volume.
- **A plaintext key file on the host.** `age.key` is a plain file owned by the sidecar's uid with mode `0400`, with no TPM or systemd-creds sealing.
- **A narrow scope.** It handles one SOPS file, age keys, and top-level scalar values only. It does not template, inject environment variables, or talk to Vault or cloud KMS.
