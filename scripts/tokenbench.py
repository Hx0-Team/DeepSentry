#!/usr/bin/env python3
"""Real DeepSeek A/B billing against a loopback web/log lab (no simulated LLM)."""
import argparse
import hashlib
import json
import pathlib
import subprocess
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

parser = argparse.ArgumentParser()
parser.add_argument('--baseline', required=True)
parser.add_argument('--optimized', required=True)
credential = parser.add_mutually_exclusive_group(required=True)
credential.add_argument('--key-file')
credential.add_argument('--provider-json', help='private settings exported from an authorized config by cmd/tokenbench')
parser.add_argument('--baseline-label', default='2.0.5')
parser.add_argument('--optimized-label', default='2.0.6')
parser.add_argument('--baseline-description', default='2.0.5 commit e55d2a9')
parser.add_argument('--output', required=True)
parser.add_argument('--repetitions', type=int, default=3)
parser.add_argument('--document-smoke', action='store_true', help='optimized-only real-model document read/create check; excluded from A/B savings')
parser.add_argument('--document-source', help='QA fixture directory for document-smoke')
args = parser.parse_args()
output = pathlib.Path(args.output).resolve()
output.mkdir(parents=True, exist_ok=True)
output.chmod(0o700)
settings = json.loads(pathlib.Path(args.provider_json).read_text()) if args.provider_json else {}
key = settings.get('api_key') if args.provider_json else pathlib.Path(args.key_file).read_text().strip()
if not key: raise SystemExit('authorized config has no API credential')
upstream = settings.get('api_url', 'https://api.deepseek.com/chat/completions')
model = settings.get('model_name', 'deepseek-flash')
proxy_url = settings.get('controller_proxy', '')
if proxy_url.startswith('socks'): raise SystemExit('benchmark proxy does not support SOCKS; source config unchanged')
opener = urllib.request.build_opener(urllib.request.ProxyHandler({'http': proxy_url, 'https': proxy_url} if proxy_url else {}))
records, runs, current = [], [], {}
lock = threading.Lock()

class Proxy(BaseHTTPRequestHandler):
    def log_message(self, *unused): pass
    def do_POST(self):
        payload = self.rfile.read(int(self.headers['Content-Length']))
        request = urllib.request.Request(upstream, data=payload,
            headers={'Authorization': 'Bearer ' + key, 'Content-Type': 'application/json'})
        started = time.monotonic()
        try:
            with opener.open(request, timeout=180) as response:
                body, status, content_type = response.read(), response.status, response.headers.get('Content-Type', 'application/json')
                retry_headers = {h: response.headers[h] for h in ['Retry-After', 'Retry-After-Ms', 'X-Request-Id'] if response.headers.get(h)}
        except urllib.error.HTTPError as error:
            body, status, content_type = error.read(), error.code, 'application/json'
            retry_headers = {h: error.headers[h] for h in ['Retry-After', 'Retry-After-Ms', 'X-Request-Id'] if error.headers.get(h)}
        except Exception as error:
            body, status, content_type = json.dumps({'error': {'message': str(error)}}).encode(), 502, 'application/json'
            retry_headers = {}
        usages = []
        if 'text/event-stream' in content_type:
            for line in body.splitlines():
                if line.startswith(b'data: ') and line != b'data: [DONE]':
                    data = json.loads(line[6:])
                    if data.get('usage'): usages.append(data['usage'])
        else:
            try: data = json.loads(body)
            except (ValueError, UnicodeError): data = {}
            if data.get('usage'): usages.append(data['usage'])
        with lock:
            records.append(dict(current, status=status, seconds=round(time.monotonic()-started, 3),
                usage=usages[-1] if usages else None, request_bytes=len(payload),
                model=json.loads(payload).get('model'),
                tools_sha256=hashlib.sha256(json.dumps(json.loads(payload).get('tools', []), ensure_ascii=False, separators=(',', ':')).encode()).hexdigest(),
                messages=len(json.loads(payload).get('messages', [])),
                error=None if status == 200 else body.decode(errors='replace').replace(key, '[REDACTED]')[:500]))
            (output/'provider-usage.json').write_text(json.dumps(records, ensure_ascii=False, indent=2))
        self.send_response(status)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body)))
        for name, value in retry_headers.items(): self.send_header(name, value)
        self.end_headers()
        self.wfile.write(body)

