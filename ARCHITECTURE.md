# Architecture

`sops-sidecar` decrypts one SOPS file and writes each secret as its own file
into a tmpfs volume that neighboring containers mount read-only. This document
is the contract the code follows: package boundaries, exported API, data flow,
ownership, and vocabulary.

## Glossary

One term per concept, used identically in code, logs, docs, and env vars.

| Term | Meaning |
|---|---|
| **SOPS file** | The one SOPS-encrypted YAML or JSON input file (`SOPS_FILE`). |
| **age key file** | The file holding the age identity (private key) that decrypts the SOPS file (`SOPS_AGE_KEY_FILE`). |
| **key** | A top-level key in the SOPS file. Key names are not secret and may appear in errors and debug logs. |
| **value** | The decrypted bytes of a key. Values are secret and never appear in logs, errors, or output. |
| **secret** | A key together with its value. |
| **map file** | The optional YAML file (`SECRETS_MAP`) choosing which keys are written, under which file names, with which owner and mode. |
| **output dir** | The tmpfs directory secrets are written to (`OUTPUT_DIR`). |
| **file name** | A secret's file name inside the output dir. Always a single path element. |
| **plan** | The validated list of files to write, computed purely from secrets plus the map file. |
| **ready marker** | The file `.sops-sidecar-ready` in the output dir; it exists only while the last load succeeded. |
| **load** | One pass of read inputs → decrypt → plan → write → mark ready. |
| **reload** | A load triggered by `SIGHUP` while `run` is running. |

## Data flow

```
             ┌──────────── pure core ────────────┐
env ──► config.FromEnv ──► Config
files ─(cmd reads)─► decrypt.ParseIdentities ─┐
                     decrypt.Decrypt ─────────┼─► []decrypt.Secret
                     config.ParseMap ─────────┘           │
                                         config.Map.Plan ◄┘ ──► config.Plan
             └───────────────────────────────────┘           │
             ┌──────────── I/O shell ────────────┐            ▼
             │ health.Marker.Clear → writer.Dir.Write (each file)
             │ → writer.Dir.Remove (stale names) → health.Marker.Set
             └───────────────────────────────────┘
```

`run` = load, then block; on `SIGHUP` load again; on `SIGTERM`/`SIGINT` exit.

## Dependency graph

```
cmd/sops-sidecar ──► config, decrypt, writer, health
config ──► decrypt, writer
health ──► writer
decrypt, writer ──► (no internal packages)
internal/sopstest ──► (test support only; imported only by _test.go files)
```

No cycles. No package imports `cmd`. Packages use each other's exported API only.

## Domain ownership map

Each concept, its rules, and its constants are defined in exactly one package.

| Concept | Owner | Constants / types it owns |
|---|---|---|
| Env var names, input-path defaults, boolean/level parsing, `SOPS_AGE_KEY` refusal | `config` | `Env*`, `DefaultSOPSFile`, `DefaultAgeKeyFile`, `DefaultMapFile`, `DefaultOutputDir`, `Config` |
| Map file schema and rules (which keys, which names, inherited defaults, duplicates), the plan | `config` | `Map`, `Plan`, `Entry` |
| SOPS formats, age identity parsing, decryption, MAC check, top-level scalar rule, value redaction | `decrypt` | `Format*`, `Identities`, `Secret`, `Value` |
| File names, reserved prefix, modes, owners, built-in mode/owner defaults, atomic writes, removal, tmpfs check, mount-table parsing | `writer` | `ReservedPrefix`, `DefaultMode`, `KeepID`, `ProcMounts`, `Name`, `Mode`, `Owner`, `File`, `Dir` |
| Readiness: the ready marker's name, mode, setting, clearing, checking | `health` | `MarkerName`, `MarkerMode`, `Marker` |
| CLI, subcommands, exit codes, logging setup, reading input files (size limit), signals, lifecycle | `cmd/sops-sidecar` | `exit*`, `maxInputBytes`, subcommand names |

