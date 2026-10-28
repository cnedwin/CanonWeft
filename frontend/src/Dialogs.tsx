import {useState} from 'react';
import {api, post, put} from './api';
import type {Project,Lore,Skill,Budget} from './types';

export function Modal({title,close,children,wide=false}:{title:string;close:()=>void;children:React.ReactNode;wide?:boolean}) {
  return <div className="overlay" onMouseDown={e=>{if(e.target===e.currentTarget) close()}}><section className={'modal '+(wide?'wide':'')} role="dialog" aria-modal="true" aria-label={title}><header><h2>{title}</h2><button onClick={close} aria-label="关闭窗口">×</button></header>{children}</section></div>;
}

export function CreateProject({done,close}:{done:(p:Project)=>void;close:()=>void}) {
  const [title,setTitle]=useState(''), [brief,setBrief]=useState(''), [error,setError]=useState(''), [busy,setBusy]=useState(false);
  return <Modal title="开始一本新小说" close={close}><form onSubmit={async e=>{e.preventDefault();setBusy(true);try{done(await post('/projects',{title,brief}))}catch(e){setError(String((e as Error).message))}finally{setBusy(false)}}}>
    <p className="muted">先告诉工作室你想写什么。人物、设定和第一章计划可以随后一起完善。</p>
    <label>书名<input autoFocus value={title} onChange={e=>setTitle(e.target.value)} placeholder="给故事一个暂定名字" maxLength={150} required/></label>
    <label>创作要求<textarea rows={7} value={brief} onChange={e=>setBrief(e.target.value)} placeholder="例如：写一部第三人称限知的悬疑小说。年轻修锁匠夜闯雨城，发现父亲留下的钥匙可以打开一扇不存在的门。第一章约 1800 字，先从行动进入故事。" required maxLength={30000}/></label>
    <label className="file">导入已有设定（TXT / Markdown）<input type="file" accept=".txt,.md" onChange={async e=>{const f=e.target.files?.[0];if(f){if(f.size>300000){setError('设定文件过大，请保留本书最相关的部分');return;}setBrief((await f.text()).slice(0,30000));}}}/></label>
    {error&&<p className="error" role="alert">{error}</p>}<footer><span className="muted">创建和保存项目免费，不调用模型。</span><button className="primary" disabled={busy}>创建小说</button></footer>
  </form></Modal>;
}

export function LoreEditor({project,done,close}:{project:Project;done:()=>void;close:()=>void}) {
  const [lore,setLore]=useState<Lore>(structuredClone(project.lore)),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const patchEntity=(index:number,key:string,value:unknown)=>setLore({...lore,entities:lore.entities.map((x,i)=>i===index?{...x,[key]:value}:x)});
  return <Modal title="设定与人物" wide close={close}><p className="muted">角色身份会被保留。修改设定后，已有章节会标为待复核，不会自动改写。</p>
    <label>创作简报<textarea rows={4} value={lore.brief} onChange={e=>setLore({...lore,brief:e.target.value})}/></label>
    <div className="section-heading"><h3>人物</h3><button onClick={()=>setLore({...lore,entities:[...lore.entities,{id:'char_'+crypto.randomUUID().slice(0,8),name:'',aliases:[],description:''}]})}>＋ 添加人物</button></div>
    {lore.entities.map((x,i)=><div className="edit-card" key={x.id}><label>姓名<input value={x.name} onChange={e=>patchEntity(i,'name',e.target.value)}/></label><label>别名（用逗号分隔）<input value={x.aliases.join('，')} onChange={e=>patchEntity(i,'aliases',e.target.value.split(/[,，]/).map(s=>s.trim()).filter(Boolean))}/></label><label>动机、性格与限制<textarea rows={3} value={x.description} onChange={e=>patchEntity(i,'description',e.target.value)}/></label><small className="muted">固定身份：{x.id}</small></div>)}
    {!lore.entities.length&&<p className="hint">还没有人物。你可以手工添加，或在首次生成时由设定师提出完整候选设定。</p>}
    <div className="section-heading"><h3>世界规则与秘密</h3><button onClick={()=>setLore({...lore,rules:[...lore.rules,{id:'rule_'+crypto.randomUUID().slice(0,8),text:'',known_by:[]}]})}>＋ 添加规则</button></div>
    {lore.rules.map((x,i)=><div className="edit-card" key={x.id}><label>规则内容<textarea rows={2} value={x.text} onChange={e=>setLore({...lore,rules:lore.rules.map((r,j)=>j===i?{...r,text:e.target.value}:r)})}/></label><p className="muted">谁知道这件事？未选择表示仅作者知道。</p><div className="chips">{lore.entities.map(c=><label className="check" key={c.id}><input type="checkbox" checked={x.known_by.includes(c.id)} onChange={e=>setLore({...lore,rules:lore.rules.map((r,j)=>j===i?{...r,known_by:e.target.checked?[...r.known_by,c.id]:r.known_by.filter(k=>k!==c.id)}:r)})}/>{c.name||'未命名'}</label>)}</div></div>)}
    {error&&<p className="error">{error}</p>}<footer><button onClick={close}>取消</button><button className="primary" disabled={busy} onClick={async()=>{setBusy(true);try{await put('/projects/'+project.id+'/lore',{lore,expected_revision:project.revision});done();close()}catch(e){setError((e as Error).message)}finally{setBusy(false)}}}>保存设定版本</button></footer>
  </Modal>;
}