class Lab(BaseHTTPRequestHandler):
    def log_message(self, *unused): pass
    def do_GET(self):
        pages = {
            '/': ('text/html', '<html><title>Local test service</title><a href="/debug">Diagnostics</a><a href="/backup/config.json">Backup</a></html>'),
            '/debug': ('application/json', json.dumps({'debug': True, 'service': 'lab-web', 'internal_path': '/srv/lab-web/app', 'build': 'test-20261013'})),
            '/backup/config.json': ('application/json', json.dumps({'environment': 'test', 'database': {'host': 'db.lab.invalid', 'user': 'lab_admin', 'password': 'NOT-A-REAL-SECRET'}})),
            '/health': ('application/json', '{"ok":true}'),
        }
        content_type, body = pages.get(self.path, ('text/plain', 'not found'))
        self.send_response(200 if self.path in pages else 404)
        self.send_header('Content-Type', content_type)
        self.send_header('Content-Length', str(len(body.encode())))
        self.end_headers()
        self.wfile.write(body.encode())

def start(handler):
    server = ThreadingHTTPServer(('127.0.0.1', 0), handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server

proxy, lab = start(Proxy), start(Lab)
lab_root = output/'lab'
lab_root.mkdir(exist_ok=True)
log = lab_root/'auth.log'
lines = [f'2026-10-12T10:00:{i%60:02d}+08:00 host sshd[10]: Failed password for invalid user guest from 192.0.2.17 port {42000+i} ssh2' for i in range(42)]
lines += ['2026-10-12T10:01:10+08:00 host sshd[11]: Accepted password for deploy from 192.0.2.17 port 43000 ssh2']
lines += [f'2026-10-12T10:02:{i%60:02d}+08:00 host cron[20]: normal health check job id={i}' for i in range(1500)]
log.write_text('\n'.join(lines)+'\n')
tasks = {
    'web': f'对本地 HTTP 靶场 http://127.0.0.1:{lab.server_port} 做只读安全检查，核验 /debug 与 /backup/config.json 是否公开暴露，给出实际 HTTP 状态与泄露内容类别、风险和建议。只访问这个 loopback 服务，不修改文件，不启动子 Agent；不要输出密码明文。完成后 finish。',
    'log': f'只读分析本地靶场日志 {log}。限定 2026-10-12 10:00 至 10:03（+08:00），找出 SSH 失败登录来源 IP 和准确次数，并关联随后的成功登录账号与时间。给出实际日志证据和结论，不修改文件，不启动子 Agent；完成后 finish。',
}
if args.document_smoke:
    import shutil
    source = pathlib.Path(args.document_source).resolve()
    for fmt in ['docx','xlsx','pptx','pdf']:
        shutil.copyfile(source/f'sample.{fmt}', lab_root/f'input.{fmt}')
    tasks = {'documents': f'文档能力验收：先用 document_parse 阅读 {lab_root}/input.docx、input.xlsx、input.pptx、input.pdf（后3个同目录），核验标题、资产、Excel合计公式与PPT演示备注。以附件中已核验的数据生成 {lab_root}/output.docx、output.xlsx、output.pptx、output.pdf：标题为月度安全报告，Word/PDF包含总体情况、风险建议和资产表，Excel至少2个工作表并保留SUM(B2:B3)公式与缓存3，PPT至少2页并包含备注。用 document_create 的spec结构化参数，不能执行Shell或自行写XML/Python，禁止安装依赖，不启动子Agent。每个文件只新建一次，检查工具回读警告后给出准确路径及阅读结论并finish。测试目录中新建这四份文件已获授权。'}
    args.repetitions = 1
config = output/'config.yaml'
config.write_text(f'''provider: {json.dumps(settings.get('provider', 'deepseek'))}
api_protocol: openai_chat
api_url: http://127.0.0.1:{proxy.server_port}
api_key: none
model_name: {json.dumps(model)}
model_profile: {json.dumps(settings.get('model_profile') or 'full')}
context_window_tokens: {settings.get('context_window_tokens', 1000000)}
reserved_output_tokens: {settings.get('reserved_output_tokens', 4096)}
native_tool_limit: {settings.get('native_tool_limit', 16)}
agent_runtime: v3
temperature: 0
use_native_tools: true
skills_disabled: true
trace_enabled: false
target_protocol: local
llm_timeout_sec: 240
llm_retries: 0
execution_budget:
  enabled: false
max_steps: 10
''')
config.chmod(0o600)
try:
    for repetition in range(args.repetitions):
        for case, task in tasks.items():
            order = [(args.baseline_label, args.baseline), (args.optimized_label, args.optimized)]
            if args.document_smoke: order = [(args.optimized_label, args.optimized)]
            elif repetition % 2: order.reverse()
            for version, binary in order:
                current.clear(); current.update(version=version, case=case, repetition=repetition+1)
                run_root = lab_root/f'{version}-{case}-{repetition+1}'
                run_root.mkdir(exist_ok=True)
                result = output/f'{version}-{case}-{repetition+1}.json'
                started, before = time.monotonic(), len(records)
                proc = subprocess.run([str(pathlib.Path(binary).resolve()), '-c', str(config), '-lab', str(run_root), '-task', task, '-out', str(result)], cwd=lab_root, capture_output=True, timeout=600)
                (output/f'{version}-{case}-{repetition+1}.log').write_bytes(proc.stdout+proc.stderr)
                data = json.loads(result.read_text()) if result.exists() else {}
                report = next((m['content'] for m in reversed(data.get('history', [])) if m.get('role') == 'assistant' and 'final_report' in m.get('content','')), '')
                expected = ['192.0.2.17', '42', 'deploy', '10:01:10'] if case == 'log' else ['/debug', '/backup/config.json']
                if args.document_smoke:
                    expected = ['output.docx','output.xlsx','output.pptx','output.pdf']
                run = dict(current, exit_code=proc.returncode, seconds=round(time.monotonic()-started,3), result=data.get('result'), evidence_pass=all(s in report for s in expected), calls=len(records)-before,
                    usage={k:sum((r['usage'] or {}).get(k,0) for r in records[before:]) for k in ['prompt_tokens','completion_tokens','total_tokens','prompt_cache_hit_tokens','prompt_cache_miss_tokens']})
                if args.document_smoke:
                    run['files_created'] = {fmt:(lab_root/f'output.{fmt}').is_file() for fmt in ['docx','xlsx','pptx','pdf']}
                    run['evidence_pass'] = run['evidence_pass'] and all(run['files_created'].values())
                runs.append(run)
                (output/'runs.json').write_text(json.dumps(runs,ensure_ascii=False,indent=2))
                print(json.dumps(run,ensure_ascii=False),flush=True)
    totals = {v:{k:sum(r['usage'][k] for r in runs if r['version']==v) for k in runs[0]['usage']} for v in [args.baseline_label, args.optimized_label]}
    final = {'mode':'document-smoke' if args.document_smoke else 'ab','model':model,'repetitions':args.repetitions,'baseline_description':args.baseline_description,'runs':runs,'totals':totals,
        'savings_percent':{k:round(100*(1-totals[args.optimized_label][k]/totals[args.baseline_label][k]),2) if totals[args.baseline_label][k] else None for k in totals[args.baseline_label]},
        'all_usage_returned':all(r['usage'] for r in records),'all_evidence_pass':all(r['evidence_pass'] for r in runs)}
    (output/'summary.json').write_text(json.dumps(final,ensure_ascii=False,indent=2))
    print(json.dumps({'totals':totals,'savings_percent':final['savings_percent'],'all_evidence_pass':final['all_evidence_pass']},ensure_ascii=False),flush=True)
finally:
    proxy.shutdown(); lab.shutdown()
