#!/bin/sh
cat <<'JSON'
{"type":"result","subtype":"success","is_error":true,"result":"API Error: 404 {\"type\":\"error\",\"error\":{\"type\":\"not_found_error\",\"message\":\"model: nonexistent-model\"}}","api_error_status":404,"session_id":"deadbeef","total_cost_usd":0,"duration_ms":412}
JSON
exit 1
