// Optional browser-session adapter runner. The shared account registry is provider
// and sign-in-method agnostic; each adapter verifies its own provider response.
// Run through the existing persistent-CDP bridge. No connection or login is opened.
import fs from 'node:fs/promises'
import path from 'node:path'
import crypto from 'node:crypto'

export const adapters = [{
  provider: 'openai',
  matches: url => url.origin === 'https://chatgpt.com' && url.pathname.startsWith('/settings/usage'),
  read: async (session, evaluate) => evaluate(session, `(async () => {
    const auth = await fetch('/api/auth/session').then(r => r.ok ? r.json() : null);
    if (!auth?.accessToken || !auth.user?.email || !auth.account?.id) return null;
    const r = await fetch('/backend-api/wham/usage', {headers:{Authorization:'Bearer '+auth.accessToken,'ChatGPT-Account-Id':auth.account.id}});
    if (!r.ok) return null;
    const d = await r.json();
    if (d.account_id !== auth.account.id || d.email?.toLowerCase() !== auth.user.email.toLowerCase()) return null;
    return {accountId:d.account_id,email:d.email,plan:d.plan_type,limit:d.rate_limit};
  })()`),
  normalize(data) {
    if (!data?.accountId || !data.email) return null
    const windows = [data.limit?.primary_window,data.limit?.secondary_window].flatMap(w => {
      if (!w || !Number.isFinite(w.used_percent) || w.used_percent<0 || w.used_percent>100 ||
        !Number.isInteger(w.limit_window_seconds) || w.limit_window_seconds<=0 || !Number.isInteger(w.reset_at) || w.reset_at<=0) return []
      const window = w.limit_window_seconds>=86400 ? 'week' : 'session'
      return [{window,usedPercent:w.used_percent,resetsAt:new Date(w.reset_at*1000).toISOString(),status:data.limit.limit_reached && w.used_percent>=100 ? 'full':'ok'}]
    })
    if (!windows.length) return null
    return {accountId:data.accountId,email:data.email,plan:data.plan,windows}
  },
}]

export async function capture({cdp,attach,evaluate}, directory, readers=adapters) {
  const {targetInfos} = await cdp.send('Target.getTargets')
  const written = new Set()
  const failures = []
  for (const tab of targetInfos.filter(t=>t.type==='page')) {
    for (const adapter of readers) {
      let url
      try { url=new URL(tab.url) } catch { continue }
      if (!adapter.matches(url)) continue
      try {
        const row=adapter.normalize(await adapter.read(await attach(tab.targetId),evaluate))
        if (!row?.accountId || !row.windows?.length) continue
        const key=JSON.stringify([adapter.provider,row.accountId,row.orgId||''])
        if (written.has(key)) continue
        const record={...row,schema:1,provider:adapter.provider,observedAt:new Date().toISOString()}
        const file=path.join(directory,crypto.createHash('sha256').update(key).digest('hex')+'.json')
        await fs.mkdir(directory,{recursive:true,mode:0o700})
        await fs.writeFile(file+'.tmp',JSON.stringify(record),{mode:0o600})
        await fs.rename(file+'.tmp',file)
        written.add(key)
      } catch (error) { failures.push({provider:adapter.provider,error:error?.code || error?.name || 'read-failed'}) }
    }
  }
  return {captured:written.size,failures}
}

export async function run(bridge) {
  const home=process.env.TOKENMAXR_HOME || path.join(process.env.LOCALAPPDATA,'tokenmaxr')
  return capture(bridge,path.join(home,'quota/accounts'))
}
