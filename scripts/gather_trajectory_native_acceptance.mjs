// Measure an isolated native instance without requesting manual UI actions.
// Native show/paint automation is unavailable in this environment. Retain any
// incidental observations, but never promote them into a passing paint proof.
import { spawnSync } from 'node:child_process';
import { readFileSync,writeFileSync,realpathSync,statSync } from 'node:fs';
import { resolve,join } from 'node:path';
import { request } from 'node:http';
import { candidateInputDigest,fixtureDigest,fileDigest } from './trajectory_acceptance_inputs.mjs';

const root=resolve(import.meta.dirname,'..');
const artifacts=join(root,'.bagakit/feature-tracker/features/f-22duuagpj/artifacts');
const run=(command,args)=>{const r=spawnSync(command,args,{encoding:'utf8',maxBuffer:4*1024*1024,env:{...process.env,LC_ALL:'C'}});if(r.status!==0)throw Error('Native observation command failed: '+command);return r.stdout.trim()};
const config=JSON.parse(readFileSync(join(artifacts,'native-runtime.json'),'utf8'));
if(resolve(config.fixture_root)!==join(artifacts,'ui-fixture')||resolve(config.instance_file)!==join(artifacts,'ui-fixture/history.jsonl.trajectory/instance.json'))throw Error('Native acceptance must use the isolated fixture, never production content access.');
const fixtureRoot=join(realpathSync(artifacts),'ui-fixture');
if(realpathSync(config.fixture_root)!==fixtureRoot||realpathSync(config.instance_file)!==join(fixtureRoot,'history.jsonl.trajectory/instance.json'))throw Error('Native fixture paths must not escape through symlinks.');
const binary=join(root,'dist/Agent Load.app/Contents/MacOS/agentload');
const candidate=candidateInputDigest(root),binarySHA=fileDigest(binary);
if(config.binary_sha256!==binarySHA||config.candidate_input_sha256!==candidate)throw Error('Native fixture was launched from a different candidate; old process evidence cannot be relabeled.');
const instance=JSON.parse(readFileSync(config.instance_file,'utf8'));
if(instance.pid!==config.pid || !/^http:\/\/127\.0\.0\.1:\d+$/.test(instance.endpoint))throw Error('Native instance identity mismatch.');
const address=new URL(instance.endpoint),history=join(fixtureRoot,'history.jsonl');
if(realpathSync(history)!==history)throw Error('Native history must not escape through a leaf symlink.');
const processCommand=run('/bin/ps',['-ww','-p',String(config.pid),'-o','command=']);
if(processCommand!==`${binary} --listen ${address.host} --history-file ${history}`)throw Error('Native process must actually own the isolated fixture history.');
const processStart=Date.parse(run('/bin/ps',['-p',String(config.pid),'-o','lstart=']));
if(!Number.isFinite(processStart)||processStart+999<statSync(binary).mtimeMs)throw Error('Native process predates the current executable.');
// Keep environment contents in memory: never print process environment/token.
const environment=run('/bin/ps',['eww','-p',String(config.pid),'-o','command=']);
for(const vendor of ['claude','codex','trae','grok','gemini','opencode','hermes','openclaw','pi']) {
 const path=join(fixtureRoot,vendor);
 if(realpathSync(path)!==path||!(` ${environment} `).includes(` AGENTLOAD_${vendor.toUpperCase()}_DIRS=${path} `))throw Error('Native source roots must actually be isolated fixture directories.');
}
const listeners=run('/usr/sbin/lsof',['-nP','-a','-p',String(config.pid),'-iTCP:'+address.port,'-sTCP:LISTEN','-Fn']);
if(!listeners.split('\n').includes('n'+address.host))throw Error('Native endpoint must actually belong to the fixture process.');
const assertCandidate=()=>{if(candidateInputDigest(root)!==candidate||fileDigest(binary)!==binarySHA)throw Error('Native candidate changed during observation; evidence discarded.');};
const paintObservation=()=>{
 const events=readFileSync(config.lifecycle_file,'utf8').trim().split('\n').filter(Boolean).map(JSON.parse).filter(e=>e.event==='popover_paint'&&e.pid===config.pid&&e.extra?.warm==='true');
 return {status:'untestable',reason:'This environment cannot reliably automate native menu-bar open/close and paint sampling. User explicitly removed human-assisted gates; incidental observations do not prove the 150ms target.',target_ms:150,boundary:'native_show_request_to_ready_two_animation_frames',observed_ms:events.map(e=>Number(e.extra.milliseconds)),events};
};
if(process.argv.includes('--join')) {
 const proof=JSON.parse(readFileSync(join(artifacts,'native-runtime-observations.json'),'utf8'));
 if(proof.pid!==config.pid||proof.candidate_input_sha256!==candidateInputDigest(root)||proof.binary_sha256!==fileDigest(binary)||proof.dataset_sha256!==fixtureDigest(config.fixture_root))throw Error('Native runtime observations belong to different inputs.');
 Object.assign(proof,{paint:paintObservation()});
 assertCandidate();
 writeFileSync(join(artifacts,'native-acceptance.json'),JSON.stringify(proof,null,2)+'\n',{mode:0o600});
 console.log(`Native automatic evidence joined; native open/paint timing UNTESTABLE. Idle CPU ${proof.idle.cpu_percent.toFixed(3)}%.`);
 process.exit(0);
}
const cli=(...args)=>JSON.parse(run(binary,['traj',...args,'--instance-file',config.instance_file,'--format','json']));
const rpc=params=>new Promise((done,reject)=>{
 const body=JSON.stringify({jsonrpc:'2.0',id:'native-acceptance',method:'traj.query',params});
 const req=request(instance.endpoint+'/api/rpc',{method:'POST',headers:{Authorization:'Bearer '+instance.token,'X-AgentLoad-Local':'1','Content-Type':'application/json','Content-Length':Buffer.byteLength(body)}},res=>{let text='';res.on('data',b=>text+=b);res.on('end',()=>{try{done(JSON.parse(text))}catch(e){reject(e)}})});req.on('error',reject);req.end(body);
});
const q=cli('query','events','--agent','codex','--tool','exec_command');
const direct=await rpc({collection:'events',agent:'codex',tool:'exec_command',limit:20});
if(!q.events.length||q.events[0].id!==direct.result?.events[0]?.id)throw Error('Native CLI/RPC identity differs.');
cli('access','off');
try {
 const denied=await rpc({collection:'events'});
 if(denied.error?.code!==-32003)throw Error('Disabled native content accessible.');
} finally {cli('access','on');} // isolated roots only; restore on assertion failure
const seconds=value=>{
 const parts=value.trim().split(':').map(Number);
 if(parts.some(n=>!Number.isFinite(n)))throw Error('Invalid observed CPU time.');
 return parts.reduce((total,n)=>total*60+n,0);
};
const processTimes=()=>config.processes.map(p=>({pid:p.pid,scope:p.scope,cpu_seconds:seconds(run('/bin/ps',['-p',String(p.pid),'-o','time=']))}));
const webkitPIDs=()=>run('/bin/ps',['-axww','-o','pid=,comm=']).split('\n').filter(line=>line.includes('/com.apple.WebKit.')).map(line=>Number(line.trim().split(/\s+/,1)[0])).sort((a,b)=>a-b);
if(!Array.isArray(config.processes)||config.processes.some(p=>!Number.isSafeInteger(p.pid)||p.pid<=0||!p.scope)||new Set(config.processes.map(p=>p.pid)).size!==config.processes.length||config.processes.filter(p=>p.pid===config.pid).length!==1)throw Error('Native CPU scope must include the app exactly once and unique positive process IDs.');
const helperPIDs=config.processes.filter(p=>p.pid!==config.pid).map(p=>p.pid);
if(!Array.isArray(config.baseline_webkit)||config.baseline_webkit.some(pid=>!Number.isSafeInteger(pid)||pid<=0)||new Set(config.baseline_webkit).size!==config.baseline_webkit.length||helperPIDs.some(pid=>config.baseline_webkit.includes(pid))||config.baseline_webkit.includes(config.pid)||helperPIDs.length<3)throw Error('Native CPU needs the observed app and complete WebKit helper scope.');
const expectedWebkit=[...new Set([...config.baseline_webkit,...helperPIDs])].sort((a,b)=>a-b);
if(JSON.stringify(webkitPIDs())!==JSON.stringify(expectedWebkit))throw Error('WebKit helper inventory changed since fixture launch; CPU proof is unavailable.');
const sockets=()=>{
 const rows=[];
 for(const p of config.processes){const r=spawnSync('/usr/sbin/lsof',['-nP','-a','-p',String(p.pid),'-i'],{encoding:'utf8'});if(r.status!==0&&r.status!==1)throw Error('Network observation failed');for(const line of r.stdout.trim().split('\n').slice(1)){const name=line.trim().split(/\s+/).slice(8).join(' ');if(name && !name.includes('127.0.0.1:') && !name.includes('[::1]:'))throw Error('Observed non-loopback socket: '+name);rows.push({pid:p.pid,name})}}
 return rows;
};
const machine={architecture:run('/usr/bin/uname',['-m']),os:run('/usr/bin/sw_vers',['-productVersion']),hardware:run('/usr/sbin/sysctl',['-n','hw.model']),logical_cpus:run('/usr/sbin/sysctl',['-n','hw.logicalcpu'])};
const before=processTimes(),networkBefore=sockets(),start=performance.now(),at=new Date().toISOString();
let helperScopeChanged=false;
const observeHelpers=setInterval(()=>{try{if(JSON.stringify(webkitPIDs())!==JSON.stringify(expectedWebkit))helperScopeChanged=true;}catch{helperScopeChanged=true;}},1000);
console.log('Native panel hidden: measuring 45-second idle CPU window.');
try {await new Promise(r=>setTimeout(r,45000));} finally {clearInterval(observeHelpers);}
if(helperScopeChanged||JSON.stringify(webkitPIDs())!==JSON.stringify(expectedWebkit))throw Error('WebKit helper scope changed during CPU observation; partial scope cannot prove the budget.');
const window=(performance.now()-start)/1000,after=processTimes(),networkAfter=sockets();
const cpuDelta=after.reduce((sum,p,i)=>sum+p.cpu_seconds-before[i].cpu_seconds,0);
const cpuPercent=cpuDelta/window*100;
if(cpuPercent>=1 || cpuDelta<0)throw Error('Native idle CPU budget failed: '+cpuPercent);
const entries=readFileSync(config.lifecycle_file,'utf8').trim().split('\n').filter(Boolean).map(line=>JSON.parse(line)).filter(e=>e.event==='popover_paint'&&e.pid===config.pid);
if(entries.some(e=>Date.parse(e.at)>=Date.parse(at)&&Date.parse(e.at)<=Date.now()))throw Error('Native window opened during the hidden idle observation window.');
const runtimeProof={pid:config.pid,at,machine,candidate_input_sha256:candidateInputDigest(root),binary_sha256:fileDigest(binary),dataset_sha256:fixtureDigest(config.fixture_root),idle:{window_seconds:window,cpu_percent:cpuPercent,processes:after.map((p,i)=>({...p,start_cpu_seconds:before[i].cpu_seconds,delta_cpu_seconds:p.cpu_seconds-before[i].cpu_seconds})),condition:'same fixed four-vendor synthetic dataset; native panel hidden; no UI actions or builds during window'},network_observations:{before:networkBefore,after:networkAfter},runtime_local_only:true,content_off_rejected:true,same_identity_cli_rpc:true,native_event_id:q.events[0].id};
assertCandidate();
writeFileSync(join(artifacts,'native-runtime-observations.json'),JSON.stringify(runtimeProof,null,2)+'\n',{mode:0o600});
if(process.argv.includes('--idle-only')) {console.log(`Native runtime: idle CPU ${cpuPercent.toFixed(3)}%; loopback, disabled access and CLI/RPC identity pass. Native open/paint timing UNTESTABLE.`);process.exit(0);}
const proof={...runtimeProof,paint:paintObservation()};
writeFileSync(join(artifacts,'native-acceptance.json'),JSON.stringify(proof,null,2)+'\n',{mode:0o600});
console.log(`Native automatic evidence recorded; native open/paint timing UNTESTABLE. Idle CPU ${cpuPercent.toFixed(3)}%.`);
