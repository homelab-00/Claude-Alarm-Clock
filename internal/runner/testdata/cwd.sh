#!/bin/sh
printf '{"type":"result","subtype":"success","is_error":false,"result":"%s","session_id":"x","total_cost_usd":0,"duration_ms":1}\n' "$(pwd)"
