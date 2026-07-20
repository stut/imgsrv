#!/bin/sh
# PID 1 for the imgsrv container: run imgsrv and nginx together, forward
# TERM/INT to both, and exit when either one dies so the orchestrator
# restarts the pair as a unit.
set -u

imgsrv &
imgsrv_pid=$!

nginx -g 'daemon off;' &
nginx_pid=$!

stopping=
trap 'stopping=1; kill -TERM "$imgsrv_pid" "$nginx_pid" 2>/dev/null' TERM INT

# The backgrounded sleep keeps the trap responsive: a signal interrupts
# `wait`, but not a foreground `sleep`.
while kill -0 "$imgsrv_pid" 2>/dev/null && kill -0 "$nginx_pid" 2>/dev/null; do
    sleep 1 &
    wait $! 2>/dev/null
done

if [ -z "$stopping" ]; then
    echo "entrypoint: child process exited unexpectedly, shutting down" >&2
fi
kill -TERM "$imgsrv_pid" "$nginx_pid" 2>/dev/null
wait

[ -n "$stopping" ] && exit 0 || exit 1
