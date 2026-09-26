#!/usr/bin/env python3
"""Local deterministic model for real agent/tool lifecycle QA.

Run with --long-seconds 660, configure a test-only OpenAI-compatible model,
and send QA_LONG_PARENT: or QA_ROLL: through the real application UI.
The fixture never runs commands itself: Memoh dispatches the advertised tools.
"""
import argparse
import json
import math
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('--port', type=int, default=19089)
parser.add_argument('--long-seconds', type=int, default=660)
args = parser.parse_args()
lock = threading.Lock()
events = []


def record(**fields):
    event = {'at': time.time(), **fields}
    with lock:
        events.append(event)
    print(json.dumps(event), flush=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def do_GET(self):
        if self.path != '/qa/state':
            self.send_error(404)
            return
        with lock:
            data = json.dumps(events).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        request = json.loads(self.rfile.read(int(self.headers.get('Content-Length', '0'))))
        messages = request.get('messages', [])
        index = next((i for i in range(len(messages)-1, -1, -1)
                      if messages[i].get('role') == 'user'
                      and re.search(r'QA_(LONG_PARENT|LONG_CHILD|ROLL):', str(messages[i].get('content', '')))), 0)
        query = str(messages[index].get('content', '')) if messages else ''
        trailing = messages[index+1:]
        calls = [call.get('function', {}).get('name') for message in trailing
                 for call in message.get('tool_calls', [])]
        tool_results = [str(message.get('content', '')) for message in trailing if message.get('role') == 'tool']
        last_result = tool_results[-1] if tool_results else ''
        tool = None
        params = {}
        text = 'QA fixture response.'
        hold = False
        resumed = 'The server gracefully shut down' in query
        case = 'other'
        if request.get('stream'):
            if 'QA_LONG_CHILD:' in query:
                case = 'long-child'
                if not calls:
                    ticks = math.ceil(args.long_seconds / 30)
                    tool = 'exec'
                    params = {'command': "printf 'long-start\\n' >> /data/qa-long-task.log; "
                              f"i=0; while [ $i -lt {ticks} ]; do printf 'long-tick %s\\n' $i; "
                              "i=$((i+1)); sleep 30; done; printf 'long-done\\n' | tee -a /data/qa-long-task.log",
                              'run_in_background': True, 'max_duration_seconds': args.long_seconds + 240}
                elif re.search(r'"reason"\s*:\s*"completed"', last_result):
                    text = 'LONG_CHILD_COMPLETE: real command finished after the configured wall-clock duration.'
                else:
                    found = re.search(r'bg_[a-z0-9_]+', str(trailing))
                    if found:
                        tool = 'wait_until'
                        params = {'task_id': found.group(), 'timeout': 120, 'idle_timeout': 120}
                    else:
                        text = 'QA_LONG_FAILED: no background task identity.'
            elif 'QA_LONG_PARENT:' in query:
                case = 'long-parent'
                if not calls:
                    tool = 'spawn_agent'
                    params = {'id': 'long_worker', 'task': 'QA_LONG_CHILD: run the finite long command and wait for completion.', 'run_in_background': False}
                elif 'LONG_CHILD_COMPLETE' in str(tool_results):
                    text = 'LONG_PARENT_COMPLETE: child task exceeded ten minutes and completed successfully.'
                else:
                    text = 'QA_LONG_FAILED: child did not report successful completion.'
            elif 'QA_ROLL:' in query:
                case = 'roll'
                if resumed:
                    text = 'ROLL_RESUMED: the new Server continued the saved session without repeating its completed command.'
                elif not calls:
                    tool = 'exec'
                    marker = '/data/qa-roll-drain-once.log' if 'drain' in query else '/data/qa-roll-once.log'
                    params = {'command': f"printf 'roll-side-effect\\n' >> {marker}; cat {marker}"}
                else:
                    text = 'ROLL_READY: completed command saved; model step remains active until Server replacement.'
                    hold = True
        record(case=case, tool=tool, resumed=resumed, prior_tools=len(calls),
               saved_tool_result='roll-side-effect' in str(messages), hold=hold)
        if not request.get('stream'):
            data = json.dumps({'id': 'qa', 'object': 'chat.completion', 'model': 'qa-local',
                               'choices': [{'index': 0, 'message': {'role': 'assistant', 'content': 'Lifecycle QA'}, 'finish_reason': 'stop'}]}).encode()
            self.send_response(200)
            self.send_header('Content-Type', 'application/json')
            self.send_header('Content-Length', str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            return
        self.send_response(200)
        self.send_header('Content-Type', 'text/event-stream')
        self.end_headers()
        delta = {'role': 'assistant', 'content': text}
        finish = 'stop'
        if tool:
            delta = {'role': 'assistant', 'tool_calls': [{'index': 0, 'id': 'qa_' + str(len(calls)), 'type': 'function',
                                                        'function': {'name': tool, 'arguments': json.dumps(params)}}]}
            finish = 'tool_calls'
        try:
            self.send_chunk(delta, None)
            if hold:
                for _ in range(60):
                    time.sleep(15)
                    self.send_chunk({'content': ' .'}, None)
            self.send_chunk({}, finish)
            self.wfile.write(b'data: [DONE]\n\n')
            self.wfile.flush()
        except (BrokenPipeError, ConnectionResetError):
            record(case=case, disconnected=True)

    def send_chunk(self, delta, finish):
        data = {'id': 'qa', 'object': 'chat.completion.chunk', 'created': int(time.time()), 'model': 'qa-local',
                'choices': [{'index': 0, 'delta': delta, 'finish_reason': finish}]}
        self.wfile.write(('data: ' + json.dumps(data) + '\n\n').encode())
        self.wfile.flush()


ThreadingHTTPServer(('0.0.0.0', args.port), Handler).serve_forever()
