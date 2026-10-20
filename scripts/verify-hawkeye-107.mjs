#!/usr/bin/env node
// Exercise the real bundled server's handshake and public schema profiles.
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import net from 'node:net';
import readline from 'node:readline';
import { fileURLToPath } from 'node:url';

const serverFile = fileURLToPath(new URL('../hawkeye-mcp-server.mjs', import.meta.url));
async function freePort() {
  const listener = net.createServer();
  await new Promise(resolve => listener.listen(0, '127.0.0.1', resolve));
  const port = listener.address().port;
  await new Promise(resolve => listener.close(resolve));
  return port;
}
for (const [profile, caps] of [['full',''], ['core',''], ['research',''], ['core','codec,scripts']]) {
  const child = spawn(process.execPath, [serverFile,'--port',String(await freePort()),'--profile',profile,...(caps ? ['--caps',caps] : [])],
    {env:{...process.env,DEEPSENTRY_MCP_PARENT_PID:String(process.pid)},stdio:['pipe','pipe','pipe']});
  const pending = new Map(); let sequence=0;
  const lines = readline.createInterface({input:child.stdout});
  lines.on('line', line => { const response=JSON.parse(line); const wait=pending.get(response.id); if(wait){pending.delete(response.id);wait(response);} });
  child.stderr.resume();
  function request(method, params) {
    const id=++sequence;
    return new Promise((resolve,reject) => {
      const timer=setTimeout(()=>reject(new Error('MCP response timeout')),5000);
      pending.set(id, response=>{clearTimeout(timer);resolve(response);});
      child.stdin.write(JSON.stringify({jsonrpc:'2.0',id,method,params})+'\n');
    });
  }
  try {
    const init=await request('initialize',{protocolVersion:'2025-11-25',capabilities:{},clientInfo:{name:'deepsentry-contract-check',version:'2.0.6'}});
    assert.equal(init.result.serverInfo.version,'1.0.14');
    const result=await request('tools/list',{}); assert(result.result);
    const tools=new Map(result.result.tools.map(t=>[t.name,t]));
    assert(tools.has('browser_bookmarks'));
    if(profile==='full') {
      assert.equal(tools.size,53);
      assert(tools.has('hawkeye_script_manage'));
      const search=tools.get('browser_search').inputSchema.properties;
      assert.equal(search.maxFetches.maximum,7);
      assert(search.concurrency.anyOf);
      assert(tools.get('hawkeye_script_upsert').inputSchema.properties.expectedUpdatedAt);
      const codec=tools.get('hawkeye_codec').inputSchema.properties;
      for(const field of ['summary','includeMeta','maxResults','maxImages','offset','maxLength','image_data']) assert(codec[field]);
      const invalid=await request('tools/call',{name:'browser_find',arguments:{text:'test',context_budget_chars:12000}});
      assert(invalid.error || invalid.result?.isError);
    }
    if(caps) { assert(tools.has('hawkeye_codec'));assert(tools.has('hawkeye_script_manage'));assert(!tools.has('hawkeye_fuzz_run')); }
    if(profile==='core'&&!caps) { assert(!tools.has('hawkeye_codec'));assert(!tools.has('browser_search')); }
    for(const tool of tools.values()) { assert(tool.inputSchema.properties.page_token);assert(!tool.inputSchema.anyOf); }
    console.log(`PASS extension 1.0.7 / server 1.0.14: profile=${profile} caps=${caps||'-'} tools=${tools.size}`);
  } finally {
    child.stdin.end();
    await new Promise(resolve=> { const timer=setTimeout(()=>child.kill('SIGTERM'),2000);child.once('exit',()=>{clearTimeout(timer);resolve();}); });
  }
}
