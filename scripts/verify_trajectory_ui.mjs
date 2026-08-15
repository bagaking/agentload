// Browser acceptance consumes the embedded production build and synthetic
// native records through the actual Go service. Browser identity is host-local.
import { spawn, spawnSync } from 'node:child_process';
import { readFileSync, writeFileSync, existsSync, mkdirSync, rmSync, readdirSync, unlinkSync } from 'node:fs';
import { resolve, join } from 'node:path';
import { createHash } from 'node:crypto';

const root=resolve(import.meta.dirname,'..');
const artifacts=join(root,'.bagakit/feature-tracker/features/f-22duuagpj/artifacts');
const configPath=process.env.AGENTLOAD_UI_BROWSER_CONFIG || join(artifacts,'ui-browser.json');
if (!existsSync(configPath)) throw Error('UI acceptance needs a host-local selected browser receipt.');
const browserConfig=JSON.parse(readFileSync(configPath,'utf8'));
const {browser_id,provider,space_id}=browserConfig;
const cli=provider==='ego' ? (process.env.AGENTLOAD_EGO_BROWSER_CLI || 'ego-browser') : process.env.AGENTMUX_CLI;
if (!cli || (provider==='ego' ? !Number.isSafeInteger(space_id) : !browser_id)) throw Error('Selected browser capability unavailable.');
mkdirSync(artifacts,{recursive:true});
const fixtureRoot=join(artifacts,'ui-fixture');
rmSync(fixtureRoot,{force:true,recursive:true});
mkdirSync(fixtureRoot,{recursive:true,mode:0o700});
const instanceFile=join(fixtureRoot,'browser-instance.json');
rmSync(instanceFile,{force:true});rmSync(instanceFile+'.stop',{force:true});
const run=(command,args)=>{
 const r=spawnSync(command,args,{cwd:root,encoding:'utf8',maxBuffer:4*1024*1024});
 if(r.status!==0) throw Error(`${command} failed: ${r.stderr || r.stdout}`);
 return r.stdout;
};
run('npm',['--prefix','ui','run','build']);
const binary=join(fixtureRoot,'agentload');
run('go',['build','-o',binary,'.']);
let output='';
const fixture=spawn('go',['test','.','-run','^TestTrajectoryUIFixture$','-count=1','-timeout','5m'],{cwd:root,env:{...process.env,AGENTLOAD_TRAJECTORY_TEST_INSTANCE_FILE:instanceFile,AGENTLOAD_TRAJECTORY_ACCEPTANCE_ROOT:fixtureRoot}});
fixture.stdout.on('data',b=>{output+=b});fixture.stderr.on('data',b=>{output+=b});
const delay=ms=>new Promise(r=>setTimeout(r,ms));
const browser=program=>{
 if(provider==='ego') {
  // Use one existing TaskSpace. All controls are discovered through the
  // current AX tree, then handled by the browser's documented Page API.
  const source=`
const task=await taskSpace(${space_id});const page=task.page('p1');
const cdp=(method,params)=>page.cdp(method,params);
const js=code=>page.evaluate(code);
const wait=ms=>new Promise(resolve=>setTimeout(resolve,ms));
const gotoUrl=url=>page.goto(url);
const snapshot=async()=>{const frames=await js('document.querySelectorAll("iframe").length');if(frames)throw Error('UI acceptance cannot claim complete embedded-frame observation');const tree=await cdp('Accessibility.getFullAXTree',{});return {missingFrames:[],nodes:tree.nodes.filter(n=>!n.ignored).map(n=>({role:['textbox','searchbox'].includes(n.role?.value)?'text input':n.role?.value,name:n.name?.value??'',ref:n.backendDOMNodeId,backendNodeId:n.backendDOMNodeId}))};};
async function control(ref){if(!Number.isSafeInteger(ref)||ref<=0)throw Error('Unobserved browser control');const resolved=await cdp('DOM.resolveNode',{backendNodeId:ref});const tagged=await cdp('Runtime.callFunctionOn',{objectId:resolved.object.objectId,functionDeclaration:'function(){if(!this.isConnected)throw Error("Observed control replaced");this.setAttribute("data-agentload-qa-ref",'+JSON.stringify(String(ref))+')}',returnByValue:true});if(tagged.exceptionDetails)throw Error('Observed browser control unavailable');return '[data-agentload-qa-ref="'+ref+'"]';}
const click=async ref=>page.click(await control(ref),{label:'Check observed trajectory control'});
const fillInput=async(ref,value)=>page.fill(await control(ref),value);
const pressKey=async(ref,key)=>page.press(await control(ref),key);
try {const result=await(async()=>{${program}})();console.log('AGENTLOAD_BROWSER_RESULT:'+JSON.stringify({ok:true,result}));}
catch(error){console.log('AGENTLOAD_BROWSER_RESULT:'+JSON.stringify({ok:false,error:String(error.stack??error)}));process.exitCode=1;}
`;
  const r=spawnSync(cli,['nodejs'],{input:source,encoding:'utf8',maxBuffer:8*1024*1024});
  const line=(r.stdout+'\n'+r.stderr).split('\n').find(line=>line.startsWith('AGENTLOAD_BROWSER_RESULT:'));
  if(!line)throw Error('Ego browser receipt unavailable: '+r.stderr);
  const receipt=JSON.parse(line.slice('AGENTLOAD_BROWSER_RESULT:'.length));
  if(!receipt.ok)throw Error(receipt.error);
  return receipt.result;
 }
 const r=spawnSync(cli,['browser','run','--browser',browser_id],{input:program,encoding:'utf8',maxBuffer:8*1024*1024});
 let receipt;try{receipt=JSON.parse(r.stdout)}catch{throw Error('Browser receipt unavailable: '+r.stderr)}
 if(!receipt.ok || receipt.result?.outcome?.kind!=='completed') throw Error('Browser operation did not complete: '+JSON.stringify(receipt.result?.outcome || receipt.error));
 return receipt.result.result;
};
const helpers=`
async function observe(find){await cdp('Emulation.setFocusEmulationEnabled',{enabled:true});for(let i=0;i<60;i++){const p=await snapshot();if(p.missingFrames.length)throw Error('Incomplete accessibility map');const n=p.nodes.find(find);if(n)return n;await wait(100);}throw Error('Expected UI element missing');}
async function tab(name){const n=await observe(n=>n.role==='tab'&&n.name===name);await click(n.ref);}
async function query(value){const input=await observe(n=>n.role==='text input');await fillInput(input.ref,value);const submit=await observe(n=>n.role==='button'&&/^搜索\\s*↵$/.test(n.name));await click(submit.ref);await cdp('Emulation.setFocusEmulationEnabled',{enabled:true});await wait(100);}
async function button(name){await click((await observe(n=>n.role==='button'&&n.name===name)).ref);}
async function settled(){await cdp('Emulation.setFocusEmulationEnabled',{enabled:true});await wait(100);for(let i=0;i<60;i++){if(await js("!!document.querySelector('[data-knowledge-ready=\\\"true\\\"]')"))return;await wait(100);}throw Error('Knowledge surface never became ready');}
async function back(){await button('返回搜索结果');await settled();}
// Target the observed raw control by its canonical event, rather than choosing
// the first identical button in a bounded multi-event reading window.
async function rawForEvent(id){await observe(n=>n.role==='button'&&n.name==='原始记录');const selector='[data-event-id='+JSON.stringify(id)+'] .knowledge-raw-button';await js('(()=>{const node=document.querySelector('+JSON.stringify(selector)+');if(!node||node.disabled)throw Error("Canonical raw control unavailable");node.click()})()');await settled();}
// AgentMux currently exposes HTML summary as text without an actionable ref.
// Use its documented page-code escape hatch only for this observed control.
async function contextDisclosure(){await observe(n=>n.name==='Context 边界');await js("(()=>{const nodes=Array.from(document.querySelectorAll('summary')).filter(e=>e.textContent==='Context 边界');if(nodes.length!==1)throw Error('Ambiguous Context disclosure');nodes[0].click()})()");await settled();}
// Preserve the focused-page condition for an observed disabled-during-refresh
// control. The generic input wrapper briefly changes visibility; resolve the
// fresh snapshot's exact backend node instead of selecting by appearance.
async function contextClick(node){const resolved=await cdp('DOM.resolveNode',{backendNodeId:node.backendNodeId});for(let i=0;i<60;i++){const status=await cdp('Runtime.callFunctionOn',{objectId:resolved.object.objectId,functionDeclaration:'function(){return {connected:this.isConnected,disabled:this.disabled}}',returnByValue:true});if(!status.result.value.connected)throw Error('Context control was replaced');if(status.result.value.disabled){await wait(100);continue;}const result=await cdp('Runtime.callFunctionOn',{objectId:resolved.object.objectId,functionDeclaration:'function(){this.click();return this.dataset.contextId}',returnByValue:true});if(result.exceptionDetails)throw Error(result.exceptionDetails.exception?.description || result.exceptionDetails.text);return result.result.value;}throw Error('Context control stayed disabled');}
`;
try {
 for(let i=0;i<200 && !existsSync(instanceFile);i++){if(fixture.exitCode!==null)throw Error(output);await delay(100)}
 if(!existsSync(instanceFile))throw Error('Fixture did not publish its private local instance.');
 const instance=JSON.parse(readFileSync(instanceFile,'utf8'));
 const queryCLI=flags=>JSON.parse(run(binary,['traj','query',...flags,'--instance-file',instanceFile,'--format','json']));
 const preparedQueryCLI=async flags=>{for(let i=0;i<60;i++){const result=queryCLI(flags);if(!result.coverage.gaps.includes('index_pending'))return result;await delay(100);}throw Error('Fixture index did not finish its explicit pending state.');};
 const getCLI=(id,flags=[])=>JSON.parse(run(binary,['traj','get',id,...flags,'--instance-file',instanceFile,'--format','json']));
 const events=queryCLI(['events','--agent','codex','--tool','exec_command']);
 if(events.events.length!==1)throw Error('Native fixture selector mismatch.');
 const eventID=events.events[0].id;
 const url=instance.endpoint+'/?view=knowledge&lang=zh#trajectory='+instance.token;
 browser(`await cdp('Emulation.setFocusEmulationEnabled',{enabled:true});await gotoUrl('about:blank');await gotoUrl(${JSON.stringify(url)});${helpers}await observe(n=>n.role==='text input');return {ready:true};`);
 const proof={at:new Date().toISOString(),browser_provider:provider??'agentmux',browser_condition:'CDP focused-page emulation for functional UI assertions; native paint/CPU measured separately without emulation',event_id:eventID,checks:[],dataset:'four native vendor fixtures, ordinary bagakit-researcher word in native tool arguments + Proxy command + Skill mention + Context unknown + permission + summary + candidate/counterexample',ui_sha256:createHash('sha256').update(readFileSync(join(root,'ui/dist/index.html'))).digest('hex')};
 const word='bagakit-researcher';
 const plain=(await preparedQueryCLI(['sessions','--text',word,'--agent','codex'])).sessions;
 if(plain.length!==1||plain[0].matched_ids[0]!==eventID||!plain[0].matched_preview?.includes(word))throw Error('Plain native word did not bind preview to exact matched event: '+JSON.stringify({plain,eventID}));
 const plainUI=browser(`${helpers}await query(${JSON.stringify(word)});await settled();await observe(n=>n.role==='button'&&n.name.includes(${JSON.stringify(word)}));return await js("({rows:Array.from(document.querySelectorAll('[data-session-id]')).map(e=>({id:e.dataset.sessionId,match:e.dataset.matchEventId,preview:e.querySelector('[data-matched-preview]')?.textContent,highlight:Array.from(e.querySelectorAll('[data-matched-preview] mark')).map(n=>n.textContent)})),advancedOpen:document.querySelector('.knowledge-advanced-search')?.open,toolChips:document.querySelectorAll('.knowledge-shortcuts button').length})");`);
 if(plainUI.rows.length!==1||plainUI.rows[0].id!==plain[0].id||plainUI.rows[0].match!==eventID||plainUI.rows[0].preview!==plain[0].matched_preview||!plainUI.rows[0].highlight.some(text=>text.toLowerCase()===word)||plainUI.advancedOpen||plainUI.toolChips)throw Error('Plain search did not show the exact preview/highlight with advanced filters collapsed.');
 const plainSlice=getCLI(eventID,['--around','3','--max-bytes','5120']);
 const plainProcess=browser(`${helpers}await click((await observe(n=>n.role==='button'&&n.name.includes(${JSON.stringify(word)}))).ref);await settled();return await js("({selected:document.querySelector('#knowledge-tab-evidence')?.getAttribute('aria-selected'),focus:document.querySelector('.knowledge-step-match')?.closest('[data-event-id]')?.dataset.eventId,ids:Array.from(document.querySelectorAll('[data-event-id]')).map(e=>e.dataset.eventId),text:document.querySelector('.knowledge-evidence')?.textContent})");`);
 if(plainProcess.selected!=='true'||plainProcess.focus!==eventID||JSON.stringify(plainProcess.ids)!==JSON.stringify(plainSlice.events.map(e=>e.id))||!plainProcess.text.includes(word))throw Error('Plain search did not open the exact matched event and native surrounding records by default.');
 const plainRaw=getCLI(eventID,['--view','raw','--around','0']);
 const plainRawExpected=Buffer.from(plainRaw.raw_chunk.data,'base64').toString('utf8');
 const plainRawUI=browser(`${helpers}await rawForEvent(${JSON.stringify(eventID)});return await js("({id:document.querySelector('[data-raw-event-id]')?.dataset.rawEventId,text:document.querySelector('.knowledge-raw')?.textContent})");`);
 if(plainRawUI.id!==eventID||plainRawUI.text!==plainRawExpected||!plainRawUI.text.includes(word))throw Error('Plain word raw view lost canonical identity or original native bytes.');
 const retained=browser(`${helpers}await back();return await js("document.querySelector('.knowledge-search input')?.value");`);
 if(retained!==word)throw Error('Back to search discarded the ordinary word query.');
 proof.checks.push({path:'ordinary_word_preview_highlight_context_raw_back',word,session_id:plain[0].id,matched_id:eventID,matched_preview:plain[0].matched_preview,context_ids:plainProcess.ids});
 for(const [selector,flags] of [['tool:exec_command',['sessions','--tool','exec_command']],['skill:proxy-debugger',['sessions','--skill','proxy-debugger']]]) {
  const expected=queryCLI(flags).sessions.map(s=>s.id).sort();
  const actual=browser(`${helpers}await query(${JSON.stringify(selector)});await observe(n=>n.role==='button'&&n.name.includes('Inspect Proxy'));return await js("Array.from(document.querySelectorAll('[data-session-id]')).map(e=>e.dataset.sessionId)");`).sort();
  if(JSON.stringify(actual)!==JSON.stringify(expected))throw Error('Popover/CLI session IDs differ for '+selector);
  proof.checks.push({selector,session_ids:actual});
 }
 // Detailed semantic paths are asserted below on canonical evidence IDs.
 const focused=browser(`${helpers}await query('tool:exec_command agent:codex');const hit=await observe(n=>n.role==='button'&&n.name.includes('Inspect Proxy'));await click(hit.ref);await tab('过程');await observe(n=>n.role==='button'&&n.name.includes('来源'));return await js("Array.from(document.querySelectorAll('[data-event-id]')).map(e=>e.dataset.eventId)");`);
 if(!focused.includes(eventID))throw Error('Focused canonical event missing in Popover.');
 proof.checks.push({path:'query_process',event_ids:focused});
 const first=getCLI(eventID).events[0];
 const rawExpected=Buffer.from(getCLI(first.id,['--view','raw','--around','0']).raw_chunk.data,'base64').toString('utf8');
 const rawActual=browser(`${helpers}const n=await observe(n=>n.role==='button'&&n.name==='原始记录');await click(n.ref);await settled();return await js("document.querySelector('.knowledge-raw')?.textContent");`);
 if(rawActual!==rawExpected)throw Error('Popover raw bytes differ from native CLI.');
 proof.checks.push({path:'exact_raw_bytes',event_id:first.id});
 browser(`${helpers}await back();return true;`);
 for(const agent of ['claude','trae','grok']) {
  const expected=queryCLI(['sessions','--agent',agent]).sessions.map(s=>s.id).sort();
  const actual=browser(`${helpers}await query(${JSON.stringify('agent:'+agent)});await settled();return await js("Array.from(document.querySelectorAll('[data-session-id]')).map(e=>e.dataset.sessionId)");`).sort();
  if(JSON.stringify(actual)!==JSON.stringify(expected)||!actual.length)throw Error('Native vendor UI coverage differs: '+agent);
  proof.checks.push({path:'vendor_'+agent,session_ids:actual});
 }
 const candidate=queryCLI(['knowledge','--kind','candidate','--agent','codex']).knowledge[0];
 const counterexample=queryCLI(['knowledge','--kind','counterexample','--agent','codex']).knowledge[0];
 if(!candidate||!counterexample)throw Error('Authored candidate/counterexample fixture absent.');
 const knowledge=browser(`${helpers}await query('in:knowledge kind:candidate agent:codex');await click((await observe(n=>n.role==='button'&&n.name.includes('Inspect Proxy'))).ref);await settled();await tab('详情');await settled();return await js("({ids:Array.from(document.querySelectorAll('[data-knowledge-id]')).map(e=>e.dataset.knowledgeId),text:document.querySelector('.knowledge-inspector')?.textContent})");`);
 if(!knowledge.ids.includes(candidate.id)||!knowledge.text.includes('未提供适用条件'))throw Error('Knowledge candidate lost identity or unknown applicability.');
 proof.checks.push({path:'candidate_counterexample',candidate_id:candidate.id,counterexample_id:counterexample.id});
 browser(`${helpers}const c=await observe(n=>n.role==='button'&&n.name.includes(${JSON.stringify(counterexample.text)}));await click(c.ref);await settled();return true;`);
 const shownCounter=browser(`return await js("document.querySelector('.knowledge-inspector')?.dataset.knowledgeId");`);
 if(shownCounter!==counterexample.id)throw Error('Linked counterexample does not navigate to exact record.');
 browser(`${helpers}await back();await query('in:contexts context-scope:actual_input agent:codex');await click((await observe(n=>n.role==='button'&&n.name.includes('Inspect Proxy'))).ref);await settled();await tab('详情');await settled();await contextDisclosure();return true;`);
 const actualContext=queryCLI(['contexts','--context-scope','actual_input','--agent','codex']).contexts[0];
 const contextUI=browser(`return await js("({ids:Array.from(document.querySelectorAll('[data-context-id]')).map(e=>e.dataset.contextId),text:document.querySelector('.knowledge-manifest')?.textContent})");`);
 if(actualContext.membership!=='unknown'||!contextUI.ids.includes(actualContext.id)||!contextUI.text?.includes('未知'))throw Error('Actual model input falsely established or context identity lost.');
 proof.checks.push({path:'context_unknown',context_id:actualContext.id});
 browser(`${helpers}await back();await query('in:contexts context-scope:archive agent:codex');await click((await observe(n=>n.role==='button'&&n.name.includes('Inspect Proxy'))).ref);await settled();await tab('详情');await settled();await contextDisclosure();return true;`);
 const archive=queryCLI(['contexts','--context-scope','archive','--agent','codex']).contexts;
 const compact=archive.find(c=>c.transformation);
 if(!compact)throw Error('Native compaction boundary absent.');
 const compactUI=browser(`${helpers}const c=await observe(n=>n.role==='button'&&n.name.includes(${JSON.stringify(compact.source_revision)})&&n.name.endsWith(${JSON.stringify('L'+compact.source.line)}));const clicked=await contextClick(c);if(clicked!==${JSON.stringify(compact.id)})throw Error('Wrong Context control');await settled();for(let i=0;i<60;i++){const value=await js("(()=>{const n=document.querySelector('.knowledge-manifest');return n?.dataset.contextId==="+${JSON.stringify(JSON.stringify(compact.id))}+"?n.textContent:null})()");if(value)return value;await wait(100);}throw Error('Selected compaction manifest unavailable');`);
 if(!compactUI?.includes('原始成员')||!compactUI.includes('未知'))throw Error('Compaction originals were invented.');
 proof.checks.push({path:'compaction_boundary',context_id:compact.id});
 browser(`${helpers}await back();await query('tool:exec_command agent:codex');await click((await observe(n=>n.role==='button'&&n.name.includes('Inspect Proxy'))).ref);await settled();await tab('关系');await settled();await observe(n=>n.role==='button'&&n.name.includes('工具调用'));return true;`);
 const expectedGraph=getCLI(eventID,['--view','relations']);
 const graphUI=browser(`return await js("({nodes:Array.from(document.querySelectorAll('[data-graph-node-id]')).map(e=>e.dataset.graphNodeId),relations:Array.from(document.querySelectorAll('[data-relation-id]')).map(e=>({id:e.dataset.relationId,status:e.dataset.evidenceStatus}))})");`);
 if(!graphUI.nodes.includes(eventID)||!expectedGraph.relations.every(r=>graphUI.relations.some(x=>x.id===r.id&&x.status===r.status)))throw Error('Relation graph differs from native source evidence.');
 browser(`${helpers}await click((await observe(n=>n.role==='button'&&n.name.startsWith('来源证据 · L'))).ref);await settled();return true;`);
 proof.checks.push({path:'native_graph_source',nodes:graphUI.nodes,relations:graphUI.relations});
 const key=browser(`${helpers}await back();const input=await observe(n=>n.role==='text input');await pressKey(input.ref,'Enter');await settled();return await js("({detail:!!document.querySelector('#knowledge-detail-panel'),matches:document.querySelectorAll('[data-session-id]').length})");`);
 if(key.detail||!key.matches)throw Error('Enter must submit search without unexpectedly opening a result.');
 const invalid=browser(`${helpers}await query('aTermWhichIsAbsentFromThisFixture');await settled();await observe(n=>n.name==='没有找到匹配内容');await query('unsupported:selector');await observe(n=>n.name.includes('查询条件不受支持'));return await js("document.querySelector('[role=alert]')?.textContent.includes('查询条件不受支持')");`);
 if(!invalid)throw Error('Unsupported selector alert missing.');
 proof.checks.push({path:'keyboard_empty_invalid'});
 browser(`${helpers}await query('tool:exec_command agent:codex');await settled();return true;`);
 for(const [language,expectedLabel] of [['ja','検索'],['en','Search'],['zh','搜索']]) {
  browser(`${helpers}await click((await observe(n=>n.role==='button'&&(/^(语言|Language|言語):/.test(n.name)))).ref);await settled();return true;`);
  const localized=browser(`return await js("({language:document.documentElement.lang,search:document.querySelector('.knowledge-search input')?.placeholder})");`);
  if(localized.language.split('-')[0]!==language||!localized.search?.includes(expectedLabel))throw Error('Knowledge language transition failed: '+language);
 }
 browser(`${helpers}const t=await observe(n=>n.role==='button'&&n.name.includes('主题'));await click(t.ref);return true;`);
 const dark=browser(`return await js("document.documentElement.dataset.theme");`);
 browser(`${helpers}const t=await observe(n=>n.role==='button'&&n.name.includes('主题'));await click(t.ref);return true;`);
 const restored=browser(`return await js("document.documentElement.dataset.theme");`);
 if(dark===restored)throw Error('Theme did not change.');
 browser(`await cdp('Emulation.setDeviceMetricsOverride',{width:430,height:600,deviceScaleFactor:1,mobile:false});return true;`);
 const narrow=browser(`${helpers}await settled();return await js("({width:innerWidth,scroll:document.documentElement.scrollWidth,knowledgeWidth:document.querySelector('.knowledge-prototype')?.getBoundingClientRect().width})");`);
 browser(`await cdp('Emulation.clearDeviceMetricsOverride',{});return true;`);
 if(narrow.scroll>narrow.width+1||!narrow.knowledgeWidth)throw Error('Knowledge overflows narrow Popover width.');
 proof.checks.push({path:'languages_theme_narrow',narrow});
 // Delete only this owned synthetic source, then restore its exact bytes.
 // A selected old reference must report missing/stale rather than a fresh body.
 const findSource=path=>{for(const e of readdirSync(path,{withFileTypes:true})){const p=join(path,e.name);if(e.isDirectory()){const f=findSource(p);if(f)return f}else if(e.name.endsWith('.jsonl'))return p}};
 const codexPath=findSource(join(fixtureRoot,'codex')),original=readFileSync(codexPath);
 browser(`${helpers}await click((await observe(n=>n.role==='button'&&n.name.includes('Inspect Proxy'))).ref);await settled();await tab('过程');return true;`);
 try {
  unlinkSync(codexPath);
  const stale=browser(`${helpers}await click((await observe(n=>n.role==='button'&&n.name==='原始记录')).ref);await observe(n=>n.name.includes('来源已变化'));return await js("document.querySelector('[role=alert]')?.textContent.includes('来源已变化')");`);
  if(!stale)throw Error('Changed source did not present a source alert.');
 } finally {writeFileSync(codexPath,original,{mode:0o600});}
 proof.checks.push({path:'source_invalidation'});
 proof.complete=true;
 writeFileSync(join(artifacts,'ui-acceptance.json'),JSON.stringify(proof,null,2)+'\n',{mode:0o600});
 console.log('Trajectory production UI: ordinary word/preview/highlight/context/raw, selectors, 4 vendors, knowledge/counterexample, context/compaction, native graph, keyboard, locales/themes/narrow width and source invalidation pass.');
} catch (error) {
 writeFileSync(join(artifacts,'ui-failure.txt'),String(error.stack ?? error).replace(/#trajectory=[^\s"']+/g,'#trajectory=[redacted]')+'\n',{mode:0o600});
 throw error;
} finally {
 try {browser(`await cdp('Emulation.setFocusEmulationEnabled',{enabled:false});return true;`);}catch{}
 writeFileSync(instanceFile+'.stop','stop\n',{mode:0o600});
 await Promise.race([new Promise(r=>fixture.once('exit',r)),delay(4000)]);
 if(fixture.exitCode===null)fixture.kill('SIGTERM');
}
