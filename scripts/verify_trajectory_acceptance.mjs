import { readFileSync,writeFileSync,existsSync } from 'node:fs';
import { resolve,join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { candidateInputDigest,fixtureDigest,fileDigest } from './trajectory_acceptance_inputs.mjs';

const root=resolve(import.meta.dirname,'..');
const artifacts=join(root,'.bagakit/feature-tracker/features/f-22duuagpj/artifacts');
const read=name=>{const p=join(artifacts,name);if(!existsSync(p))throw Error('Missing actual acceptance evidence: '+name);return JSON.parse(readFileSync(p,'utf8'))};
const native=read('native-acceptance.json'),ui=read('ui-acceptance.json');
const watch=read('native-watch-acceptance.json');
const requiredUI=['query_process','exact_raw_bytes','vendor_claude','vendor_trae','vendor_grok','candidate_counterexample','context_unknown','compaction_boundary','native_graph_source','keyboard_empty_invalid','languages_theme_narrow','source_invalidation'];
if(!ui.complete||requiredUI.some(path=>!ui.checks.some(check=>check.path===path)))throw Error('Actual production UI workflow evidence incomplete.');
const binary=join(root,'dist/Agent Load.app/Contents/MacOS/agentload');
if(native.candidate_input_sha256!==candidateInputDigest(root)||native.binary_sha256!==fileDigest(binary))throw Error('Native evidence belongs to a different candidate; rebuild and measure the current app.');
if(native.dataset_sha256!==fixtureDigest(join(artifacts,'ui-fixture')))throw Error('Native fixed dataset changed.');
if(ui.ui_sha256!==fileDigest(join(root,'ui/dist/index.html')))throw Error('Browser evidence belongs to a different production UI.');
if(native.paint?.status!=='untestable'||!native.paint.reason||native.paint.target_ms!==150||native.paint.boundary!=='native_show_request_to_ready_two_animation_frames'||!Array.isArray(native.paint.observed_ms)||native.paint.observed_ms.some(ms=>!Number.isFinite(ms)||ms<0))throw Error('Native paint limitations must be explicitly recorded; unavailable evidence must not be reported as a pass.');
if(native.idle.window_seconds<30||native.idle.cpu_percent>=1||native.idle.cpu_percent<0||!native.idle.processes.length)throw Error('Actual native idle CPU budget failed.');
if(!native.runtime_local_only||!native.content_off_rejected||!native.same_identity_cli_rpc)throw Error('Native privacy/identity evidence incomplete.');
if(watch.pid!==native.pid||!watch.batch.reset_required||watch.fresh_selector_resume?.reset_required||!watch.fresh_selector_resume?.cursor||watch.candidate_input_sha256!==native.candidate_input_sha256||watch.binary_sha256!==native.binary_sha256||watch.dataset_sha256!==native.dataset_sha256)throw Error('Exact native NDJSON watch reset/resume evidence incomplete.');
const longPath=join(artifacts,'long-session-acceptance.json');
if(!existsSync(longPath)||read('long-session-acceptance.json').candidate_input_sha256!==native.candidate_input_sha256){
 const result=spawnSync('go',['test','./internal/trajectory','-run','^TestTrajectoryLongSessionAcceptance$','-count=1','-timeout','2m'],{cwd:root,env:{...process.env,AGENTLOAD_TRAJECTORY_LONG_PROOF:longPath},encoding:'utf8'});
 if(result.status!==0)throw Error(result.stderr+result.stdout);
 if(candidateInputDigest(root)!==native.candidate_input_sha256)throw Error('Current inputs changed during long-session measurement.');
 const fresh=JSON.parse(readFileSync(longPath,'utf8'));
 fresh.candidate_input_sha256=native.candidate_input_sha256;
 fresh.measured_at=new Date().toISOString();
 writeFileSync(longPath,JSON.stringify(fresh,null,2)+'\n',{mode:0o600});
}
const long=read('long-session-acceptance.json');
if(long.records!==30000||long.warm_slice_ms.length!==10||long.warm_slice_ms.some(ms=>ms>150)||long.max_result_bytes>5120||(!Number.isSafeInteger(long.max_replayed_records_per_slice)||long.max_replayed_records_per_slice>5000)||long.retained_go_heap_delta_bytes>long.heap_limit_bytes)throw Error('Long-session slice/retention budget failed.');
const matrix=spawnSync('go',['test','.','-run','^TestTrajectoryAcceptanceNativeMatrix$','-count=1'],{cwd:root,encoding:'utf8'});
if(matrix.status!==0)throw Error(matrix.stderr+matrix.stdout);
console.log(`Trajectory automatic acceptance: 4 vendors, exact native candidate, ${native.idle.window_seconds.toFixed(1)}s idle ${native.idle.cpu_percent.toFixed(3)}%, 30k-record bounded slices. Native open/paint timing UNTESTABLE; 150ms target remains unverified.`);