## Pure core vs I/O shell

**Pure core** (no file system, env, clock, or process state; inputs → outputs):
`config.FromEnv` (env is passed in as a lookup function), `config.ParseMap`,
`config.Map.Plan`, `decrypt.FormatForPath`, `decrypt.ParseIdentities`,
`decrypt.Decrypt`, `writer.ParseName`, `writer.ParseReservedName`,
`writer.ParseMode`, `writer.Stale`, `writer.MountType`.

**I/O shell** (thin; executes decisions the core made):
`writer.Open`, `writer.Dir.*`, `health.NewMarker`, `health.Marker.*`,
`health.Check`, and everything in `cmd/sops-sidecar` (reading files, signals,
logging, exit codes).

## Package APIs

Signatures are the contract. Unexported helpers are free-form. Every exported
identifier has a doc comment. Errors are lowercase, wrapped once per layer with
`%w`, and never contain a value or the age key file's contents.

### `internal/writer`

```go
// Package writer owns the output dir: file names, modes, owners, atomic
// writes, removal, and the tmpfs check.
package writer

const ReservedPrefix = ".sops-sidecar" // names starting with this belong to the sidecar (ready marker, temp files)
const DefaultMode Mode = 0o400         // mode when neither the map file entry nor its defaults set one
const KeepID = -1                      // UID/GID value meaning "leave unchanged" (the sidecar's own id); the built-in default
const MaxID = 1<<32 - 2                // largest uid/gid accepted (32-bit ids; 2^32-1 means "unchanged" to chown)
const ProcMounts = "/proc/mounts"      // mount table consulted by CheckTmpfs in production

type Name struct{ /* unexported */ }   // a validated single-element file name; zero value is invalid
func ParseName(s string) (Name, error)          // rejects "", ".", "..", any '/' or '\\', NUL, len > 255, and ReservedPrefix-prefixed names
func ParseReservedName(s string) (Name, error)  // same rules, but s must start with ReservedPrefix
func (n Name) String() string

type Mode uint32                                // permission bits only (≤ 0o777)
func ParseMode(s string) (Mode, error)          // octal text: "0400" or "400" (3 digits, optional leading 0); rejects others and bits above 0o777
func (m Mode) String() string                   // "0400"

type Owner struct{ UID, GID int }               // KeepID leaves that id unchanged
func KeepOwner() Owner                          // Owner{KeepID, KeepID}: the built-in default owner

type File struct {
    Name  Name
    Data  []byte // written byte-exact
    Mode  Mode
    Owner Owner
}

func Stale(prev, next []Name) []Name            // names in prev absent from next, in prev's order
func MountType(mounts []byte, dir string) (string, error)
    // fs type of the mount whose mount point is the longest path-prefix of dir
    // (dir absolute and clean; mount points decoded from /proc/mounts octal
    // escapes \040 \011 \012 \134); error if no line matches or a line is malformed

type Dir struct{ /* *os.Root + path */ }
func Open(path string) (*Dir, error)            // path must be absolute; refuses a symlink or non-directory; opens an os.Root
func (d *Dir) Close() error
func (d *Dir) CheckTmpfs(mountsFile string) error // reads mountsFile, errors unless MountType(...) == "tmpfs"
func (d *Dir) Write(f File) error
    // atomic, all via os.Root: create ReservedPrefix+"-tmp-<random hex>" with
    // O_CREATE|O_EXCL|O_WRONLY 0600 → write Data → Sync → Chmod(Mode) → Chown(Owner)
    // → Close → refuse if the target exists and is not a regular file (symlink,
    // dir, …) → Rename over target. On any error the temp file is removed and the
    // previous target (if any) is untouched. Rejects a zero Name.
func (d *Dir) Remove(n Name) error              // nil if already absent; refuses to remove a non-regular file
func (d *Dir) RemoveTempFiles() error           // deletes ReservedPrefix+"-tmp-*" files a killed run left behind
func (d *Dir) Names() ([]Name, error)           // regular files with valid secret names (adopted at startup)
```

