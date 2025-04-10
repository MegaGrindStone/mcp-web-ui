# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Add refresh title feature to refresh chat title from LLM

### Fixed

- Trim whitespace from stop sequences parameters on `Anthropic`, because Anthropic API doesn't support it

## [0.1.1] - 2025-04-05

This update introduces configuration flexibility for OpenAI API endpoints, improves system logging capabilities, and fixes a navigation issue with non-existent chat conversations, collectively enhancing both the system's stability and customization options.

### Added

- Add custom endpoint configuration option for OpenAI provider, allowing connection to alternative API servers

### Changed

- Enhance Model Control Protocol (MCP) client with dedicated logger for improved diagnostics and troubleshooting

### Fixed

- Resolve error handling when navigating to non-existent chat conversations in the UI

## [0.1.0] - 2025-03-03

This release introduces a complete web-based chat interface for LLMs with support for multiple providers (Ollama, Anthropic, OpenAI, OpenRouter), persistent conversation storage, and extensive customization options. The addition of containerized deployment and structured logging improves the system's operability, while the ability to use external tools with Anthropic models extends the functional capabilities.

### Added

- Add web-based user interface for chatting with Large Language Models (LLMs)
- Integrate multiple LLM providers: Ollama, Anthropic, OpenAI and OpenRouter
- Implement Bolt database for persistent storage of chat history and messages
- Add configuration file system for managing LLM provider settings
- Add customizable LLM parameters for fine-tuning model behavior
- Implement dedicated LLM instance for generating conversation titles
- Add ability to customize system prompts and conversation title generation
- Enable tools interaction capability for Anthropic models
- Display system objects (servers, tools, resources, prompts) in the user interface
- Add structured logging for improved monitoring and troubleshooting
- Include Dockerfile for containerized deployment
