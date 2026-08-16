#!/usr/bin/env bash
# Does the waiter flood a node that answers instantly with nothing?
#
# The discriminating case is a server that returns at once, no events, cursor
# unchanged - a stuck cursor, exactly what made the console send 145 requests a
# second. A node that blocks its window properly hides the bug completely, so
# testing against the real one proves nothing.
set -u
BIN=${1:?usage: spintest.sh <flowy binary>}
PORT=${PORT:-8801}
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"; pkill -f "[s]pinnode.py $PORT" 2>/dev/null' EXIT

cat >"$TMP/spinnode.py" <<'PY'
import http.server, json, sys, threading
count = [0]

class H(http.server.BaseHTTPRequestHandler):
    def _j(self, code, body):
        b = json.dumps(body).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_GET(self):
        # Answers AT ONCE, no events, cursor never moves: the stuck-cursor case.
        count[0] += 1
        self._j(200, {"events": [], "cursor": 5, "since": 5, "skipped": 0})

    def do_POST(self):
        self._j(200, {"reader": "t", "cursor": 5})

    def log_message(self, *a):
        pass

def dump():
    print(count[0], flush=True)
    threading.Timer(6, dump).start()

threading.Timer(6, dump).start()
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY

python3 "$TMP/spinnode.py" "$PORT" >"$TMP/count" 2>/dev/null &
sleep 1

FLOWY_TOKEN=t timeout 12 "$BIN" inbox --as t --deadline 10 --url "http://127.0.0.1:$PORT" >/dev/null 2>&1
waiter_rc=$?
sleep 1
requests=$(tail -1 "$TMP/count" 2>/dev/null || echo 0)
echo "requests in ~10s: $requests (waiter exit $waiter_rc)"

# THE COUNTER NEEDS A WITNESS THAT IT RAN. A bounded loop and a loop that never
# started produce the same small number, and only one of them is the fix - a
# binary that dies on a bad flag scores zero requests and passes. So: it must
# have polled at least twice, and it must have ended on the quiet deadline (1)
# rather than broken (2). Borrowed from flowy-claude, whose first browser
# measurement came back 0 because the app crashed before mount.
if [[ ${requests:-0} -lt 2 ]]; then
	echo "FAIL  the waiter never polled - this run measured nothing ($requests requests)"
	exit 1
fi
if [[ $waiter_rc != 1 ]]; then
	echo "FAIL  waiter exited $waiter_rc, wanted 1 (quiet deadline) - it did not run the loop under test"
	exit 1
fi
if [[ ${requests:-0} -gt 60 ]]; then
	echo "FAIL  the waiter floods a node that answers instantly ($requests requests)"
	exit 1
fi
echo "ok    bounded: $requests requests, and it demonstrably ran"
