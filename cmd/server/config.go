package main

import (
	"fmt"
	"log/slog"
	"os"

	"github.com/MegaGrindStone/mcp-web-ui/internal/handlers"
	"github.com/MegaGrindStone/mcp-web-ui/internal/services"
	"gopkg.in/yaml.v3"
)

type llmConfig interface {
	llm(string, *slog.Logger) (handlers.LLM, error)
	titleGen(string, *slog.Logger) (handlers.TitleGenerator, error)
}

// BaseLLMConfig contains the common fields for all LLM configurations.
type BaseLLMConfig struct {
	Provider   string                 `yaml:"provider"`
	Model      string                 `yaml:"model"`
	Parameters services.LLMParameters `yaml:"parameters"`
}

type config struct {
	Port                 string                          `yaml:"port"`
	LogLevel             string                          `yaml:"logLevel"`
	LogMode              string                          `yaml:"logMode"`
	SystemPrompt         string                          `yaml:"systemPrompt"`
	TitleGeneratorPrompt string                          `yaml:"titleGeneratorPrompt"`
	LLM                  llmConfig                       `yaml:"llm"`
	GenTitleLLM          llmConfig                       `yaml:"genTitleLLM"`
	MCPSSEServers        map[string]mcpSSEServerConfig   `yaml:"mcpSSEServers"`
	MCPStdIOServers      map[string]mcpStdIOServerConfig `yaml:"mcpStdIOServers"`
}

type ollamaConfig struct {
	BaseLLMConfig `yaml:",inline"`
	Host          string `yaml:"host"`
}

type anthropicConfig struct {
	BaseLLMConfig `yaml:",inline"`
	APIKey        string `yaml:"apiKey"`
	MaxTokens     int    `yaml:"maxTokens"`
}

type openaiConfig struct {
	BaseLLMConfig `yaml:",inline"`
	APIKey        string `yaml:"apiKey"`
	Endpoint      string `yaml:"endpoint"`
}

type openrouterConfig struct {
	BaseLLMConfig `yaml:",inline"`
	APIKey        string `yaml:"apiKey"`
}

type mcpSSEServerConfig struct {
	URL            string `yaml:"url"`
	MaxPayloadSize int    `yaml:"maxPayloadSize"`
}