export function SkillEditor({project,skills,close,done}:{project:Project;skills:Skill[];close:()=>void;done:()=>void}) {
  const [index,setIndex]=useState(0), [body,setBody]=useState(skills[0].instructions),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  const current=skills[index];
  return <Modal title="专业角色与 Skill" close={close} wide><div className="role-tabs">{skills.map((s,i)=><button key={s.role} className={index===i?'selected':''} onClick={()=>{setIndex(i);setBody(s.instructions);setError('')}}>{s.name}</button>)}</div>
    <p className="muted">{current.source==='bundled'?'内置预设':'本书自定义'} · 版本 {current.version} · {current.execution==='deterministic'?'本地规则执行':'独立模型上下文'}</p>
    <p className="hint">每个角色都有可执行的方法、例子和失败处理。编辑只影响本书新任务，进行中的任务使用原版本。预算和正史校验不会被说明文字绕过。</p>
    <label>工作方法<textarea className="skill-editor" rows={16} value={body} onChange={e=>setBody(e.target.value)}/></label>
    {current.validation&&<small>{current.validation}</small>}{error&&<p className="error">{error}</p>}
    <footer><small className="muted">自定义方法保存后仍需真实写作检验效果。</small><button className="primary" disabled={busy} onClick={async()=>{setBusy(true);try{await put(`/projects/${project.id}/skills/${current.role}`,{instructions:body,expected_hash:current.hash});done();close()}catch(e){setError((e as Error).message)}finally{setBusy(false)}}}>保存为本书新版本</button></footer>
  </Modal>;
}

export function BudgetDialog({budget,close,done}:{budget:Budget;close:()=>void;done:()=>void}) {
  const [enabled,setEnabled]=useState(budget.enabled),[total,setTotal]=useState(budget.total_cap),[cap,setCap]=useState(budget.task_cap),[key,setKey]=useState(''),[error,setError]=useState(''),[message,setMessage]=useState(''),[busy,setBusy]=useState(false);
  return <Modal title="模型与费用" close={close}><div className="budget-summary"><div><small>累计估算支出</small><b>¥ {budget.spent.toFixed(3)}</b></div><div><small>执行中 / 待核实</small><b>¥ {budget.reserved.toFixed(3)}</b></div></div>
    <p>默认模型：<strong>GPT-6.1 Sol</strong></p><p className="muted">Codex 开发订阅与本应用 API 费用分开。以下按标准价格、汇率 7.5 和 20% 余量估算，以服务商账单为准。每步先预留，再按返回用量结算；未知请求保持占用。</p>
    <label>API 密钥（{budget.key_ready?'已配置':'未配置'}）<input type="password" autoComplete="off" value={key} onChange={e=>setKey(e.target.value)} placeholder="仅在本机填写，不要发到聊天里"/></label>
    <button disabled={!key||busy} onClick={async()=>{setBusy(true);try{const r=await post('/key',{key});setMessage(r.message);setKey('');done()}catch(e){setError((e as Error).message)}finally{setBusy(false)}}}>仅保存密钥到本次运行</button>
    <div className="two-fields"><label>累计额度（元）<input type="number" min={0} max={1000} step="0.5" value={total} onChange={e=>setTotal(Number(e.target.value))}/></label><label>单次任务上限（元）<input type="number" min={0.1} max={10} step="0.1" value={cap} onChange={e=>setCap(Number(e.target.value))}/></label></div>
    <label className="check consent"><input type="checkbox" checked={enabled} onChange={e=>setEnabled(e.target.checked)}/>我同意在上述额度内进行付费模型调用</label>
    <p className="muted">开启不会立刻生成；点击“生成”或“审查”才会发起任务。单次任务最高 ¥10，累计最高 ¥1000。</p>
    {message&&<p className="success">{message}</p>}{error&&<p className="error">{error}</p>}
    <footer><button className="primary" disabled={busy} onClick={async()=>{setBusy(true);try{await post('/budget',{enabled,total_cap:total,task_cap:cap});done();close()}catch(e){setError((e as Error).message)}finally{setBusy(false)}}}>保存费用设置</button></footer>
    {!!budget.calls.length&&<details><summary>调用费用记录（{budget.calls.length}）</summary>{budget.calls.map((c,i)=><p className="muted" key={i}>{c.stage} · {c.state} · ¥{(c.actual??c.reserved).toFixed(4)} {c.response_id&&<small>{c.response_id}</small>}</p>)}</details>}
  </Modal>;
}

export function RestoreDialog({done,close}:{done:(p:Project)=>void;close:()=>void}) {
  const [file,setFile]=useState<File|null>(null),[migration,setMigration]=useState(false),[error,setError]=useState(''),[busy,setBusy]=useState(false);
  return <Modal title="恢复项目备份" close={close}><p>恢复为独立副本，当前项目会保留。旧备份中已被撤销的项目不能重新出现。</p><label>备份文件<input type="file" accept=".zip" onChange={e=>setFile(e.target.files?.[0]||null)}/></label><label className="check"><input type="checkbox" checked={migration} onChange={e=>setMigration(e.target.checked)}/>跨电脑迁移：我已核对来源电脑的删除与撤销记录</label>{error&&<p className="error">{error}</p>}<footer><button className="primary" disabled={!file||busy} onClick={async()=>{setBusy(true);try{const f=new FormData();f.append('file',file!);f.append('allow_migration',String(migration));done(await api('/restore',{method:'POST',body:f}));close()}catch(e){setError((e as Error).message)}finally{setBusy(false)}}}>校验并恢复副本</button></footer></Modal>;
}
