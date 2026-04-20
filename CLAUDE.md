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
sandbox_base_url: https://your-sandbox.example.com
sandbox_id: your-sandbox-id
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
| `llm_custom_header` | 注入 LLM 请求的自定义 HTTP 头（JSON 字符串） | 空 |
| `max_loop_rounds` | 每次请求最大工具调用轮数，超出则返回错误 | `50` |
| `listen_addr` | HTTP 服务监听地址 | `:8080` |
| `data_dir` | 会话数据存储目录 | `data/sessions` |
| `skills_dir` | Skills 目录 | `skills` |
| `memory_event_threshold` | 事件数超过此值时触发会话摘要 | `40` |
| `memory_recent_count` | 摘要时保留最近事件数（滑动窗口尾部） | `20` |

仓库默认直接包含 `skills/` 目录，首次运行前无需额外解压内置 Skills。

## 架构

整个服务是单包 `main` 的 Go 程序，所有 `.go` 文件共享 `package main`。核心设计将 **LLM 推理**（LLM API）与**工具执行**（Sandbox 服务）解耦，通过持久化事件日志串联两者。

**用户消息的处理流程：**
1. `api.go:setupRoutes` 接收 HTTP 请求 → 处理 `/skill-name` 快捷激活，或转交 `RunAgent`
2. `harness.go:RunAgent` 加载会话事件，构建系统 prompt（基础 prompt + skill 摘要 + 激活 skill 的完整 prompt），调用 `LLMClient.CallStream`（由 `config.llm_provider` 决定实际实现：claude/gemini/openai）
3. LLM 返回文本或工具调用；工具调用按类型分发：
   - `skill` 工具 → 在 `harness.go:executeSkillTool` 本地处理（activate/deactivate/list）
   - 其他工具 → 经 `tools.go:ExecuteTool` 转发至 `sandbox_sdk.go:SDKSandboxClient`
4. 工具结果作为事件持久化，并在下一轮循环中回传给 LLM
5. 所有中间事件通过 SSE 实时推送给客户端

**核心文件说明：**
- `harness.go` — Agent 循环（`RunAgent`）、系统 prompt 组装、从事件日志重建消息历史、skill 文件部署到沙箱、图片预热缓存（`prefetchSessionImages`）与内容块构建（`buildContentBlocks`）
- `image_cache.go` — 进程内图片缓存（`ImageCache`）：key 为沙箱路径，value 为 base64 编码字节 + MIME 类型；线程安全，缓存 miss 时降级为文本路径
- `session.go` — `Session` 与 `SessionStore`：文件存储于 `data/sessions/`，每个会话是一个含有序 `[]Event` 的 `<id>.json`
- `skills.go` — `SkillRegistry` 与 `Skill`：加载 `skills/<name>/SKILL.md`（含 YAML frontmatter），支持 `user_invocable` 和 `disable-model-invocation` 标志，激活时将捆绑文件（`scripts/`、`references/`、`assets/`）部署至沙箱的 `/home/gem/skills/<name>/`
- `llm/client.go` — `LLMClient` 接口、`ContentBlock`（含 `ImageMIMEType`/`ImageData` 字段及自定义 `MarshalJSON`）、`ClaudeMessage`
- `llm/claude.go` — `ClaudeClient` 实现（Anthropic Messages API），序列化 `image` block 为 `source.type=base64` 格式
- `llm/gemini.go` — `GeminiClient` 实现（Google Gemini streamGenerateContent API），序列化 `image` block 为 `inlineData` part
- `llm/openai.go` — `OpenAIClient` 实现（OpenAI 兼容 /v1/chat/completions API），序列化 `image` block 为 `image_url` 格式；`buildOAIMessages` 负责消息格式转换
- `sandbox_sdk.go` — `SDKSandboxClient`：封装 `github.com/agent-infra/sandbox-sdk-go`，提供 `ExecCommand`、`WriteFile`、`ReadFile`、`DownloadFile`
- `tools.go` — `ToolDefinitions`（Claude 工具 schema）与 `ExecuteTool`（分发至 Sandbox SDK）；含 `memory` 工具（save/recall/delete/list）
- `memory.go` — 跨会话记忆系统：`MemoryStore` 接口、`FileMemoryStore`（JSON 文件持久化）、`buildMemoryPrompt`（注入系统 prompt）、`extractMemories`（自动提取）、`summarizeEvents`（增量摘要）
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

## 记忆系统

跨会话持久化记忆 + 会话内长上下文管理，混合模式（自动提取 + 显式工具管理）。

**存储**：`data/memory.json`（JSON 文件，`FileMemoryStore` 实现 `MemoryStore` 接口）

**去重策略**（`Save` 方法三层检查）：
1. Key 精确匹配 → 更新已有条目
2. Content 归一化匹配（trim + lowercase）→ 合并到已有条目，采用新 key，合并 tags
3. 以上均不匹配 → 新建条目

**自动提取**：Agent 循环结束时异步调用 `extractMemories`，通过 LLM 从新增对话中提取用户偏好、项目决策等

**系统 prompt 注入**：`buildMemoryPrompt` 将最多 50 条记忆（~2000 token 预算）格式化注入系统 prompt

**滑动窗口 + 增量摘要**：
- 当事件数超过 `memory_event_threshold`（默认 40）时触发
- 最小增量门控：新增至少 6 条事件才重新摘要，避免频繁触发
- 增量模式：已有旧摘要时，只传 "旧摘要 + 新增事件" 给 LLM 合并，避免全量重做
- 摘要结果持久化于 `session.ConversationSummary`，后续循环直接复用

**`memory` 工具**：LLM 可主动调用 `save`/`recall`/`delete`/`list` 操作管理记忆

**Session 扩展字段**：
- `MemoryExtractedIndex` — 自动提取已处理到的事件下标
- `ConversationSummary` — 当前会话的增量摘要文本
- `SummaryUpToEventIndex` — 摘要覆盖的事件范围上界

## 会话事件类型

`Session.Events` 中的事件驱动 `harness.go:buildMessages` 重建完整对话历史：

| 类型 | Content 内容 |
|------|-------------|
| `user_message` | `UserMessageContent{Text string, Attachments []Attachment}`（或历史兼容的纯 string） |
| `assistant_message` | string |
| `tool_use` | `{id, name, input}` |
| `tool_result` | `{tool_use_id, output, is_error}` |

**图片视觉能力**：`user_message` 中 `IsImage=true` 的附件在 agent 循环前由 `prefetchSessionImages` 从沙箱下载，base64 编码后写入 `ImageCache`；`buildMessages` 时缓存命中则生成原生 `image` content block 传入 LLM，缓存 miss 降级为文本路径描述。三个 provider（Claude/OpenAI/Gemini）各自在序列化层处理 `image` block 格式差异，`CallStream` 接口签名不变。