### `internal/decrypt`

```go
// Package decrypt decrypts a SOPS file in-process with an age identity and
// returns its top-level scalar secrets.
package decrypt

type Format int
const (
    FormatYAML Format = iota
    FormatJSON
)
func FormatForPath(path string) Format          // ".json" (case-insensitive) → FormatJSON, otherwise FormatYAML

type Identities struct{ /* []age.Identity */ }
func ParseIdentities(keyFile []byte) (Identities, error)
    // parses an age identity file (comments and blank lines allowed); error if
    // none found. Errors never include file contents or age's parser messages.

type Value []byte
// Value redacts itself everywhere it can be formatted:
func (v Value) String() string                  // "[REDACTED]"
func (v Value) GoString() string                // "[REDACTED]"
func (v Value) Format(f fmt.State, verb rune)   // writes "[REDACTED]" for every verb
func (v Value) LogValue() slog.Value            // "[REDACTED]"
func (v Value) MarshalText() ([]byte, error)    // "[REDACTED]" (JSON/text encoders would otherwise base64 the bytes)
func (i Identities) Format(f fmt.State, verb rune) // "[REDACTED]" for every verb

type Secret struct {
    Key   string
    Value Value
}

func DisableSOPSLogs()                          // discards sops' own logrus output, which can echo file content
func Decrypt(data []byte, f Format, ids Identities) ([]Secret, error)
    // 1. load the tree with sops' yaml/json store (LoadEncryptedFile)
    // 2. require exactly one key group; for each *age.MasterKey in it apply ids
    //    (age.ParsedIdentities.ApplyToMasterKey) and call key.Decrypt() directly;
    //    first success is the data key. Never call GetDataKey*/keyservice (they
    //    drop injected identities and fall back to env/home lookup). Non-age keys
    //    are ignored, so no KMS/network is ever contacted.
    // 3. tree.Decrypt(dataKey, aes.NewCipher()); verify the MAC exactly as sops'
    //    decrypt.DataWithFormat does; never skip.
    // 4. walk tree.Branches: exactly one document; skip sops.Comment items;
    //    keys must be strings; values must be string/int/float64/bool
    //    (converted with sops.ToBytes); nil, branches, and lists are refused
    //    with an error naming the key. Result sorted by Key.
```

### `internal/config`

```go
// Package config parses the environment and the map file into validated
// settings and computes the plan of files to write.
package config

const (
    EnvSOPSFile      = "SOPS_FILE"
    EnvAgeKeyFile    = "SOPS_AGE_KEY_FILE"
    EnvAgeKey        = "SOPS_AGE_KEY"       // refused if present (even empty)
    EnvMapFile       = "SECRETS_MAP"
    EnvOutputDir     = "OUTPUT_DIR"
    EnvAllowNonTmpfs = "ALLOW_NON_TMPFS"
    EnvWipeOnExit    = "WIPE_ON_EXIT"
    EnvLogLevel      = "LOG_LEVEL"

    DefaultSOPSFile   = "/config/secrets.sops.yaml"
    DefaultAgeKeyFile = "/config/age.key"
    DefaultMapFile    = "/config/secrets.map.yaml"
    DefaultOutputDir  = "/secrets"
)

type Config struct {
    SOPSFile      string
    AgeKeyFile    string
    MapFile       string
    MapRequired   bool       // true when EnvMapFile is set: a missing map file is then an error
    OutputDir     string
    AllowNonTmpfs bool
    WipeOnExit    bool
    LogLevel      slog.Level // default slog.LevelInfo
}

func FromEnv(lookup func(string) (string, bool)) (Config, error)
    // unset or empty → default. Booleans via strconv.ParseBool; LOG_LEVEL via
    // a case-insensitive match of debug|info|warn|error (level offsets such as `info+1` are refused). Path env vars must be
    // absolute. SOPS_AGE_KEY present → error explaining that env vars are visible
    // in `docker inspect` and to mount the key as a file instead.

// Map is a parsed map file. The zero Map means "no map file": every key is
// written under its own name with the built-in defaults.
type Map struct{ /* unexported */ }
func ParseMap(data []byte) (Map, error)
    // strict YAML (go.yaml.in/yaml/v3, KnownFields): `defaults: {uid, gid, mode}`
    // (all optional) and `files: [{key, path, uid, gid, mode}]` (key required,
    // path defaults to key, others inherit defaults, then writer built-ins).
    // Errors: empty files list, empty/duplicate key, invalid/duplicate path
    // (via writer.ParseName), invalid mode (writer.ParseMode), uid/gid outside 0..writer.MaxID,
    // multiple YAML documents.

type Entry struct {
    Key  string
    File writer.File
}
type Plan struct {
    Entries []Entry // sorted by file name
    Ignored int     // keys in the SOPS file not listed in the map file
}
func (m Map) Plan(secrets []Secret) (Plan, error)   // secrets are []decrypt.Secret
    // zero Map: every secret, name = writer.ParseName(key) (error naming the key
    // if invalid). Parsed Map: only listed keys; a listed key missing from
    // secrets is an error naming it; unlisted keys counted in Ignored.
```

