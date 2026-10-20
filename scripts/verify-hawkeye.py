import subprocess,socket,os,json,select
from pathlib import Path
script=str(Path(__file__).resolve().parents[1] / 'hawkeye-mcp-server.mjs')
for owned in (False,True):
 with socket.socket() as s:
  s.bind(('127.0.0.1',0));port=s.getsockname()[1]
 env=os.environ.copy()
 if owned:env['DEEPSENTRY_MCP_PARENT_PID']=str(os.getpid())
 else:env.pop('DEEPSENTRY_MCP_PARENT_PID',None)
 p=subprocess.Popen(['node',script,'--port',str(port)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=env)
 try:
  def request(i,method,params):
   p.stdin.write((json.dumps({'jsonrpc':'2.0','id':i,'method':method,'params':params})+'\n').encode());p.stdin.flush()
   assert select.select([p.stdout],[],[],5)[0], 'MCP response timeout'
   return json.loads(p.stdout.readline())
  init=request(1,'initialize',{'protocolVersion':'2024-11-05','capabilities':{},'clientInfo':{'name':'smoke','version':'1'}})
  assert init['result']['serverInfo']['version']=='1.0.12',init
  listing=request(2,'tools/list',{})['result']['tools'];assert len(listing)==51,len(listing)
  for name,field in [('browser_click','clickMode'),('browser_press_key','inputMode')]:
   tool=next(t for t in listing if t['name']==name)
   assert 'native' in tool['inputSchema']['properties'][field]['enum']
  p.stdin.close();assert p.wait(timeout=5)==0
  print('PASS MCP 1.0.12 handshake, 51 tools, native schemas, EOF exit; owned='+str(owned))
 finally:
  if p.poll() is None:p.terminate();p.wait(timeout=5)
