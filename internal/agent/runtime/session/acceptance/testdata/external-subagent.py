import json
import os
import re
import subprocess
import sys
import threading
import time
import uuid
from pathlib import Path


artifact = Path(sys.argv[1])
output_lock = threading.Lock()
cancelled = threading.Event()
waiters = {}
answers = {}


def send(value):
    with output_lock:
        sys.stdout.write(json.dumps({"jsonrpc": "2.0", **value}) + "\n")
        sys.stdout.flush()


def record(event, **fields):
    with artifact.open("a") as output:
        output.write(json.dumps({"event": event, "pid": os.getpid(), "cwd": os.getcwd(), **fields}) + "\n")
        output.flush()
        os.fsync(output.fileno())


def update(session_id, text):
    send({"method": "session/update", "params": {"sessionId": session_id, "update": {
        "sessionUpdate": "agent_message_chunk", "content": {"type": "text", "text": text}
    }}})


def prompt(request):
    session_id = request["params"]["sessionId"]
    text = "\n".join(str(block.get("text", "")) for block in request["params"].get("prompt", []))
    matches = re.findall(r"external-subagent-action=(\w+)", text)
    action = matches[-1] if matches else "invalid"
    record("prompt", action=action, session_id=session_id, notice="Memoh has since stopped the processes it could find from this session" in text)
    update(session_id, "external-subagent-partial-" + action + "\n")
    if action == "hold_escape":
        escaped = subprocess.Popen(["sleep", "600"], start_new_session=True, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        record("escaped", session_id=session_id, escaped_pid=escaped.pid, thread=os.environ.get("MEMOH_SUBAGENT_THREAD", ""))
    if action in ("hold", "hold_escape"):
        record("partial_sent", session_id=session_id)
    if action == "failure":
        send({"id": request["id"], "error": {"code": -32603, "message": "controlled fixture failure after partial output"}})
        return
    if action == "approval":
        key = "permission-" + uuid.uuid4().hex
        waiters[key] = threading.Event()
        send({"id": key, "method": "session/request_permission", "params": {
            "sessionId": session_id,
            "toolCall": {"toolCallId": key, "title": "Run acceptance effect", "kind": "execute", "status": "pending", "rawInput": {"command": "printf acceptance-effect"}},
            "options": [{"optionId": "allow", "name": "Allow once", "kind": "allow_once"}, {"optionId": "deny", "name": "Reject once", "kind": "reject_once"}]
        }})
        deadline = time.monotonic() + 45
        while not waiters[key].wait(0.05) and not cancelled.is_set() and time.monotonic() < deadline:
            pass
        selected = answers.get(key, {}).get("outcome", {}).get("optionId")
        record("approval", selected=selected)
        if selected != "allow":
            send({"id": request["id"], "result": {"stopReason": "refusal"}})
            return
    if action in ("cancel", "hold", "hold_escape"):
        if cancelled.wait(30 if action == "cancel" else 120):
            time.sleep(0.5)
            record("cancel_settled", session_id=session_id)
            send({"id": request["id"], "result": {"stopReason": "cancelled"}})
            return
        record("unexpected_effect")
    previous = [json.loads(line) for line in artifact.read_text().splitlines()]
    count = sum(row["event"] == "complete" for row in previous) + 1
    record("complete", action=action, count=count)
    update(session_id, f"external-subagent-complete-{action} count={count}\n")
    send({"id": request["id"], "result": {"stopReason": "end_turn"}})


for line in sys.stdin:
    request = json.loads(line)
    method = request.get("method")
    if method == "initialize":
        send({"id": request["id"], "result": {"protocolVersion": 1, "agentCapabilities": {"loadSession": False}}})
    elif method == "session/new":
        send({"id": request["id"], "result": {"sessionId": "external-acceptance-" + uuid.uuid4().hex}})
    elif method == "session/prompt":
        cancelled.clear()
        threading.Thread(target=prompt, args=(request,), daemon=True).start()
    elif method == "session/cancel":
        record("cancel_received", session_id=request["params"]["sessionId"])
        cancelled.set()
    elif not method and request.get("id") in waiters:
        answers[request["id"]] = request.get("result", {})
        waiters[request["id"]].set()
    elif "id" in request:
        send({"id": request["id"], "error": {"code": -32601, "message": "unsupported fixture method"}})