### `internal/health`

```go
// Package health owns readiness: the ready marker in the output dir.
package health

const MarkerName = writer.ReservedPrefix + "-ready"   // ".sops-sidecar-ready"
const MarkerMode writer.Mode = 0o444

type Marker struct{ /* *writer.Dir, writer.Name */ }
func NewMarker(d *writer.Dir) (Marker, error)
func (m Marker) Set() error    // writes an empty MarkerMode file atomically, owner unchanged
func (m Marker) Clear() error  // removes it; nil if absent
func Check(outputDir string) error // nil only if outputDir/MarkerName is a regular file (Lstat; symlink → error)
```

The ready marker is the only readiness state. It is cleared before every load
and set only after every file of that load is written and stale files are
removed. `health` runs as a separate process, so there is no in-memory status.

### `internal/sopstest` (test support)

```go
// Package sopstest builds SOPS-encrypted fixtures for tests.
package sopstest

func NewIdentity(t testing.TB) (keyFile []byte, recipient string)       // fresh X25519 identity file text
func EncryptYAML(t testing.TB, recipient string, b sops.TreeBranch) []byte // single age key group, MAC set, like `sops -e`
func EncryptJSON(t testing.TB, recipient string, b sops.TreeBranch) []byte
```

Imported only from `_test.go` files, so it never reaches the binary.
It is excluded from coverage gates (it has no behavior of its own to test).

### `cmd/sops-sidecar`

```go
const (
    exitOK      = 0 // success; health: ready
    exitFailure = 1 // any config, input, decrypt, write, or readiness failure
    exitUsage   = 2 // unknown subcommand or unexpected arguments
)
const maxInputBytes = 1 << 20 // per input file; larger files are refused

func main() // os.Exit(cli(os.Args[1:], os.LookupEnv, os.Stdout, os.Stderr)) and nothing else
func cli(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) int
```

Subcommands (`flag` + `switch`, no framework):