type mcpStdIOServerConfig struct {
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

func (c *config) UnmarshalYAML(value *yaml.Node) error {
	var rawConfig struct {
		Port                 string                          `yaml:"port"`
		LogLevel             string                          `yaml:"logLevel"`
		LogMode              string                          `yaml:"logMode"`
		SystemPrompt         string                          `yaml:"systemPrompt"`
		TitleGeneratorPrompt string                          `yaml:"titleGeneratorPrompt"`
		LLM                  map[string]any                  `yaml:"llm"`
		GenTitleLLM          map[string]any                  `yaml:"genTitleLLM"`
		MCPSSEServers        map[string]mcpSSEServerConfig   `yaml:"mcpSSEServers"`
		MCPStdIOServers      map[string]mcpStdIOServerConfig `yaml:"mcpStdIOServers"`
	}

	if err := value.Decode(&rawConfig); err != nil {
		return err
	}

	c.Port = rawConfig.Port
	c.LogLevel = rawConfig.LogLevel
	c.LogMode = rawConfig.LogMode
	c.SystemPrompt = rawConfig.SystemPrompt
	c.TitleGeneratorPrompt = rawConfig.TitleGeneratorPrompt

	llmProvider, ok := rawConfig.LLM["provider"].(string)
	if !ok {
		return fmt.Errorf("llm provider is required")
	}

	llmRawYAML, err := yaml.Marshal(rawConfig.LLM)
	if err != nil {
		return err
	}
	genTitleLLMRawYAML, err := yaml.Marshal(rawConfig.GenTitleLLM)
	if err != nil {
		return err
	}

	var llm llmConfig
	switch llmProvider {
	case "ollama":
		llm = &ollamaConfig{}
	case "anthropic":
		llm = &anthropicConfig{}
	case "openai":
		llm = &openaiConfig{}
	case "openrouter":
		llm = &openrouterConfig{}
	default:
		return fmt.Errorf("unknown llm provider: %s", llmProvider)
	}

	if err := yaml.Unmarshal(llmRawYAML, llm); err != nil {
		return err
	}

	var genTitleLLM llmConfig
	useSameLLM := true
	genTitleLLM = llm
	genTitleLLMProvider, ok := rawConfig.GenTitleLLM["provider"].(string)
	if ok {
		useSameLLM = false
		switch genTitleLLMProvider {
		case "ollama":
			genTitleLLM = &ollamaConfig{}
		case "anthropic":
			genTitleLLM = &anthropicConfig{}
		case "openai":
			genTitleLLM = &openaiConfig{}
		case "openrouter":
			genTitleLLM = &openrouterConfig{}
		}
	}

	if !useSameLLM {
		if err := yaml.Unmarshal(genTitleLLMRawYAML, genTitleLLM); err != nil {
			return err
		}
	}

	c.LLM = llm
	c.GenTitleLLM = genTitleLLM
	c.MCPSSEServers = rawConfig.MCPSSEServers
	c.MCPStdIOServers = rawConfig.MCPStdIOServers

	return nil
}

func (o ollamaConfig) newOllama(systemPrompt string, logger *slog.Logger) (services.Ollama, error) {
	if o.Model == "" {
		return services.Ollama{}, fmt.Errorf("model is required")
	}

	host := o.Host
	if host == "" {
		host = os.Getenv("OLLAMA_HOST")
	}
	return services.NewOllama(host, o.Model, systemPrompt, o.Parameters, logger), nil
}

func (o ollamaConfig) llm(systemPrompt string, logger *slog.Logger) (handlers.LLM, error) {
	return o.newOllama(systemPrompt, logger)
}

func (o ollamaConfig) titleGen(systemPrompt string, logger *slog.Logger) (handlers.TitleGenerator, error) {
	return o.newOllama(systemPrompt, logger)
}

func (a anthropicConfig) newAnthropic(systemPrompt string, _ *slog.Logger) (services.Anthropic, error) {
	if a.Model == "" {
		return services.Anthropic{}, fmt.Errorf("model is required")
	}
	if a.MaxTokens == 0 {
		return services.Anthropic{}, fmt.Errorf("max_tokens is required")
	}

	apiKey := a.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("ANTHROPIC_API_KEY")
	}

	return services.NewAnthropic(apiKey, a.Model, systemPrompt, a.MaxTokens, a.Parameters), nil
}

func (a anthropicConfig) llm(systemPrompt string, logger *slog.Logger) (handlers.LLM, error) {
	return a.newAnthropic(systemPrompt, logger)
}

func (a anthropicConfig) titleGen(systemPrompt string, logger *slog.Logger) (handlers.TitleGenerator, error) {
	return a.newAnthropic(systemPrompt, logger)
}

func (o openaiConfig) newOpenAI(systemPrompt string, logger *slog.Logger) (services.OpenAI, error) {
	if o.Model == "" {
		return services.OpenAI{}, fmt.Errorf("model is required")
	}

	apiKey := o.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	return services.NewOpenAI(apiKey, o.Model, systemPrompt, o.Endpoint, o.Parameters, logger), nil
}

func (o openaiConfig) llm(systemPrompt string, logger *slog.Logger) (handlers.LLM, error) {
	return o.newOpenAI(systemPrompt, logger)
}

func (o openaiConfig) titleGen(systemPrompt string, logger *slog.Logger) (handlers.TitleGenerator, error) {
	return o.newOpenAI(systemPrompt, logger)
}

func (o openrouterConfig) newOpenRouter(systemPrompt string, logger *slog.Logger) (services.OpenRouter, error) {
	if o.Model == "" {
		return services.OpenRouter{}, fmt.Errorf("model is required")
	}

	apiKey := o.APIKey
	if apiKey == "" {
		apiKey = os.Getenv("OPENROUTER_API_KEY")
	}
	return services.NewOpenRouter(apiKey, o.Model, systemPrompt, o.Parameters, logger), nil
}

func (o openrouterConfig) llm(systemPrompt string, logger *slog.Logger) (handlers.LLM, error) {
	return o.newOpenRouter(systemPrompt, logger)
}

func (o openrouterConfig) titleGen(systemPrompt string, logger *slog.Logger) (handlers.TitleGenerator, error) {
	return o.newOpenRouter(systemPrompt, logger)
}
