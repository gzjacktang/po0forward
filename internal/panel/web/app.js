'use strict';
const $ = id => document.getElementById(id);
let csrf = '', view = {nodes: [], rules: [], audit: []}, enrollment = null, pendingRuleAction = null, toastTimer;
const esc = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function toast(text) { $('toast').textContent=text; $('toast').hidden=false; clearTimeout(toastTimer); toastTimer=setTimeout(()=>$('toast').hidden=true,4500); }
function loggedIn(ok) { $('login').hidden=ok; $('workspace').hidden=!ok; if(!ok) {csrf=''; $('password').value='';} }
async function api(path, body) {
 const options={method:body===undefined?'GET':'POST',headers:{'Content-Type':'application/json','X-CSRF-Token':csrf},credentials:'same-origin'};
 if(body!==undefined) options.body=JSON.stringify(body);
 const response=await fetch('/api/'+path,options);const data=await response.json();
 if(!response.ok) {if(response.status===401&&path!=='login') loggedIn(false);throw new Error(data.error||'操作失败');}return data;
}
function nodeStatus(n) {if(!n.last_seen)return ['offline','等待接入'];if(!n.online)return ['offline','离线'];if(n.error)return ['failure','应用异常'];if(n.applied_revision!==n.revision)return ['pending','等待应用'];return ['online','在线 · 已同步'];}
function nodeName(id){return view.nodes.find(n=>n.id===id)?.name||'未知节点';}
function renderNodes(){
 $('total-nodes').textContent=view.nodes.length;$('node-count').textContent=view.nodes.length;$('online-nodes').textContent=view.nodes.filter(n=>n.online).length;$('total-rules').textContent=view.rules.length;
 $('pending-nodes').textContent=view.nodes.filter(n=>n.applied_revision!==n.revision||n.error).length;
 $('nodes').innerHTML=view.nodes.length?view.nodes.map(n=>{const [style,label]=nodeStatus(n);const rules=view.rules.filter(r=>r.node_id===n.id);return `<article class="node-card"><div class="node-top"><div class="node-title"><span class="node-icon">▤</span><div><strong>${esc(n.name)}</strong><small>${esc(n.id)}</small></div></div><span class="badge ${style}">${label}</span></div><div class="node-detail"><div><span>转发规则</span><strong>${rules.filter(r=>r.enabled).length} 启用 / ${rules.length} 条</strong></div><div><span>配置版本</span><strong>${n.applied_revision} / ${n.revision}</strong></div><div><span>最近响应</span><strong>${n.last_seen?new Date(n.last_seen*1000).toLocaleTimeString('zh-CN',{hour12:false}):'尚未连接'}</strong></div></div>${n.error?`<div class="node-error">${esc(n.error)}</div>`:''}</article>`;}).join(''):'<div class="empty">从添加第一台节点开始。系统会生成这台机器专属的接入配置。</div>';
 const selected=$('filter-node').value;$('filter-node').innerHTML='<option value="">全部节点</option>'+view.nodes.map(n=>`<option value="${n.id}">${esc(n.name)}</option>`).join('');$('filter-node').value=view.nodes.some(n=>n.id===selected)?selected:'';
 $('add-rule').disabled=!view.nodes.length;
}
function renderRules(){
 const query=$('search').value.trim().toLowerCase(),node=$('filter-node').value;
 const rules=view.rules.filter(r=>(!node||r.node_id===node)&&`${r.name} ${nodeName(r.node_id)} ${r.target_ip} ${r.listen_port} ${r.target_port} ${r.protocol}`.toLowerCase().includes(query));
 $('rule-count').textContent=rules.length+' 条规则';
 $('rules').innerHTML=rules.length?rules.map(r=>{const n=view.nodes.find(n=>n.id===r.node_id),pending=n&&n.applied_revision!==n.revision;return `<tr><td><span class="rule-title">${esc(r.name)}</span><span class="rule-node">${esc(nodeName(r.node_id))}</span></td><td><span class="protocol">${r.protocol==='both'?'TCP + UDP':r.protocol.toUpperCase()}</span></td><td class="mono">${r.listen_port}</td><td class="mono">${esc(r.target_ip)}:${r.target_port}</td><td><span class="badge ${pending?'pending':r.enabled?'enabled':'disabled'}">${pending?'等待应用':r.enabled?'启用':'停用'}</span></td><td class="right"><button class="operation" data-rule="${r.id}" data-action="${r.enabled?'disable':'enable'}">${r.enabled?'停用':'启用'}</button><button class="operation danger" data-rule="${r.id}" data-action="delete">删除</button></td></tr>`;}).join(''):'<tr><td colspan="6" class="empty-cell">没有匹配的规则。点击“新增规则”创建一条转发路径。</td></tr>';
}
function renderAudit(){const labels={'node.create':'添加节点','rule.create':'新增规则','rule.enable':'启用规则','rule.disable':'停用规则','rule.delete':'删除规则'};const rows=[...view.audit].reverse().slice(0,8);$('audits').innerHTML=rows.length?rows.map(a=>`<div class="audit-row"><span class="audit-time">${new Date(a.at*1000).toLocaleString('zh-CN',{month:'2-digit',day:'2-digit',hour:'2-digit',minute:'2-digit',hour12:false})}</span><span class="audit-action">${esc(labels[a.action]||a.action)}</span><span>${esc(a.detail)}</span><span class="audit-actor">${esc(a.actor)}</span></div>`).join(''):'<div class="empty-cell">操作记录将在这里显示。</div>';}
async function refresh(){if(!csrf)return;try{view=await api('state');renderNodes();renderRules();renderAudit();$('sync-status').textContent='● 状态已同步';$('last-refresh').textContent='更新于 '+new Date().toLocaleTimeString('zh-CN',{hour12:false});}catch(error){$('sync-status').textContent='同步失败';toast(error.message);}}
$('login-form').addEventListener('submit',async event=>{event.preventDefault();const button=event.target.querySelector('button');button.disabled=true;try{const result=await api('login',{password:$('password').value});csrf=result.csrf;$('password').value='';$('login-error').textContent='';loggedIn(true);await refresh();}catch(error){$('login-error').textContent=error.message;}finally{button.disabled=false;}});
$('logout').addEventListener('click',async()=>{try{await api('logout',{});}finally{loggedIn(false);}});
$('refresh').addEventListener('click',refresh);$('search').addEventListener('input',renderRules);$('filter-node').addEventListener('change',renderRules);
document.querySelectorAll('[data-close]').forEach(button=>button.addEventListener('click',() => $(button.dataset.close).close()));
$('add-node').addEventListener('click',()=>{$('node-form').reset();$('node-dialog').showModal();});
$('node-form').addEventListener('submit',async event=>{event.preventDefault();try{const result=await api('nodes',{name:new FormData(event.target).get('name')});enrollment=result.agent_config;$('enroll-proxy').value='';updateEnrollment();$('node-dialog').close();$('enroll-dialog').showModal();await refresh();}catch(error){toast(error.message);}});
function updateEnrollment(){if(!enrollment)return;enrollment.proxy_url=$('enroll-proxy').value;$('enroll-config').value=JSON.stringify(enrollment,null,2);}
$('enroll-proxy').addEventListener('input',updateEnrollment);
$('copy-config').addEventListener('click',async()=>{try{await navigator.clipboard.writeText($('enroll-config').value);toast('已复制，请保存到节点的 agent.json');}catch{$('enroll-config').select();toast('浏览器未允许自动复制，请手动复制已选中的内容');}});
$('download-config').addEventListener('click',()=>{const blob=new Blob([$('enroll-config').value+'\n'],{type:'application/json'}),url=URL.createObjectURL(blob),a=document.createElement('a');a.href=url;a.download='agent.json';a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);});
$('enroll-dialog').addEventListener('close',()=>{enrollment=null;$('enroll-config').value='';$('enroll-proxy').value='';});
$('add-rule').addEventListener('click',()=>{$('rule-form').reset();$('rule-node').innerHTML=view.nodes.map(n=>`<option value="${n.id}">${esc(n.name)}</option>`).join('');if($('filter-node').value)$('rule-node').value=$('filter-node').value;$('rule-dialog').showModal();});
$('rule-form').addEventListener('submit',async event=>{event.preventDefault();const fields=Object.fromEntries(new FormData(event.target));fields.listen_port=Number(fields.listen_port);fields.target_port=Number(fields.target_port);fields.enabled=true;try{await api('rules',fields);$('rule-dialog').close();toast('规则已保存，等待节点应用');await refresh();}catch(error){toast(error.message);}});
$('rules').addEventListener('click',event=>{
 const button=event.target.closest('[data-rule]');if(!button)return;
 const rule=view.rules.find(r=>r.id===button.dataset.rule);if(!rule)return;
 const action=button.dataset.action,label={enable:'启用',disable:'停用',delete:'删除'}[action];
 pendingRuleAction={id:rule.id,action};
 $('confirm-title').textContent='确认'+label+'规则';
 $('confirm-summary').textContent=`${rule.name}\n节点：${nodeName(rule.node_id)}\n${rule.protocol.toUpperCase()} ${rule.listen_port} → ${rule.target_ip}:${rule.target_port}`;
 $('confirm-note').textContent=action==='enable'?'保存后等待节点应用。':'生效后阻止新的转发连接，已有连接可能继续至结束。';
 $('confirm-submit').textContent='确认'+label;$('confirm-dialog').showModal();
});
$('confirm-dialog').addEventListener('close',()=>{pendingRuleAction=null;});
$('confirm-form').addEventListener('submit',async event=>{
 event.preventDefault();if(!pendingRuleAction)return;
 const {id,action}=pendingRuleAction,button=$('confirm-submit');button.disabled=true;
 try{await api(`rules/${id}/${action}`,{});$('confirm-dialog').close();toast('操作已保存，等待节点应用');await refresh();}
 catch(error){toast(error.message);}finally{button.disabled=false;}
});
(async()=>{try{const result=await api('session');csrf=result.csrf;loggedIn(true);await refresh();}catch{loggedIn(false);}})();
setInterval(refresh,5000);
