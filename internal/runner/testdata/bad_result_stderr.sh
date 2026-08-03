#!/bin/sh
# is_error:true WITHOUT api_error_status -- the other of the two is_error
# shapes claude.go must handle (see bad_model_stderr.sh for the one WITH
# api_error_status). Both must surface stderr and the exit code.
cat <<'JSON'
{"type":"result","subtype":"success","is_error":true,"result":"Error: something went wrong internally","session_id":"deadbeef","total_cost_usd":0,"duration_ms":250}
JSON
echo "node:internal warning: deprecated flag encountered while resolving model" >&2
exit 1
