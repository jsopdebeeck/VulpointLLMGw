#!/usr/bin/env bash
# Running Claude Code through LLMGW: every prompt, file and tool result it sends is
# pseudonymised, and the conversation memory keeps «PERSON_1» the same person across turns
# (the gateway reads Claude Code's session id).
#
#   LLMGW_URL=https://gateway.example.com LLMGW_KEY=gw-alice-change-me examples/clients/claude_code.sh
#
# The profile needs a target with api = "anthropic" (Anthropic itself, or a provider that
# speaks the Anthropic format) serving the claude-* models.
set -euo pipefail

export ANTHROPIC_BASE_URL=${LLMGW_URL:?set LLMGW_URL}
export ANTHROPIC_AUTH_TOKEN=${LLMGW_KEY:?set LLMGW_KEY}
unset ANTHROPIC_API_KEY  # otherwise it would take precedence

exec claude "$@"
