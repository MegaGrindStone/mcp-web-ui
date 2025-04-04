# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Pass logger to MCP client

### Fixed

- Fix if given chat_id query in home handlers is not found in store

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