| Command | Behavior |
|---|---|
| `run` (default when no args) | Config → open output dir → tmpfs check (skipped with a `WARN` when `ALLOW_NON_TMPFS=true`) → load. Initial load failure → exit 1. Then block. `SIGHUP` → reload (failure: logged, marker stays cleared, previous files stay, keep running). `SIGTERM`/`SIGINT` → exit 0; files kept unless `WIPE_ON_EXIT=true` (then remove the last load's files and the marker). |
| `health` | `config.FromEnv` → `health.Check(OutputDir)` → exit 0 or 1. Prints nothing on success. |
| `check` | Read inputs → decrypt → plan; print each planned entry's **key** on its own line to stdout (sorted); write nothing; exit 1 on any problem. |
| `version` | Print `sops-sidecar <version>` where version comes from `runtime/debug.ReadBuildInfo().Main.Version`. |

Signals: `signal.NotifyContext(ctx, SIGTERM, SIGINT)` for shutdown and a
separate `signal.Notify(ch, SIGHUP)` channel, both registered explicitly (the
binary is PID 1 in the container). The lifecycle loop takes the reload channel
as a parameter so tests can drive it without real signals.

Reading inputs: each file read with a `maxInputBytes` limit. Map file: absent
at the default path → zero `Map`; absent when `MapRequired` → error.

## Errors

- No sentinel or typed errors are exported: no caller branches on them.
  Standard-library sentinels (`fs.ErrNotExist`) are checked with `errors.Is`.
- Error text names keys, file names, and paths. It never contains a value, the
  age key file's contents, or text from parsers that may echo input
  (age identity parsing, sops store, and sops tree-decrypt errors are replaced
  with fixed messages, not wrapped). Map file YAML errors are wrapped: the map
  file holds no secrets, and its field/line messages are needed to fix it.
- `cmd` prints a failing command's error once to stderr as a log line.

## Logging

`log/slog` `TextHandler` on stderr, level from `LOG_LEVEL` (default `info`).
Never logs a value. Key names and file names appear only at `debug`.

| Level | Message | Attributes |
|---|---|---|
| INFO | `secrets written` | `files` (count), `ignored` (count) |
| INFO | `reload requested` | — |
| INFO | `shutting down` | — |
| WARN | `tmpfs check disabled` | `output_dir`, `reason` (`ALLOW_NON_TMPFS` is for tests only) |
| ERROR | `config failed` / `startup failed` / `load failed` / `reload failed` / `check failed` / `not ready` / `wipe failed` | `error` |
| DEBUG | `secret written` | `key`, `file`, `mode`, `uid`, `gid` |
| DEBUG | `secret removed` | `file` |

sops' own logrus loggers are discarded at startup (`decrypt.DisableSOPSLogs`): they write outside `LOG_LEVEL` and can echo file content, such as an unencrypted comment.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | Success (`health`: ready) |
| 1 | Failure (config, inputs, decrypt, write, not ready) |
| 2 | Usage error |

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `SOPS_FILE` | `/config/secrets.sops.yaml` | SOPS file (YAML, or JSON if it ends in `.json`) |
| `SOPS_AGE_KEY_FILE` | `/config/age.key` | age key file |
| `SECRETS_MAP` | `/config/secrets.map.yaml` (optional) | map file; if set explicitly it must exist |
| `OUTPUT_DIR` | `/secrets` | output dir; must be tmpfs |
| `ALLOW_NON_TMPFS` | `false` | skip the tmpfs check (tests only; logs a warning) |
| `WIPE_ON_EXIT` | `false` | remove written files and the ready marker on `SIGTERM`/`SIGINT` |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `SOPS_AGE_KEY` | — | **refused**: env vars leak through `docker inspect` |

Built-in file defaults (when the map file sets neither entry values nor
`defaults`): mode `0400`, owner unchanged (the sidecar's uid/gid).

## Testing

- Table-driven tests through each package's exported API; `t.TempDir`,
  `t.Setenv`, `t.Helper`; hand-written fakes only; `go-cmp` allowed.
- Fuzz: `config.ParseMap` and `writer.ParseName` with committed seed corpora
  under `testdata/fuzz/`.
- No-secret-in-logs: each package asserts a canary value never appears in its
  errors; `cmd` runs every subcommand and failure path with a canary value,
  both in-process and as a re-executed subprocess (so sops' own stderr output
  is captured too), and asserts the canary never appears in stdout, stderr, or
  errors.
- Coverage: `scripts/coverage.sh` — ≥ 90 % per `internal/` package and ≥ 80 %
  overall, from `go test -race -covermode=atomic -coverprofile`.
