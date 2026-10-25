# LLMGW examples

| Example | What it shows |
|---|---|
| [`demo/run.sh`](demo/run.sh) | **Start here.** The gateway in front of a fake LLM that echoes what it receives: no provider key needed. |
| [`config.minimal.toml`](config.minimal.toml) | A short, English-commented configuration: Ollama, Mistral and Anthropic targets, two profiles, two users. |
| [`anonymisation_preview.py`](anonymisation_preview.py) | What a profile would send to the LLM for a given text, without calling any LLM. |
| [`clients/curl.sh`](clients/curl.sh) | Raw HTTP: `/v1/me`, `/v1/models`, OpenAI and Anthropic formats, forced target, streaming. |
| [`clients/python_openai.py`](clients/python_openai.py) | OpenAI Python SDK, with the conversation and application headers. |
| [`clients/python_anthropic.py`](clients/python_anthropic.py) | Anthropic Python SDK, plain and streaming. |
| [`clients/node_openai.mjs`](clients/node_openai.mjs) | OpenAI Node.js SDK. |
| [`clients/claude_code.sh`](clients/claude_code.sh) | Claude Code routed through the gateway. |
| [`../integrations/hermes/`](../integrations/hermes/) | Hermes Agent kit: one command routes the whole agent through the gateway. |

## The one-minute demo

```bash
examples/demo/run.sh
```

It needs Go (to build `llmgw`) and Python 3. Output:

```
=== the client sends:
Please answer Mrs Claire Dubois (claire.dubois@acme.be, +32 470 12 34 56) about ticket TCK-104233 for ACME SA.

--- the LLM receives:
Please answer Mrs «PERSON_1» («EMAIL_1», «PHONE_1») about ticket «TICKET_1» for «COMPANY_1».
=== the client receives:
You wrote: Please answer Mrs Claire Dubois (claire.dubois@acme.be, +32 470 12 34 56) about ticket TCK-104233 for ACME SA.
```

The same happens in streaming, where a placeholder split across two chunks is held back until it is complete. The third request carries an IBAN, which the demo profile blocks: the client gets a 403 and the LLM never sees it. The dashboard of the demo is at http://127.0.0.1:8090/dashboard (token `demo-admin`) while the script waits.

## Trying the client examples

1. Copy `config.minimal.toml` to `config.toml` in the repository root, change `admin_token` and the user keys, and export the provider keys you use (`MISTRAL_API_KEY`, `ANTHROPIC_API_KEY`). Without any key, the `local` target uses Ollama on `localhost:11434`.
2. Start the gateway: `go build -o llmgw . && ./llmgw -config config.toml`.
3. Run a client:

```bash
export LLMGW_URL=http://127.0.0.1:8080 LLMGW_KEY=gw-alice-change-me
examples/clients/curl.sh
python3 examples/clients/python_openai.py
```

The model names in the examples (`mistral-small-latest`, `claude-haiku-4-5`) must match a target of your profile; `GET /v1/models` lists the ones your key may use.

## Headers the gateway understands

| Header | Effect |
|---|---|
| `Authorization: Bearer <key>` or `x-api-key: <key>` | The gateway key: identifies the user, hence the profile, targets and quotas. |
| `X-LLMGW-Session: <id>` | Names the conversation: placeholders stay the same from turn to turn (memory only, forgotten after 24 h of inactivity). Claude Code's session id is picked up automatically. |
| `X-LLMGW-Client: <name>` | Names the application in the reporting (the User-Agent is used otherwise). |
| `X-Admin-Token: <token>` | Admin API (`/admin/api/...`), as used by `anonymisation_preview.py`. |
