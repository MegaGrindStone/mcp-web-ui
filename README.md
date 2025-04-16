# MCP Web UI

![CI](https://github.com/MegaGrindStone/mcp-web-ui/actions/workflows/ci.yml/badge.svg)
[![Go Report Card](https://goreportcard.com/badge/github.com/MegaGrindStone/mcp-web-ui)](https://goreportcard.com/report/github.com/MegaGrindStone/mcp-web-ui)
[![codecov](https://codecov.io/gh/MegaGrindStone/mcp-web-ui/branch/main/graph/badge.svg)](https://codecov.io/gh/MegaGrindStone/mcp-web-ui)

MCP Web UI is a web-based user interface that serves as a Host within the Model Context Protocol (MCP) architecture. It provides a powerful and user-friendly interface for interacting with Large Language Models (LLMs) while managing context aggregation and coordination between clients and servers.

## 🌟 Overview

MCP Web UI is designed to simplify and enhance interactions with AI language models by providing:
- A unified interface for multiple LLM providers
- Real-time, streaming chat experiences
- Flexible configuration and model management
- Robust context handling using the MCP protocol

### Demo Video

[![YouTube](http://i.ytimg.com/vi/DnC-z0CpRpM/hqdefault.jpg)](https://www.youtube.com/watch?v=DnC-z0CpRpM)

## 🚀 Features

- 🤖 **Multi-Provider LLM Integration**:
  - Anthropic (Claude models)
  - OpenAI (GPT models)
  - Ollama (local models)
  - OpenRouter (multiple providers)
- 💬 **Intuitive Chat Interface**
- 🔄 **Real-time Response Streaming** via Server-Sent Events (SSE)
- 🔧 **Dynamic Configuration Management**
- 📊 **Advanced Context Aggregation**
- 💾 **Persistent Chat History** using BoltDB
- 🎯 **Flexible Model Selection**

## 📋 Prerequisites

- Go 1.23+
- Docker (optional)
- API keys for desired LLM providers

## 🛠 Installation

### Quick Start

1. Clone the repository:
   ```bash
   git clone https://github.com/MegaGrindStone/mcp-web-ui.git
   cd mcp-web-ui
   ```

2. Configure your environment:
   ```bash
   mkdir -p $HOME/.config/mcpwebui
   cp config.example.yaml $HOME/.config/mcpwebui/config.yaml
   ```

3. Set up API keys:
   ```bash
   export ANTHROPIC_API_KEY=your_anthropic_key
   export OPENAI_API_KEY=your_openai_key
   export OPENROUTER_API_KEY=your_openrouter_key
   ```

### Running the Application

#### Local Development
```bash
go mod download
go run ./cmd/server/main.go
```

#### Docker Deployment
```bash
docker build -t mcp-web-ui .
docker run -p 8080:8080 \
  -v $HOME/.config/mcpwebui/config.yaml:/app/config.yaml \
  -e ANTHROPIC_API_KEY \
  -e OPENAI_API_KEY \
  -e OPENROUTER_API_KEY \
  mcp-web-ui
```

## 🔧 Configuration

The configuration file (`config.yaml`) provides comprehensive settings for customizing the MCP Web UI. Here's a detailed breakdown:

### Server Configuration
- `port`: The port on which the server will run (default: 8080)
- `logLevel`: Logging verbosity (options: debug, info, warn, error; default: info)
- `logMode`: Log output format (options: json, text; default: text)

### Prompt Configuration
- `systemPrompt`: Default system prompt for the AI assistant
- `titleGeneratorPrompt`: Prompt used to generate chat titles

### LLM (Language Model) Configuration
The `llm` section supports multiple providers with provider-specific configurations:

#### Common LLM Parameters
- `provider`: Choose from: ollama, anthropic, openai, openrouter
- `model`: Specific model name (e.g., 'claude-3-5-sonnet-20241022')
- `parameters`: Fine-tune model behavior:
  - `temperature`: Randomness of responses (0.0-1.0)
  - `topP`: Nucleus sampling threshold
  - `topK`: Number of highest probability tokens to keep
  - `frequencyPenalty`: Reduce repetition of token sequences
  - `presencePenalty`: Encourage discussing new topics
  - `maxTokens`: Maximum response length
  - `stop`: Sequences to stop generation
  - And more provider-specific parameters

#### Provider-Specific Configurations
- **Ollama**:
  - `host`: Ollama server URL (default: http://localhost:11434)

- **Anthropic**:
  - `apiKey`: Anthropic API key (can use ANTHROPIC_API_KEY env variable)
  - `maxTokens`: Maximum token limit
  - Note: Stop sequences containing only whitespace are ignored, and whitespace is trimmed from valid sequences as Anthropic doesn't support whitespace in stop sequences

- **OpenAI**:
  - `apiKey`: OpenAI API key (can use OPENAI_API_KEY env variable)
  - `endpoint`: OpenAI API endpoint (default: https://api.openai.com/v1)
  - For using alternative OpenAI-compatible APIs, see [this discussion thread](https://github.com/MegaGrindStone/mcp-web-ui/discussions/7)

- **OpenRouter**:
  - `apiKey`: OpenRouter API key (can use OPENROUTER_API_KEY env variable)

### Title Generator Configuration
The `genTitleLLM` section allows separate configuration for title generation, defaulting to the main LLM if not specified.

### MCP Server Configurations
- `mcpSSEServers`: Configure Server-Sent Events (SSE) servers
  - `url`: SSE server URL
  - `maxPayloadSize`: Maximum payload size

- `mcpStdIOServers`: Configure Standard Input/Output servers
  - `command`: Command to run server
  - `args`: Arguments for the server command

#### Example MCP Server Configurations

**SSE Server Example:**
```yaml
mcpSSEServers:
  filesystem:
    url: https://yoursseserver.com
    maxPayloadSize: 1048576 # 1MB
```

**StdIO Server Examples:**

1. Using the [official filesystem MCP server](https://github.com/modelcontextprotocol/servers/tree/main/src/filesystem):
```yaml
mcpStdIOServers:
  filesystem:
    command: npx
    args:
      - -y
      - "@modelcontextprotocol/server-filesystem"
      - "/path/to/your/files"
```
This example can be used directly as the official filesystem mcp server is an executable package that can be run with npx. Just update the path to point to your desired directory.

2. Using [go-mcp filesystem MCP server](https://github.com/MegaGrindStone/go-mcp/tree/main/servers/filesystem):
```yaml
mcpStdIOServers:
  filesystem:
    command: go
    args:
      - run
      - github.com/your_username/your_app # Replace with your app
      - -path
      - "/data/mcp/filesystem" # Path to expose to MCP clients
```
For this example, you'll need to create a new Go application that imports the `github.com/MegaGrindStone/go-mcp/servers/filesystem` package. The flag naming (like `-path` in this example) is completely customizable based on how you structure your own application - it doesn't have to be called "path". [This example](https://github.com/MegaGrindStone/go-mcp/blob/main/example/filesystem/main.go) is merely a starting point showing one possible implementation where a flag is used to specify which directory to expose. You're free to design your own application structure and command-line interface according to your specific needs.

### Example Configuration Snippet
```yaml
port: 8080
logLevel: info
systemPrompt: You are a helpful assistant.

llm:
  provider: anthropic
  model: claude-3-5-sonnet-20241022
  maxTokens: 1000
  parameters:
    temperature: 0.7

genTitleLLM:
  provider: openai
  model: gpt-3.5-turbo
```

## 🏗 Project Structure

- `cmd/`: Application entry point
- `internal/handlers/`: Web request handlers
- `internal/models/`: Data models
- `internal/services/`: LLM provider integrations
- `static/`: Static assets (CSS)
- `templates/`: HTML templates

## 🤝 Contributing

1. Fork the repository
2. Create a feature branch
3. Commit your changes
4. Push and create a Pull Request

## 📄 License

MIT License
