import test from 'node:test'
import assert from 'node:assert/strict'
import fs from 'node:fs/promises'
import os from 'node:os'
import path from 'node:path'
import {capture,adapters} from './capture-browser-quotas.mjs'

test('any number of accounts, providers and workspaces are independent; failed sessions retain old readings', async () => {
 const dir=await fs.mkdtemp(path.join(os.tmpdir(),'quota-registry-'))
 try {
  const providers=['openai','anthropic','xai','cursor','google']
  const readers=providers.map(provider=>({provider,matches:()=>true,read:async s=>({accountId:String(s),email:'same@example.com',windows:[{window:'week',usedPercent:s%100}]}),normalize:x=>x}))
  const bridge={cdp:{send:async()=>({targetInfos:Array.from({length:51},(_,i)=>({type:'page',url:'https://example.com',targetId:i%50}))})},attach:async id=>id,evaluate:()=>{}}
  assert.equal((await capture(bridge,dir,readers)).captured,250)
  const before=await fs.readdir(dir)
  assert.equal(before.length,250)
  const saved=await fs.readFile(path.join(dir,before[0]),'utf8')
  for(const r of readers) r.read=async()=>{throw new Error('expired')}
  assert.equal((await capture(bridge,dir,readers)).captured,0)
  assert.equal(await fs.readFile(path.join(dir,before[0]),'utf8'),saved)
 } finally {await fs.rm(dir,{recursive:true,force:true})}
})

test('Codex adapter preserves exhausted quota and exact reset, rejecting missing meters',()=>{
 const data={accountId:'personal',email:'apple-relay@example.com',plan:'pro',limit:{limit_reached:true,primary_window:{used_percent:100,limit_window_seconds:604800,reset_at:1794390285}}}
 const row=adapters[0].normalize(data)
 assert.equal(row.windows[0].usedPercent,100)
 assert.equal(row.windows[0].status,'full')
 assert.equal(row.windows[0].resetsAt,'2026-11-11T09:44:45.000Z')
 assert.equal(adapters[0].normalize({...data,limit:null}),null)
})
