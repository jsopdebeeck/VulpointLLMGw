#!/usr/bin/env bash
# LLMGW in one minute, without any provider key: starts a fake LLM that echoes what it
# receives, starts the gateway in front of it, and sends three requests.
#
#   examples/demo/run.sh              # needs Go (or an llmgw binary in the repository root) and Python 3
#
# Watch the "the LLM receives" lines: names, emails, phones and tickets never reach the
# LLM, yet the answer shown to the client has them back. The dashboard stays available at
# http://127.0.0.1:8090/dashboard (token demo-admin) until you press Enter.
set -euo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
root="$(cd "$here/../.." && pwd)"
gw="$root/llmgw"
if [ ! -x "$gw" ]; then
	echo "building llmgw…"
	(cd "$root" && go build -o llmgw .)
fi

pids=()
trap 'kill "${pids[@]}" 2>/dev/null || true' EXIT

python3 "$here/echo_llm.py" 9999 &
pids+=($!)
"$gw" -config "$here/config.toml" >"$here/demo.log" 2>&1 &
pids+=($!)
for _ in $(seq 50); do curl -fs http://127.0.0.1:8090/healthz >/dev/null 2>&1 && break; sleep 0.1; done

ask() { # ask "<message>" [stream]
	local stream=${2:-false}
	echo
	echo "=== the client sends:"
	echo "$1"
	local body
	body=$(python3 -c 'import json,sys; print(json.dumps({"model":"echo","stream":sys.argv[2]=="true","messages":[{"role":"user","content":sys.argv[1]}]}))' "$1" "$stream")
	local out
	out=$(curl -s http://127.0.0.1:8090/v1/chat/completions \
		-H "Authorization: Bearer gw-demo" -H "Content-Type: application/json" -d "$body")
	sleep 0.2
	echo "=== the client receives:"
	if [ "$stream" = true ]; then
		printf '%s\n' "$out" | python3 -c '
import json, sys
text = ""
for line in sys.stdin:
    if line.startswith("data: {"):
        for c in json.loads(line[6:]).get("choices", []):
            text += c.get("delta", {}).get("content") or ""
print(text)'
	else
		printf '%s\n' "$out" | python3 -c '
import json, sys
r = json.load(sys.stdin)
print(r["choices"][0]["message"]["content"] if "choices" in r else json.dumps(r, ensure_ascii=False))'
	fi
}

ask "Please answer Mrs Claire Dubois (claire.dubois@acme.be, +32 470 12 34 56) about ticket TCK-104233 for ACME SA."
ask "Same thing in streaming for Jean Martin, jean.martin@acme.be." true
ask "Refund Claire on BE71 0961 2345 6769, please."

echo
echo "The last request was refused (403): the profile blocks IBANs."
echo "Dashboard: http://127.0.0.1:8090/dashboard (token demo-admin). Press Enter to stop."
read -r _
