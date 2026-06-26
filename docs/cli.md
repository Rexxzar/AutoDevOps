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

## Deployments

`autodev deployments` (alias `deploy`, `deps`). Cloud targets (`vm`/`ecs`/`aci`)
deploy onto a provisioned + applied resource (`--resource <name>`); the account
comes from the resource. ACI/ECS need a registry linked to that resource.

| Command | What it does | Example |
|---|---|---|
| `deployments list` | List deployments | `autodev deployments list` |
| `deployments get <name>` | Apps, status, domains, links | `autodev deployments get shophub` |
| `deployments create` | Create a deployment (flags or `-f`) | *(see below)* |
| `deployments trigger <name> [app]` | Redeploy (rebuild + redeploy) | `autodev deployments trigger shophub` |
| `deployments destroy <name>` | Destroy a deployment (`-y`) | `autodev deployments destroy shophub -y` |
| `deployments logs <name>` | Stream live deploy + health events | `autodev deployments logs shophub` |
| `deployments apps list <name>` | List apps in a deployment | `autodev deployments apps list shophub` |
| `deployments apps add <name> ...` | Add an app to a deployment | `autodev deployments apps add shophub --name worker --target container --repo <url>` |
| `deployments apps remove <name> <app>` | Remove an app | `autodev deployments apps remove shophub worker` |
| `deployments link <name> <app> <target>` | Connect apps (`AUTODEVOPS_<ALIAS>_*`) | `autodev deployments link shophub backend db --alias DB --port 5432` |
| `deployments links <name>` | List app links | `autodev deployments links shophub` |
| `deployments unlink <name> <app> <alias>` | Remove a link | `autodev deployments unlink shophub backend DB` |
| `deployments import` | Import a docker-compose repo as a multi-app stack | `autodev deployments import --repo <url> --target container` |

**Create:**
```bash
# local k8s, from a git repo (single app — auto Dockerfile/Paketo)
autodev deployments create --name shophub --target k8s --repo https://github.com/you/app --branch main

# local docker, image-kind app
autodev deployments create --name cache --target container --kind image --image redis:7 --port 6379

# cloud: onto a provisioned + applied resource
autodev deployments create --name api --target ecs --resource my-ecs --repo https://github.com/you/api

# full control via a spec file (matches the create request body)
autodev deployments create -f deployment.json
```

Create/add flags: `--name --target k8s|container|vm|ecs|aci --resource --kind git|image
--repo --branch --git-token --build-context --framework --image --port --env k=v --volume --domain`, or `-f`.

---

## Clusters & pods

| Command | What it does | Example |
|---|---|---|
| `clusters list` | Registered K8s clusters (status, pod count) | `autodev clusters list` |
| `clusters pods` | Pod health + restart counts | `autodev clusters pods --namespace default --status unhealthy` |
| `clusters register` | Register a cluster | `autodev clusters register --name kind --endpoint https://127.0.0.1:6443` |

`pods` filters: `--namespace`, `--cluster <name>`, `--status`.

## Self-healing

`autodev heal` — observability for the self-healing controller (K8s pods **and**
Docker containers).

| Command | What it does | Example |
|---|---|---|
| `heal metrics` | Recovery rate, avg **MTTD**, avg **MTTR** (overall + per kind) | `autodev heal metrics` |
| `heal episodes` | Recent heal cycles (detected → recovered, MTTR) | `autodev heal episodes --limit 20` |
| `heal stream` | Live tail of heal events (Ctrl+C) | `autodev heal stream` |

## System logs

| Command | What it does | Example |
|---|---|---|
| `logs` | View the control-plane system log | `autodev logs --level ERROR` |
| `logs -f` | Tail the log live (polls for new entries) | `autodev logs --source docker -f` |

Filters: `--level`, `--source`, `-q/--search`, `--since 15m`, `--limit`.

## Compliance

| Command | What it does | Example |
|---|---|---|
| `compliance findings` | List findings (`--severity`, `--scanner`) | `autodev compliance findings --severity critical` |
| `compliance summary` | Counts by severity + scanner (+ actionable total) | `autodev compliance summary` |

## Secrets

`autodev secrets` — the AES-256-GCM vault.

| Command | What it does | Example |
|---|---|---|
| `secrets list` | List secrets (metadata only; `--category`) | `autodev secrets list --category user` |
| `secrets get <name>` | Reveal a secret's value | `autodev secrets get my-api-key` |
| `secrets set <name> [value]` | Create/update a user secret (value via arg or stdin) | `echo -n s3cr3t \| autodev secrets set my-api-key` |
| `secrets delete <name>` | Delete a secret | `autodev secrets delete my-api-key` |
| `secrets status` | Vault key version, rotations, secret count | `autodev secrets status` |
| `secrets rotate` | Rotate the data key (re-encrypts all; values unchanged) | `autodev secrets rotate` |
| `secrets rotate-master --new-key <k>` | Re-wrap under a new MASTER_KEY (update env before restart!) | `autodev secrets rotate-master --new-key …` |

## Notifications

| Command | What it does | Example |
|---|---|---|
| `notifications list` | Recent notifications (`--unread`) | `autodev notifications list --unread` |
| `notifications read` | Mark read (`--all` or specific ids) | `autodev notifications read --all` |

## Git repo store

| Command | What it does | Example |
|---|---|---|
| `repos list` | Stored repositories (findings, last scan) | `autodev repos list` |
| `repos get <name>` | Repo details + findings | `autodev repos get my-app` |
| `repos add` | Register a repo (`--repo --branch --git-token`) | `autodev repos add --repo https://github.com/you/app` |
| `repos scan <name>` | Run a compliance scan | `autodev repos scan my-app` |
| `repos refresh <name>` | Pull latest commit | `autodev repos refresh my-app` |
| `repos remove <name>` | Remove from the store | `autodev repos remove my-app` |

## Terraform store

| Command | What it does | Example |
|---|---|---|
| `terraform workspaces` | List workspaces (config/state/runs per resource) | `autodev terraform workspaces` |
| `terraform runs` | Recent plan/apply/destroy runs | `autodev terraform runs` |

(View a resource's generated `.tf` with `autodev resources tf <name>`.)

## Image registry

| Command | What it does | Example |
|---|---|---|
| `images list` | List images in the local registry | `autodev images list` |
| `images import --source <ref>` | Pull an external image into the registry | `autodev images import --source postgres:16` |
| `images delete <repo> <tag>` | Delete an image | `autodev images delete app 27e4ecf5` |

## Shell completion

Cobra-generated, for bash/zsh/fish/powershell:

```bash
autodev completion bash > /etc/bash_completion.d/autodev
autodev completion zsh  > "${fpath[1]}/_autodev"
```
