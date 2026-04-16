# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 常用命令

```bash
# 构建
go build -o bin/managed-agent-mvp

# 运行（需要配置 config.yaml）
./bin/managed-agent-mvp
./bin/managed-agent-mvp -config /path/to/config.yaml

# 测试（全包，含竞态检测）
go test -race -timeout=300s -v -cover ./...

# Makefile 工作流
make build    # prepare + compile
make test     # prepare + 带覆盖率的测试
make all      # prepare + compile + test + package
```

## 配置

配置优先级：**环境变量 > config.yaml > 默认值**

`config.yaml` 必填字段（或对应的环境变量 `LLM_API_KEY`、`SANDBOX_BASE_URL`、`SANDBOX_ID`）：

```yaml
llm_api_key: sk-...
llm_base_url: https://...
sandbox_base_url: https://8080-xxx.agent-sandbox.baidu-int.com
sandbox_id: xxxxxxxx
```

**注意**：`config.yaml` 包含密钥，已加入 `.gitignore`。首次使用请复制 `config.example.yaml`：
```bash
cp config.example.yaml config.yaml
# 然后编辑 config.yaml 填入真实密钥
```

可选字段：

| 字段 | 说明 | 默认值 |
|------|------|--------|
| `llm_provider` | LLM 提供商：`claude`、`gemini`、`openai` | `claude` |
| `llm_model` | 模型名称 | `Claude Sonnet 4.6` |
| `llm_max_tokens` | 最大输出 token 数 | `8192` |
| `llm_debug` | 设为 `true` 时将完整 LLM 请求/响应写入日志（仅调试用，日志量大） | `false` |
| `llm_custom_header` | 注入 LLM 请求的自定义 HTTP 头（JSON 字符串，用于百度内部 OneAPI 鉴权） | 空 |
| `max_loop_rounds` | 每次请求最大工具调用轮数，超出则返回错误 | `50` |
| `listen_addr` | HTTP 服务监听地址 | `:8080` |
| `data_dir` | 会话数据存储目录 | `data/sessions` |
| `skills_dir` | Skills 目录 | `skills` |

首次运行前需解压内置 Skills：`unzip skills.zip`

## 架构

整个服务是单包 `main` 的 Go 程序，所有 `.go` 文件共享 `package main`。核心设计将 **LLM 推理**（Claude API）与**工具执行**（Sandbox 服务）解耦，通过持久化事件日志串联两者。

**用户消息的处理流程：**
1. `api.go:setupRoutes` 接收 HTTP 请求 → 处理 `/skill-name` 快捷激活，或转交 `RunAgent`
2. `harness.go:RunAgent` 加载会话事件，构建系统 prompt（基础 prompt + skill 摘要 + 激活 skill 的完整 prompt），调用 `LLMClient.CallStream`（由 `config.llm_provider` 决定实际实现：claude/gemini/openai）
3. Claude 返回文本或工具调用；工具调用按类型分发：
   - `skill` 工具 → 在 `harness.go:executeSkillTool` 本地处理（activate/deactivate/list）
   - 其他工具 → 经 `tools.go:ExecuteTool` 转发至 `sandbox_sdk.go:SDKSandboxClient`
4. 工具结果作为事件持久化，并在下一轮循环中回传给 Claude
5. 所有中间事件通过 SSE 实时推送给客户端

**核心文件说明：**
- `harness.go` — Agent 循环（`RunAgent`）、系统 prompt 组装、从事件日志重建消息历史、skill 文件部署到沙箱
- `session.go` — `Session` 与 `SessionStore`：文件存储于 `data/sessions/`，每个会话是一个含有序 `[]Event` 的 `<id>.json`
- `skills.go` — `SkillRegistry` 与 `Skill`：加载 `skills/<name>/SKILL.md`（含 YAML frontmatter），支持 `user_invocable` 和 `disable-model-invocation` 标志，激活时将捆绑文件（`scripts/`、`references/`、`assets/`）部署至沙箱的 `/home/gem/skills/<name>/`
- `claude.go` — `LLMClient` 接口定义、`ClaudeClient` 实现（Anthropic Messages API）、`NewLLMClient` 工厂函数
- `gemini.go` — `GeminiClient` 实现（Google Gemini streamGenerateContent API）
- `openai.go` — `OpenAIClient` 实现（OpenAI 兼容 /v1/chat/completions API）
- `sandbox_sdk.go` — `SDKSandboxClient`：封装 `github.com/agent-infra/sandbox-sdk-go`，提供 `ExecCommand`、`WriteFile`、`ReadFile`、`DownloadFile`
- `tools.go` — `ToolDefinitions`（Claude 工具 schema）与 `ExecuteTool`（分发至 Sandbox SDK）
- `config.go` — `Config` 含手写 YAML 解析器（无外部依赖），环境变量名为字段名大写加下划线

## Skills 系统

每个 skill 是 `skills/` 下的一个目录，包含带 YAML frontmatter 的 `SKILL.md`：

```markdown
---
name: my-skill
description: 何时激活此 skill 的描述
trigger: 可选，注入系统 prompt 的触发场景提示
user_invocable: true          # 默认；允许用户 /skill-name 激活
disable-model-invocation: false  # 默认；允许模型自动激活
---
Prompt 内容写在这里...
```

- Skill 可在 `scripts/`、`references/`、`assets/` 子目录中捆绑文件，激活时自动部署至沙箱 `/home/gem/skills/<name>/`
- Prompt 正文中的相对路径引用（如 `scripts/foo.sh`）会自动改写为沙箱绝对路径
- Prompt 正文中的 `$ARGUMENTS` 占位符会被 `/skill-name <args>` 中的参数替换
- 每个会话的激活 skill 列表存储于 `session.ActiveSkills []string`

## 会话事件类型

`Session.Events` 中的事件驱动 `harness.go:buildMessages` 重建完整对话历史：

| 类型 | Content 内容 |
|------|-------------|
| `user_message` | string |
| `assistant_message` | string |
| `tool_use` | `{id, name, input}` |
| `tool_result` | `{tool_use_id, output, is_error}` |
