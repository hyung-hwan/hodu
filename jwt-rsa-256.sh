#!/bin/bash

b64() { openssl base64 -e -A | tr '+/' '-_' | tr -d '='; }

usage() {
	echo "Usage: $0 generate private-key-file [ttl-seconds]" >&2
	exit 1
}

cmd="$1"
key_file="$2"
ttl="$3"

if [ "$cmd" != "generate" ]; then
	usage
fi

if [ -z "$key_file" ]; then
	usage
fi

if [ -z "$ttl" ]; then
	ttl=3600
fi

case "$ttl" in
''|*[!0-9]*)
	echo "invalid ttl-seconds: $ttl" >&2
	exit 1
	;;
esac

now=$(date +%s)
exp=$((now + ttl))

H=$(printf '{"alg":"RS256","typ":"JWT"}' | b64)
P=$(printf '{"sub":"123","iat":%s,"exp":%s}' "$now" "$exp" | b64)
D="$H.$P"
S=$(printf '%s' "$D" | openssl dgst -sha256 -sign "$key_file" | b64)

echo "$H.$P.$S"
