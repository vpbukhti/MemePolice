#!/bin/bash
cleanup() {
    echo "{\"msg\": \"wrapper process killed\"}" >> "/var/log/memepolice.log"
    if [[ -n "$CHILD_PID" ]]; then
        kill "$CHILD_PID" 2>/dev/null
        wait "$CHILD_PID" 2>/dev/null
    fi
    exit 0
}
trap cleanup SIGINT SIGTERM

source build_fpcalc.sh
export BOT_TOKEN=$(cat $HOME/secret.txt)
go build -o memepolice .

while true; do
    ./memepolice &>> "/var/log/memepolice.log" &
    CHILD_PID=$!
    wait $CHILD_PID
    EXIT_CODE=$?
    echo "{\"exit_code\": $EXIT_CODE}" >> "/var/log/memepolice.log"
    sleep 1
done