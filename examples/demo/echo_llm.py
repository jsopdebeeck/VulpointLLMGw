#!/usr/bin/env python3
"""A fake OpenAI-compatible LLM that answers with what it received.

It shows what a real provider would see behind LLMGW: the gateway pseudonymises the
request on the way out, this server prints it, echoes it back, and the gateway restores
the original values before the client gets the answer.

    python3 echo_llm.py [port]        # default port 9999, standard library only
"""
import json
import sys
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def text_of(content):
    """Message content is a string or a list of parts (OpenAI format)."""
    if isinstance(content, str):
        return content
    return " ".join(p.get("text", "") for p in content or [] if p.get("type") == "text")


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def send_json(self, obj, status=200):
        body = json.dumps(obj).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path.endswith("/models"):
            self.send_json({"object": "list", "data": [{"id": "echo", "object": "model"}]})
        else:
            self.send_json({"error": "not found"}, 404)

    def do_POST(self):
        if not self.path.endswith("/chat/completions"):
            return self.send_json({"error": "not found"}, 404)
        req = json.loads(self.rfile.read(int(self.headers.get("Content-Length", 0))))
        user = [text_of(m.get("content")) for m in req.get("messages", []) if m.get("role") == "user"]
        seen = user[-1] if user else ""
        print(f"\n--- the LLM receives:\n{seen}", flush=True)
        answer = f"You wrote: {seen}"
        model = req.get("model", "echo")
        usage = {"prompt_tokens": len(seen.split()), "completion_tokens": len(answer.split())}
        usage["total_tokens"] = usage["prompt_tokens"] + usage["completion_tokens"]

        if not req.get("stream"):
            return self.send_json({
                "id": "echo-1", "object": "chat.completion", "created": int(time.time()), "model": model,
                "choices": [{"index": 0, "message": {"role": "assistant", "content": answer}, "finish_reason": "stop"}],
                "usage": usage,
            })

        # Streaming: a few words per chunk, so placeholders get split across chunks
        # and the gateway has to reassemble them before restoring.
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.end_headers()
        words = answer.split(" ")
        for i in range(0, len(words), 2):
            piece = " ".join(words[i:i + 2]) + ("" if i + 2 >= len(words) else " ")
            chunk = {"id": "echo-1", "object": "chat.completion.chunk", "model": model,
                     "choices": [{"index": 0, "delta": {"content": piece}, "finish_reason": None}]}
            self.wfile.write(f"data: {json.dumps(chunk)}\n\n".encode())
            self.wfile.flush()
            time.sleep(0.03)
        end = {"id": "echo-1", "object": "chat.completion.chunk", "model": model,
               "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}], "usage": usage}
        self.wfile.write(f"data: {json.dumps(end)}\n\ndata: [DONE]\n\n".encode())


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 9999
    print(f"echo LLM on http://127.0.0.1:{port}/v1", flush=True)
    ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
