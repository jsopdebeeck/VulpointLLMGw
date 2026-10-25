#!/usr/bin/env python3
"""Preview what a profile would send to the LLM, without calling any LLM.

Uses the dashboard's test bench (POST /admin/api/test, admin token required). Nothing is
learned or stored: handy to tune detectors and allow lists on your own texts.

    LLMGW_URL=http://127.0.0.1:8080 LLMGW_ADMIN_TOKEN=change-me \
        python3 anonymisation_preview.py standard "Mrs Claire Dubois, claire.dubois@acme.be"
    python3 anonymisation_preview.py standard < letter.txt

Standard library only.
"""
import json
import os
import sys
import urllib.request

url = os.environ.get("LLMGW_URL", "http://127.0.0.1:8080") + "/admin/api/test"
token = os.environ["LLMGW_ADMIN_TOKEN"]
profile = sys.argv[1] if len(sys.argv) > 1 else "standard"
text = " ".join(sys.argv[2:]) if len(sys.argv) > 2 else sys.stdin.read()

req = urllib.request.Request(
    url,
    data=json.dumps({"profile": profile, "text": text}).encode(),
    headers={"X-Admin-Token": token, "Content-Type": "application/json"},
)
with urllib.request.urlopen(req) as resp:
    result = json.load(resp)

print("The LLM would receive:\n")
print(result["output"])
if result["blocked"]:
    print("\n!! This profile would REFUSE the request (a data type set to block was found).")
print("\nPlaceholders:")
for placeholder, value in sorted(result["mapping"].items()):
    print(f"  «{placeholder}» = {value}")
print("\nDetections:", json.dumps(result["stats"], ensure_ascii=False))
