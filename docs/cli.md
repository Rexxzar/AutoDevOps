# AutoDevOps CLI (`autodev`)

The `autodev` CLI talks to the same control plane as the dashboard — anything you
can do in the UI you can script here. Every command supports `-o json` for
machine-readable output.

## Install / build

```bash
go build -o autodev ./cmd/cli
# optional: inject build metadata
go build -ldflags "-X main.version=1.0.0 -X main.commit=$(git rev-parse --short HEAD) -X main.date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o autodev ./cmd/cli
```

Config is stored at `~/.autodev/config.json` (written by `login` / `cloud use`).

## Global flags

Available on every command. Config is the default; flags override it per-invocation.

| Flag | Description |
|---|---|
| `--server <url>` | Control-plane URL (overrides config) |
| `--token <jwt>` | Auth token (overrides config) |
| `-o, --output table\|json` | Output format (default `table`) |
| `--no-color` | Disable colored output |
| `-h, --help` | Help for any command |
| `-v, --version` | Print the CLI version |

```bash
autodev get deployments -o json --server https://autodevops.example.com
```

---

## Auth

| Command | What it does | Example |
|---|---|---|
| `login` | Authenticate (email + password) and save a token | `autodev login --email admin@autodevops.local` |
| `logout` | Remove saved credentials | `autodev logout` |
| `whoami` | Show the current login (server + user) | `autodev whoami` |

Login accepts the password interactively (hidden), via `--password`, or piped:

```bash
echo "$PASS" | autodev login --email admin@autodevops.local --password-stdin
autodev login --server https://autodevops.example.com --email admin@autodevops.local
```

---

## Overview & system

| Command | What it does | Example |
|---|---|---|
| `overview` | At-a-glance summary (deployments, clusters, cloud, compliance) | `autodev overview` |
| `info` | CLI + control-plane info, health/latency, subsystems | `autodev info` |
| `version` | Print version / commit / build info | `autodev version` |

```bash
autodev overview            # dashboard in the terminal
autodev overview -o json    # structured summary for monitoring
autodev info                # is the server reachable? what's running?
```

---

## Resources (provisioned cloud & local)

`autodev resources` (alias `res`). Most subcommands accept a **name or id**.

| Command | What it does | Example |
|---|---|---|
| `resources list` | List provisioned resources | `autodev resources list --provider aws` |
| `resources get <name>` | Details + terraform outputs | `autodev resources get my-db` |
| `resources create` | Create from flags or `-f spec.json`, then plan | *(see below)* |
| `resources apply <name>` | `terraform apply` | `autodev resources apply my-db` |
| `resources destroy <name>` | `terraform destroy` (confirms; `-y` to skip) | `autodev resources destroy my-db -y` |
| `resources dismiss <name>` | Hide a destroyed resource from listings | `autodev resources dismiss my-db` |
| `resources tf <name>` | Show generated Terraform (`--out` to a file) | `autodev resources tf my-db --out main.tf` |
| `resources exec <name> -- <cmd>` | One-shot command on EC2/VM/ECS/ACI | `autodev resources exec my-vm -- uname -a` |

**Filters on `list`:** `--provider aws|azure|local`, `--type ec2|rds|ecs|aci|vm|db|docker`, `--status`.

**Create** — flags for simple cases, `-f` for full control:

```bash
# pick a default cloud account once (see Cloud below), then:
autodev resources create --provider aws --type ec2 --name my-ec2 --region eu-west-2

# repeatable --opt key=value for resource options:
autodev resources create --provider local --type docker --name web --opt image=nginx --opt port=80

# declarative spec file:
autodev resources create -f resource.json
```

`resource.json`:
```json
{
  "name": "my-db",
  "provider": "aws",
  "resource_type": "rds",
  "region": "eu-west-2",
  "cloud_account_id": "aws-prod-eu-west-1",
  "resource_opts": { "engine": "16" }
}
```

---

## Cloud inventory & accounts

| Command | What it does | Example |
|---|---|---|
| `inventory` | Live polled cloud resources (AWS + Azure) | `autodev inventory --provider aws` |
| `cloud accounts list` | List cloud accounts (shows IDs, marks default) | `autodev cloud accounts list` |
| `cloud accounts add -f <file>` | Register an account from a JSON spec | `autodev cloud accounts add -f aws.json` |
| `cloud accounts remove <name>` | Remove an account (`-y` to skip confirm) | `autodev cloud accounts remove old-acct` |
| `cloud use <name>` | Set the default account for `resources create` | `autodev cloud use aws-prod-eu-west-1` |
| `cloud current` | Show the default account | `autodev cloud current` |

`aws.json` for `cloud accounts add`:
```json
{
  "provider": "aws",
  "name": "prod",
  "credentials": { "access_key_id": "...", "secret_access_key": "...", "region": "eu-west-2" }
}
```

---

## Local inventory (Docker containers & K8s pods)

| Command | What it does | Example |
|---|---|---|
| `local list` | List local containers and pods | `autodev local list` |
| `local logs <name>` | Recent logs (`--tail N`) | `autodev local logs shophup-front --tail 100` |
| `local restart <name>` | Restart a container or pod | `autodev local restart shophup-front` |

---

## Deployments (existing — to be expanded)

| Command | What it does | Example |
|---|---|---|
| `deploy` | Register a Git repo for deployment | `autodev deploy --name my-app --repo <url> --target k8s` |
| `get <resource>` | List deployments/clusters/containers/resources/compliance | `autodev get deployments` |
| `status <name>` | Detailed deployment status | `autodev status my-app` |
| `logs <name>` | Stream live deploy events | `autodev logs my-app` |
| `destroy <name>` | Destroy a deployment | `autodev destroy my-app` |

---

## Shell completion

Cobra-generated, for bash/zsh/fish/powershell:

```bash
autodev completion bash > /etc/bash_completion.d/autodev
autodev completion zsh  > "${fpath[1]}/_autodev"
```
