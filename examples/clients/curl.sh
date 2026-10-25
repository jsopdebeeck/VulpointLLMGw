#!/usr/bin/env bash
# Calling LLMGW with curl. Only the URL and the key differ from calling a provider directly.
#
#   LLMGW_URL=http://127.0.0.1:8080 LLMGW_KEY=gw-alice-change-me examples/clients/curl.sh
set -euo pipefail

url=${LLMGW_URL:-http://127.0.0.1:8080}
key=${LLMGW_KEY:?set LLMGW_KEY to your gateway key}

echo "# who am I: profile, allowed targets, quotas and usage"
curl -s "$url/v1/me" -H "Authorization: Bearer $key"
echo

echo "# models I may use"
curl -s "$url/v1/models" -H "Authorization: Bearer $key"
echo

echo "# OpenAI format: the model is routed to the first target of my profile that serves it"
curl -s "$url/v1/chat/completions" \
	-H "Authorization: Bearer $key" -H "Content-Type: application/json" \
	-d '{
	  "model": "mistral-small-latest",
	  "messages": [{"role": "user", "content": "Write a two-line apology to Mrs Claire Dubois (claire.dubois@acme.be)."}]
	}'
echo

echo "# OpenAI format, forcing one target with <target>/<model>"
curl -s "$url/v1/chat/completions" \
	-H "Authorization: Bearer $key" -H "Content-Type: application/json" \
	-d '{"model": "local/llama3.2", "messages": [{"role": "user", "content": "Say hello."}]}'
echo

echo "# Anthropic format (x-api-key works as well as Authorization: Bearer)"
curl -s "$url/v1/messages" \
	-H "x-api-key: $key" -H "anthropic-version: 2023-06-01" -H "Content-Type: application/json" \
	-d '{
	  "model": "claude-haiku-4-5",
	  "max_tokens": 200,
	  "messages": [{"role": "user", "content": "Summarise: Jean Martin (+32 470 12 34 56) asked for a call back."}]
	}'
echo

echo "# streaming (SSE): placeholders split across chunks are restored before reaching you"
curl -sN "$url/v1/chat/completions" \
	-H "Authorization: Bearer $key" -H "Content-Type: application/json" \
	-d '{"model": "mistral-small-latest", "stream": true,
	     "messages": [{"role": "user", "content": "Greet Mr Peeters in one sentence."}]}'
