# Managed Agent

[English README](./README.md)

Managed Agent 是一个基于 Go 的 AI Agent 服务，它将 LLM 编排能力与 AIO sandbox 运行时结合在一起，提供持久化会话、工具执行、Skills 提示扩展、文件与图片处理，以及基于 SSE 的流式交互能力。

## 项目定位

这个仓库是对 Anthropic 在 [Managed Agents](https://www.anthropic.com/engineering/managed-agents) 一文中所描述架构的一份紧凑实现：把 Agent 的推理层和工具执行层拆开，通过持久化事件日志把它们连接起来，使系统更容易恢复、观测和扩展。

在本项目中：

- **大脑** 是 Go 服务，负责 prompt 组装、会话管理、工具循环和模型接入
- **手** 由 **AIO sandbox** 提供，负责执行命令、浏览器交互和文件操作
- **连接层** 是存储在 `data/sessions/` 下的持久化会话事件日志

## 核心特性

- 多轮会话持久化到本地文件
- 基于 SSE 的实时文本与工具事件流
- 支持 Claude、OpenAI 兼容接口和 Gemini
- 图片作为原生多模态内容传给模型
- 文件上传与沙箱下载链路完整
- Skills 直接从版本管理中的 `skills/` 目录加载
- 真实沙箱集成测试采用显式开启
- 自带 Web UI，便于本地直接运行

## 架构

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

核心请求流程：

1. 客户端发送消息。
2. Agent Harness 重建会话历史和已激活 Skills。
3. 服务调用配置好的 LLM provider。
4. 模型返回文本或工具调用。
5. 工具调用在 AIO sandbox 中执行。
6. 结果持久化为事件，并持续流式回传给客户端。

## 功能概览

### LLM 编排层

- `claude`、`openai`、`gemini` 三种 provider 抽象
- 带轮次限制的 tool-use 循环
- 支持 Skills 的系统 prompt 构建
- 可选完整请求/响应调试日志

### Sandbox 运行时

- 命令执行
- 浏览器导航与交互
- 文件读写、上传、下载
- 代码执行辅助能力

### Skills 系统

- Skills 从 `skills/<name>/SKILL.md` 加载
- 支持用户通过 `/skill-name` 显式激活
- 支持模型按需自动激活
- 激活时可将 `scripts/`、`references/`、`assets/` 一并部署到 sandbox

### 文件与图片能力

- 附件先上传到 sandbox
- 图片附件会转换成 provider 原生图像内容块
- 非图片文件则以 sandbox 路径形式注入上下文

## 快速开始

### 前置条件

- Go 1.23+
- 可访问的 LLM API
- 一个与当前 sandbox SDK 兼容的 **AIO sandbox** 实例

### 1. 准备 Sandbox

准备一个 AIO sandbox 实例，并获取：

- `sandbox_base_url`，例如 `https://your-sandbox.example.com`
- `sandbox_id`，例如 `your-sandbox-id`

如果你的 sandbox 需要自定义镜像、初始化流程或额外工具，请在你自己的环境中完成准备。本仓库不包含任何厂商内部平台说明。

### 2. 创建 `config.yaml`

```bash
cp config.example.yaml config.yaml
```

然后编辑 `config.yaml`：

```yaml
llm_api_key: sk-your-api-key-here
llm_base_url: https://api.openai.com/v1
sandbox_base_url: https://your-sandbox.example.com
sandbox_id: your-sandbox-id
```

### 3. 启动服务

```bash
go build -o managed-agent && ./managed-agent
```

然后在浏览器中打开 `http://localhost:8080`。

## 开发说明

### 运行测试

```bash
# 单元测试和包级测试
go test ./...

# 真实沙箱集成测试
RUN_SANDBOX_TESTS=1 go test -run 'TestSandbox(Tools|Basic)' -v ./...
```

### 常用 Make 命令

```bash
make build
make test
make package
```

## 配置说明

配置优先级：

1. 环境变量
2. `config.yaml`
3. 内置默认值

关键字段如下：


| 字段                | 说明                                     | 默认值                             |
| ------------------- | ---------------------------------------- | ---------------------------------- |
| `llm_provider`      | LLM 提供商：`claude`、`openai`、`gemini` | `claude`                           |
| `llm_base_url`      | LLM API 地址                             | `https://api.openai.com/v1`        |
| `llm_api_key`       | LLM API Key                              | —                                 |
| `llm_model`         | 模型名称                                 | `Claude Sonnet 4.6`                |
| `llm_max_tokens`    | 最大输出 token 数                        | `8192`                             |
| `llm_custom_header` | 额外 HTTP 头，格式为 JSON 对象字符串     | —                                 |
| `sandbox_base_url`  | AIO sandbox 地址                         | `https://your-sandbox.example.com` |
| `sandbox_id`        | Sandbox 实例 ID                          | `your-sandbox-id`                  |
| `listen_addr`       | HTTP 监听地址                            | `:8080`                            |
| `data_dir`          | 会话存储目录                             | `data/sessions`                    |
| `skills_dir`        | Skills 目录                              | `skills`                           |

## API


| 方法   | 路径                          | 说明                          |
| ------ | ----------------------------- | ----------------------------- |
| `POST` | `/api/sessions`               | 创建新会话                    |
| `GET`  | `/api/sessions`               | 列出会话                      |
| `GET`  | `/api/sessions/{id}`          | 获取会话详情                  |
| `POST` | `/api/sessions/{id}/messages` | 发送消息并通过 SSE 流式返回   |
| `POST` | `/api/sessions/{id}/upload`   | 上传文件或图片到 sandbox      |
| `GET`  | `/api/files/download?path=`   | 从 sandbox 下载文件           |
| `GET`  | `/api/files/content?path=`    | 读取 sandbox 文件内容用于预览 |

## Skills 目录结构

```text
skills/
└── my-skill/
    ├── SKILL.md
    ├── scripts/
    ├── references/
    └── assets/
```

最小 `SKILL.md` 示例：

```markdown
---
name: my-skill
description: 示例 Skill 描述
user_invocable: true
---

# My Skill

在这里写你的提示词内容...
```

## 致谢

- 感谢 Anthropic 对 managed-agent 架构思路的公开分享
- 感谢 [agent-infra/sandbox](https://github.com/agent-infra/sandbox) 提供的 AIO sandbox 相关工作

## License

本项目采用 [MIT License](./LICENSE)。
