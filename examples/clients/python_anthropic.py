"""Using LLMGW from the official Anthropic Python SDK.

    pip install anthropic
    LLMGW_URL=http://127.0.0.1:8080 LLMGW_KEY=gw-alice-change-me python3 python_anthropic.py

The profile must list a target with api = "anthropic" that serves the model.
"""
import os

import anthropic

client = anthropic.Anthropic(
    base_url=os.environ.get("LLMGW_URL", "http://127.0.0.1:8080"),  # no /v1 here
    api_key=os.environ["LLMGW_KEY"],
)

message = client.messages.create(
    model="claude-haiku-4-5",
    max_tokens=300,
    messages=[{
        "role": "user",
        "content": "Summarise for the file: patient Peeters, born 30/12/1961, "
                   "called from +32 2 541 91 91 about her appointment with Dr Janssens.",
    }],
)
print(message.content[0].text)

# Streaming
with client.messages.stream(
    model="claude-haiku-4-5",
    max_tokens=200,
    messages=[{"role": "user", "content": "Write a one-line reminder for Mr Jean Martin."}],
) as stream:
    for text in stream.text_stream:
        print(text, end="", flush=True)
print()
