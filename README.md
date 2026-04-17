# Managed Agent

一个基于 Go 的 AI Agent 服务，将 LLM 与沙箱执行环境深度集成，支持会话管理、Skills 扩展和 SSE 流式响应。

## 背景与设计理念

本项目借鉴 Anthropic 工程博客 [Managed Agents](https://www.anthropic.com/engineering/managed-agents) 中提出的核心架构思想，并以 MVP 最小化的方式落地实现。

原文的核心洞察是：应将 Agent 的**"大脑"**（LLM 推理 + 控制逻辑）与**"手"**（沙箱工具执行环境）解耦，各自独立运行、独立故障恢复，通过持久化的会话事件日志串联两者，避免任何一方故障导致整个任务丢失。

本项目的 MVP 实现：

| 原文概念 | 本项目实现 |
|----------|-----------|
| 大脑与手分离 | Go 服务（控制逻辑）+ 独立 Sandbox 服务（工具执行） |
| 会话持久化 | 会话事件以文件形式存储于 `data/sessions/`，服务重启不丢失 |
| 工具虚拟化 | 通过 Sandbox SDK 统一封装代码执行、浏览器、文件等工具 |
| 灵活扩展 | Skills 系统支持按需注入专属工作流指导 |

> 有意简化的部分：多租户隔离、凭证安全隔离、多大脑/多沙箱横向扩展——这些留待后续按需引入。

## 功能特性

- **会话管理**：创建/持久化多轮对话，文件存储
- **图片视觉**：上传图片后后端自动下载并 base64 编码，以原生 `image` content block 传入 LLM，三个 provider（Claude/OpenAI/Gemini）均支持；进程内缓存避免重复下载，缓存 miss 降级为文本路径
- **文件上传**：支持多附件上传至沙箱，图片可直接被模型"看到"，非图片文件路径注入上下文
- **Skills 系统**：按目录加载 SKILL.md，支持用户 `/skill-name` 激活或模型自动激活
- **沙箱集成**：代码执行、文件读写、浏览器操作等工具
- **SSE 流式输出**：实时推送 Agent 思考和工具调用过程
- **Web 前端**：内置聊天界面，支持附件预览、拖拽上传
- **文件下载**：直接从沙箱下载生成的文件

## 快速开始

### 前置条件

- Go 1.23+
- 可访问的 LLM API（兼容 OpenAI 格式）
- Sandbox 服务实例

### 第一步：准备 Sandbox

前往 [百度内部 Sandbox 控制台](https://console.cloud.baidu-int.com/onetool/sandbox-square) 获取一个 **Fullstack** 类型的沙箱，创建完成后获取：

- **Sandbox 访问地址**（形如 `https://8080-xxxxxxxx.agent-sandbox.baidu-int.com`）
- **Sandbox ID**（形如 `xxxxxxxx`）

> **推荐使用自定义镜像**：申请沙箱时，镜像地址填写：
> ```
> iregistry.baidu-int.com/magellan-public/vefaas-public/all-in-one-sandbox-ducc:1.6.3
> ```
> 该镜像已预装本项目所需的完整运行环境。

### 第二步：配置 config.yaml

编辑项目根目录下的 `config.yaml`，填入必要参数：

```yaml
llm_api_key: sk-your-api-key-here      # 必填：LLM API Key
llm_base_url: https://...              # 必填：LLM API 地址
sandbox_base_url: https://8080-xxx...  # 必填：Sandbox 访问地址
sandbox_id: xxxxxxxx                   # 必填：Sandbox ID
```

其余字段有默认值，按需修改即可。

### 第三步：解压 Skills

项目内置的 Skills 以压缩包形式提供，运行前需先解压：

```bash
unzip skills.zip
```

解压后会在项目根目录生成 `skills/` 目录。

### 第四步：构建并启动

```bash
# 构建并启动
go build -o managed-agent && ./managed-agent

# 打开浏览器
open http://localhost:8080
```

## 配置说明

编辑 `config.yaml`（优先级：环境变量 > 配置文件 > 默认值）：

| 字段 | 说明 | 默认值 |
|------|------|--------|
| `llm_base_url` | LLM API 地址 | — |
| `llm_api_key` | API Key | — |
| `llm_model` | 模型名称 | Claude Sonnet 4.6 |
| `llm_max_tokens` | 最大 Token 数 | 8192 |
| `sandbox_base_url` | Sandbox 服务地址 | — |
| `sandbox_id` | Sandbox 实例 ID | — |
| `listen_addr` | 服务监听地址 | :8080 |
| `data_dir` | 会话数据目录 | data/sessions |
| `skills_dir` | Skills 目录 | skills |

## API 接口

| 方法 | 路径 | 说明 |
|------|------|------|
| `POST` | `/api/sessions` | 创建新会话 |
| `GET` | `/api/sessions` | 列出所有会话 |
| `GET` | `/api/sessions/{id}` | 获取会话详情 |
| `POST` | `/api/sessions/{id}/messages` | 发送消息（SSE 流式响应） |
| `POST` | `/api/sessions/{id}/upload` | 上传文件/图片到沙箱 |
| `GET` | `/api/files/download?path=` | 从沙箱下载文件 |
| `GET` | `/api/files/content?path=` | 读取沙箱文件内容（用于图片预览）|

## Skills 系统

Skills 是可插拔的提示扩展，放在 `skills/` 目录下，每个 Skill 为独立子目录。

### 目录结构

```
skills/
└── my-skill/
    ├── SKILL.md        # 必须：包含 YAML frontmatter + 提示内容
    ├── scripts/        # 可选：部署到沙箱的脚本
    ├── references/     # 可选：参考文档
    └── assets/         # 可选：静态资源
```

### SKILL.md 示例

```markdown
---
name: my-skill
description: 这是一个示例 Skill
user_invocable: true
---

# My Skill

你的提示内容写在这里...
```

### 激活方式

**用户激活**：在对话中输入 `/skill-name [可选参数]`
（需 `user_invocable: true`，默认为 true）

**模型自动激活**：模型根据对话内容判断后，通过内置 `skill` 工具自动激活
（默认允许，可在 frontmatter 中设置 `disable-model-invocation: true` 禁止）

### SKILL.md frontmatter 字段

| 字段 | 说明 | 默认值 |
|------|------|--------|
| `name` | Skill 名称 | 目录名 |
| `description` | 描述（模型判断激活时机的依据） | — |
| `trigger` | 触发场景提示（注入系统 prompt） | — |
| `user_invocable` | 是否允许用户 `/name` 激活 | `true` |
| `disable-model-invocation` | 是否禁止模型自动激活 | `false` |

## 架构

```
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
                    │  Claude  │           │   Sandbox    │  │   File   │
                    │   API    │           │  (工具执行)   │  │  Store   │
                    └──────────┘           └──────────────┘  └──────────┘
```

核心流程：客户端发送消息 → Agent Harness 构建系统 prompt（含激活的 Skills）→ 调用 Claude API → 模型返回文本或工具调用 → 工具在 Sandbox 中执行 → 结果回传 → 循环直到任务完成，全程 SSE 实时推送。
