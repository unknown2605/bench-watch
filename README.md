# Benchwatch

A local dashboard for the coding agents you run and the machine they run on.

Benchwatch watches your workbench. It reads the session logs that Claude Code, Codex, OpenCode, Gemini CLI,
Qwen Code and Copilot CLI already write on your computer. It shows tokens, cost, context use, compactions,
sub-agents and live sessions side by side, with a per-agent filter, next to CPU, memory, disk and Docker figures.
Everything stays on your machine: it listens on 127.0.0.1 only, opens the logs read-only and stores counts,
labels and hashes, never prompt, output or file text. It runs on macOS; Linux is planned.

## Features

- One page for six coding agents, with a filter to look at one agent at a time.
- Tabs: Overview, Tokens, Context, Sessions, Sub-agents, Fills, Machine and Docker.
- Token and cost totals over a chosen range, from today to all time.
- Context use, compactions and sub-agent activity per session.
- Fills: which tool results take up the context window, by tool and by Bash command.
- The Context, Sub-agents, Fills and Guard tabs cover Claude Code only; the other tabs cover every agent.
- Live machine figures: CPU, memory, disk and network.
- A Docker tab when a Docker socket is present, and a Guard tab when a claude-guard install or log exists.
- Readers for Gemini CLI, Qwen Code, Copilot CLI and OpenCode are built from upstream sources and are labelled
  "format unverified" until checked against real records. Claude Code and Codex are verified.

## Privacy

- The agent listens on 127.0.0.1 only.
- Agent logs are opened read-only and are never changed.
- Only numbers, short labels and hashes are stored.
- Prompt, output and file text are never stored and never leave the log files.

## Requirements

- macOS.
- Go 1.27 or newer.
- Node 26 or newer, to build the web UI.
- A PostgreSQL server on 127.0.0.1:5432 that you can log in to as an administrator.

## Installation on macOS

1. Build the web UI. In `web/`, run:

   ```sh
   npm ci --ignore-scripts
   npm run build
   ```

   The result is `web/dist`.

2. Add these pg_hba lines. They confine the role to its own database. Put them above every other `host` line the
   role could match, then reload PostgreSQL's configuration (`bw-db-role` refuses to create a role without them):

   ```
   host  sameuser  /^benchwatch(_[a-z0-9_]+)?$  all  scram-sha-256
   host  all       /^benchwatch(_[a-z0-9_]+)?$  all  reject
   ```

3. Install the agent:

   ```sh
   BENCHWATCH_CONTAINER_PATHS=/path/one:/path/two bash bin/install-server.sh
   ```

   List the host folders and files your containers bind-mount writable; the install refuses to run code from
   inside them. Use `BENCHWATCH_CONTAINER_PATHS=none` if no container can write to the host.

   The script builds the agent, creates the database role and database, applies the migrations, installs a launchd
   job and checks that the agent answers on `127.0.0.1:8790`.

4. Serve `web/dist` from any local web server that forwards `/api/` to `http://127.0.0.1:8790`, then open that
   address in your browser.

To remove the launchd job:

```sh
bash bin/uninstall-server.sh
```

## Configuration

The agent address is fixed at `127.0.0.1:8790`. These variables are read by the scripts:

| Variable                  | Used by             | Meaning                                                   |
| ------------------------- | ------------------- | --------------------------------------------------------- |
| `BENCHWATCH_INSTALL_WAIT` | `install-server.sh` | Seconds to wait for the agent to answer (default 120).    |
| `BENCHWATCH_NODE_BIN`     | `bin/generate.sh`   | Folder holding the Node 26 binary, when it is not on PATH. |
| `BENCHWATCH_CONTAINER_PATHS` | `install-server.sh` | Required. Colon-separated host paths containers can write; the install refuses a source, tool or temp folder inside any. `none` skips the check. |
| `BENCHWATCH_PG_CONTAINER` | `bin/test-db.sh`    | Postgres container name; without it the container-log checks are skipped. |
| `BENCHWATCH_WEB_DIR`      | `publish-web.mjs`, `scan-dump` | The published web folder (`next/` sits under it); both refuse to run without it. |
| `DOCKER_HOST`             | the agent           | A `unix://` socket address for the Docker tab.            |

## Development

Go tests, from `server/`:

```sh
go test ./...
```

Web UI, from `web/`:

```sh
npm run typecheck
npm run lint
npm test
npm run build
```

Run `npm run lint:install` once first to install the linter. From the repository root:

```sh
npm test
bash bin/test-db.sh
```

`npm test` runs the script tests in `test/`. `bin/test-db.sh` tests the migrations and `bw-db-role` against a
PostgreSQL server and drops only the database and role it creates.

## Project layout

- `server/`: the Go agent, collectors, readers for each coding agent, and database migrations.
- `web/`: the React web UI.
- `api/`: the OpenAPI contract shared by the agent and the web UI.
- `bin/`: install, uninstall, publish, generate and test scripts.
- `test/`: tests for the scripts in `bin/`.
- `design/`: design tokens.
- `docs/`: further notes.

## Platform support

macOS is supported. Linux is not yet: the machine figures (CPU, memory, disk, network) are read from macOS, so the
agent does not run on Linux today. Linux support is planned.

## License

MIT. See `LICENSE`.
