#!/bin/sh
# Stands in for a successful `claude -p --output-format json` run.
# Echoes the arguments it was given to stderr so the test can assert on them.
echo "ARGS: $*" >&2
cat <<'JSON'
{"type":"result","subtype":"success","is_error":false,"result":"Hello! How can I help you today?","session_id":"1f0c8f2a-3d4e-4b5a-9c6d-7e8f9a0b1c2d","total_cost_usd":0.0044,"duration_ms":3312,"num_turns":1}
JSON
