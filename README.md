# Managed Agent

[中文说明](./README.zh-CN.md)

Managed Agent is a Go-based AI agent service that combines LLM orchestration with an AIO sandbox runtime. It provides persistent sessions, tool execution, Skills-based prompt extensions, file and image handling, and SSE streaming for interactive agent workflows.

## Why This Project

This repository is a compact implementation of the architecture described in Anthropic's [Managed Agents](https://www.anthropic.com/engineering/managed-agents): separate the agent's reasoning layer from the tool execution layer, connect them through durable event logs, and make the system easier to recover, inspect, and extend.

In this project:

- The **brain** is the Go service that manages prompts, sessions, tool loops, and provider integrations
- The **hands** are provided by an **AIO sandbox** that executes commands, browser actions, and file operations
- The **glue** is a persistent session event log under `data/sessions/`

## Highlights

- Persistent multi-turn sessions stored on disk
- Streaming agent responses and tool events over SSE
- Support for Claude, OpenAI-compatible APIs, and Gemini
- Native image content support across providers
- File upload and sandbox download flow
- Skills loaded from the versioned `skills/` directory
- Explicit sandbox integration tests with opt-in execution
- Simple deployment model with a built-in web UI

## Architecture

```text
┌─────────────┐     HTTP/SSE      ┌──────────────────────────────────┐
│  Browser /  │ ◄────────────────► │           managed-agent          │
│   Client    │                   │                                  │
└─────────────┘                   │  ┌──────────┐  ┌─────────────┐  │
                                  │  │  Session │  │   Skills    │  │
                                  │  │  Store   │  │  Registry   │  │
                                  │  └──────────┘  └─────────────┘  │
                                  │         │             │          │
                                  │         ▼             ▼          │
                                  │      ┌─────────────────────┐    │
                                  │      │     Agent Harness   │    │
                                  │      └──────────┬──────────┘    │
                                  └─────────────────┼───────────────┘
                                                    │
                          ┌─────────────────────────┼──────────────┐
                          │                         │              │
                          ▼                         ▼              ▼
                    ┌──────────┐           ┌──────────────┐  ┌──────────┐
                    │   LLM    │           │ AIO Sandbox  │  │   File   │
                    │ Provider │           │ Tool Runtime │  │  Store   │
                    └──────────┘           └──────────────┘  └──────────┘
```

Core request flow:

1. A client sends a message.
2. Agent Harness rebuilds session history and active Skills.
3. The service calls the configured LLM provider.
4. The model returns text or tool calls.
5. Tool calls execute inside the AIO sandbox.
6. Results are persisted as events and streamed back to the client.

## Feature Overview

### LLM Orchestration

- Provider abstraction for `claude`, `openai`, and `gemini`
- Tool-use loop with round limits
- Skill-aware system prompt construction
- Debug mode for full request and response logging

### Sandbox Runtime

- Command execution
- Browser navigation and interaction
- File read, write, upload, and download
- Code execution helpers

### Skills System

- Skills are loaded from `skills/<name>/SKILL.md`
- User activation via `/skill-name`
- Optional model-triggered activation
- Bundled `scripts/`, `references/`, and `assets/` are deployed into the sandbox when activated

### File And Image Support

- Attachments are uploaded into the sandbox
- Image attachments are fetched and converted into provider-native image blocks
- Non-image files are passed as sandbox paths

## Quick Start

### Prerequisites

- Go 1.23+
- An accessible LLM API endpoint
- An **AIO sandbox** instance compatible with the sandbox SDK used by this project

### 1. Prepare Your Sandbox

Provision an AIO sandbox instance and collect:

- `sandbox_base_url`, for example `https://your-sandbox.example.com`
- `sandbox_id`, for example `your-sandbox-id`

If your sandbox requires a custom image, bootstrap process, or extra tooling, prepare that on your side. This repository does not include vendor-specific internal setup instructions.

### 2. Create `config.yaml`

```bash
cp config.example.yaml config.yaml
```

Then edit `config.yaml`:

```yaml
llm_api_key: sk-your-api-key-here
llm_base_url: https://api.openai.com/v1
sandbox_base_url: https://your-sandbox.example.com
sandbox_id: your-sandbox-id
```

### 3. Start The Service

```bash
go build -o managed-agent && ./managed-agent
```

Open `http://localhost:8080` in your browser.

## Development

### Run Tests

```bash
# Unit and package-level tests
go test ./...

# Real sandbox integration tests
RUN_SANDBOX_TESTS=1 go test -run 'TestSandbox(Tools|Basic)' -v ./...
```

### Make Targets

```bash
make build
make test
make package
```

## Configuration

Configuration priority is:

1. Environment variables
2. `config.yaml`
3. Built-in defaults

Important fields:

| Field | Description | Default |
|------|------|--------|
| `llm_provider` | LLM provider: `claude`, `openai`, `gemini` | `claude` |
| `llm_base_url` | LLM API base URL | `https://api.openai.com/v1` |
| `llm_api_key` | LLM API key | — |
| `llm_model` | Model name | `Claude Sonnet 4.6` |
| `llm_max_tokens` | Max output tokens | `8192` |
| `llm_custom_header` | Extra HTTP headers as a JSON object string | — |
| `sandbox_base_url` | AIO sandbox base URL | `https://your-sandbox.example.com` |
| `sandbox_id` | Sandbox instance ID | `your-sandbox-id` |
| `listen_addr` | HTTP listen address | `:8080` |
| `data_dir` | Session storage directory | `data/sessions` |
| `skills_dir` | Skills directory | `skills` |

## API

| Method | Path | Description |
|------|------|------|
| `POST` | `/api/sessions` | Create a new session |
| `GET` | `/api/sessions` | List sessions |
| `GET` | `/api/sessions/{id}` | Get session details |
| `POST` | `/api/sessions/{id}/messages` | Send a message with SSE streaming |
| `POST` | `/api/sessions/{id}/upload` | Upload files or images into the sandbox |
| `GET` | `/api/files/download?path=` | Download a file from the sandbox |
| `GET` | `/api/files/content?path=` | Read sandbox file content for previews |

## Skills Layout

```text
skills/
└── my-skill/
    ├── SKILL.md
    ├── scripts/
    ├── references/
    └── assets/
```

Minimal `SKILL.md` example:

```markdown
---
name: my-skill
description: Example Skill description
user_invocable: true
---

# My Skill

Write your prompt here...
```

## Acknowledgements

- Anthropic for the original managed-agent architecture write-up
- The AIO sandbox work from [agent-infra/sandbox](https://github.com/agent-infra/sandbox)

## License

This project is released under the [MIT License](./LICENSE).
