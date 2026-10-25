"""Using LLMGW from the official OpenAI Python SDK.

    pip install openai
    LLMGW_URL=http://127.0.0.1:8080 LLMGW_KEY=gw-alice-change-me python3 python_openai.py

Any OpenAI-compatible tool works the same way: point base_url at <gateway>/v1 and use
your gateway key. The provider's real key stays on the gateway.
"""
import os

from openai import OpenAI

client = OpenAI(
    base_url=os.environ.get("LLMGW_URL", "http://127.0.0.1:8080") + "/v1",
    api_key=os.environ["LLMGW_KEY"],
    # Optional: name the conversation so placeholders stay stable from turn to turn,
    # and name the application for the per-application reporting.
    default_headers={"X-LLMGW-Session": "demo-conversation-42", "X-LLMGW-Client": "python-example"},
)

# The models this key may use (the profile decides).
print([m.id for m in client.models.list().data])

prompt = (
    "Draft a short reply to Mrs Claire Dubois (claire.dubois@acme.be, +32 470 12 34 56) "
    "confirming that ticket TCK-104233 is resolved."
)

# Plain request. The LLM only sees «PERSON_1», «EMAIL_1», «PHONE_1», «TICKET_1»;
# the answer comes back with the real values.
reply = client.chat.completions.create(
    model="mistral-small-latest",
    messages=[{"role": "user", "content": prompt}],
)
print(reply.choices[0].message.content)

# Streaming works the same way.
stream = client.chat.completions.create(
    model="mistral-small-latest",
    messages=[{"role": "user", "content": "Now the same reply in French."}],
    stream=True,
)
for chunk in stream:
    if chunk.choices and chunk.choices[0].delta.content:
        print(chunk.choices[0].delta.content, end="", flush=True)
print()
