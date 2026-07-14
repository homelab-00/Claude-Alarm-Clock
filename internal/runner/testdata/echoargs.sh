#!/bin/sh
# Escape backslashes then double quotes, so the argv survives being embedded in
# a JSON string.
ESCAPED=$(printf '%s' "$*" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g')
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","session_id":"x","total_cost_usd":0,"duration_ms":1}\n' "$ESCAPED"
