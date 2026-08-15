// Development acceptance for an in-place physical upgrade. The old source
// migration keeps its own historical proof; this verifies the current owner.
import { existsSync, readFileSync, statSync, realpathSync } from 'node:fs';
import { join } from 'node:path';
import { homedir } from 'node:os';
import { DatabaseSync } from 'node:sqlite';
import { candidateInputDigest, fileDigest } from './trajectory_acceptance_inputs.mjs';
import { sourceStorageInputDigest, sealedReceipt, stableFileDigest, pageIdentity, verifyRaw, verifySigning, verifyProtected } from './verify_trajectory_source_store.mjs';

const requireProof = (condition, message) => { if (!condition) throw Error(message); };
const sha = x => /^[a-f0-9]{64}$/.test(x ?? '');
const number = x => Number.isSafeInteger(x) && x >= 0;
const sameFile = (a,b) => String(a.dev)===String(b.dev) && String(a.ino)===String(b.ino);
const allocated = path => existsSync(path) ? statSync(path).blocks*512 : 0;

export function verifySourceBlockUpgrade(root,p) {
 const read = ref => sealedReceipt(root,ref), json = ref => JSON.parse(read(ref));
 const history=join(homedir(),'Library/Application Support/AgentLoad/history.jsonl'), database=history+'.trajectory/trajectory.sqlite';
 requireProof([2,3].includes(p.version) && p.kind==='source_exception_blocks' && p.input_sha256===sourceStorageInputDigest(root), 'Block evidence belongs to another candidate.');
 requireProof(p.history_file===history && p.database_path===database && realpathSync(database)===database, 'Block upgrade must use the actual production store.');
 const liveAudit = p.version === 3;
 requireProof(!liveAudit || p.live_audit_policy === 'raw_verified_unsealed_ranges', 'Live audit policy must be explicit.');
 const installed='/Applications/Agent Load.app/Contents/MacOS/agentload';
 requireProof(sha(p.binary_sha256) && fileDigest(installed)===p.binary_sha256 && fileDigest(join(root,'dist/Agent Load.app/Contents/MacOS/agentload'))===p.binary_sha256, 'Installed/built block candidates differ.');
 const build=json(p.build);
 requireProof(build.exit_code===0 && build.command==='./build_macos_app.sh' && build.candidate_input_sha256===candidateInputDigest(root) && read(build.output).includes('Agent Load.app'), 'Actual bound block build is missing.');
 verifySigning(build,json(p.signing),p.binary_sha256,candidateInputDigest(root),read);
 const before=json(p.before), maintenance=json(p.maintenance), after=json(p.after);
 const original=p.version===3?json(p.upgrade_maintenance):maintenance;
 const upgraded=p.version===3?json(p.upgrade_after):after;
 requireProof(before.scope==='whole_source_store_v2' && before.database_path===database && before.version===2 && sha(before.file.sha256) && before.sources.length>0 && before.exceptions>0 && sha(before.controls), 'Whole v2 baseline missing.');
 if(p.version===3){
  const oldBuild=json(p.upgrade_build), oldSign=json(p.upgrade_signing), gate=json(p.upgrade_go_gate);
  requireProof(oldBuild.exit_code===0 && oldBuild.command==='./build_macos_app.sh' && oldBuild.input_sha256===original.input_sha256 && read(oldBuild.output).includes('Agent Load.app') && p.upgrade_binary.sha256===original.binary_sha256 && fileDigest(join(root,p.upgrade_binary.path))===original.binary_sha256, 'Original upgrade binary/build is unbound.');
  verifySigning(oldBuild,oldSign,original.binary_sha256,oldBuild.candidate_input_sha256,read);
  requireProof(gate.exit_code===0 && gate.same_inputs && gate.source_input_sha256===oldBuild.input_sha256 && gate.candidate_input_sha256===oldBuild.candidate_input_sha256 && read(gate.output).includes('PASS'),'Original upgrade Go gate is missing.');
 }
 requireProof((p.version===3 || original.binary_sha256===p.binary_sha256 && original.input_sha256===p.input_sha256) && original.exit_code===0 && original.owner==='exclusive_history_lock' && original.database_path===database && original.started_at>=before.at && original.finished_at>=original.started_at && JSON.stringify(original.arguments)===JSON.stringify(['traj','compact','--history-file',history,'--expected-input-sha256',before.file.sha256]), 'Actual exclusive in-place upgrade is missing.');
 const progress=read(original.progress).trim().split('\n').filter(Boolean).map(JSON.parse),ready=progress.at(-1),upgrade=ready?.block_upgrade;
 requireProof(ready?.phase==='ready' && sha(ready.target_sha256) && upgrade?.phase==='ready' && upgrade.input_sha256===before.file.sha256 && upgrade.bytes_before===before.file.bytes && upgrade.records===before.exceptions && upgrade.total===before.exceptions && sha(upgrade.logical_digest) && upgrade.control_digest===before.controls, 'Complete original-to-target block proof is missing.');
 requireProof(upgraded.database_path===database && upgraded.at>=original.finished_at && upgraded.version===3 && upgraded.controls===before.controls && sameFile(before.file,upgraded.file) && upgraded.file.sha256===ready.target_sha256 && upgraded.exceptions===before.exceptions && upgraded.blocks<upgraded.exceptions && JSON.stringify(before.sources)===JSON.stringify(upgraded.sources), 'Upgrade changed control evidence, lost facts or replaced the whole store.');
 let finalReady=ready, packing, packingBuild;
 if(p.version===3){
  packingBuild=json(p.packing_build);
  const packingSign=json(p.packing_signing), packingGate=json(p.packing_go_gate);
  requireProof(packingBuild.exit_code===0 && packingBuild.command==='./build_macos_app.sh' && packingBuild.input_sha256===maintenance.input_sha256 && read(packingBuild.output).includes('Agent Load.app') && p.packing_binary.sha256===maintenance.binary_sha256 && fileDigest(join(root,p.packing_binary.path))===maintenance.binary_sha256,'Actual packing-stage binary/build is unbound.');
  verifySigning(packingBuild,packingSign,maintenance.binary_sha256,packingBuild.candidate_input_sha256,read);
  requireProof(packingGate.exit_code===0 && packingGate.same_inputs && packingGate.source_input_sha256===packingBuild.input_sha256 && packingGate.candidate_input_sha256===packingBuild.candidate_input_sha256 && read(packingGate.output).includes('PASS'),'Packing-stage Go gate is missing.');
  requireProof(maintenance.exit_code===0 && maintenance.owner==='exclusive_history_lock' && maintenance.database_path===database && maintenance.started_at>=upgraded.at && maintenance.finished_at>=maintenance.started_at && JSON.stringify(maintenance.arguments)===JSON.stringify(['traj','compact','--history-file',history,'--expected-input-sha256',upgraded.file.sha256]),'Actual exclusive packing is missing.');
  finalReady=read(maintenance.progress).trim().split('\n').filter(Boolean).map(JSON.parse).at(-1);packing=finalReady?.block_packing;
  requireProof(finalReady?.phase==='ready' && packing?.phase==='ready' && packing.input_sha256===upgraded.file.sha256 && packing.request_input_sha256===upgraded.file.sha256 && packing.bytes_before===upgraded.file.bytes && packing.logical_digest===upgrade.logical_digest && packing.control_digest===before.controls && packing.records===before.exceptions && packing.blocks===upgraded.blocks && packing.packed_blocks===packing.blocks,'Complete preserving physical packing is missing.');
  requireProof(after.database_path===database && after.at>=maintenance.finished_at && after.version===3 && after.controls===before.controls && sameFile(upgraded.file,after.file) && after.file.sha256===finalReady.target_sha256 && after.exceptions===before.exceptions && after.blocks===upgraded.blocks && JSON.stringify(before.sources)===JSON.stringify(after.sources),'Packing changed canonical/control evidence.');
 }
 const fileBefore=statSync(database);
 requireProof(sameFile(fileBefore,after.file), 'Current store differs from the verified in-place target.');
 for(const suffix of ['.source-migrating','.source-legacy','.source-ready.json']) requireProof(!existsSync(database+suffix),'Unfinished old source cutover remains.');
 const db=new DatabaseSync(database,{readOnly:true});
 try {
  db.exec('PRAGMA query_only=ON; PRAGMA cache_size=-4096; BEGIN');
  requireProof(db.prepare('PRAGMA user_version').get().user_version===3 && db.prepare('PRAGMA page_size').get().page_size===16384 && db.prepare('PRAGMA auto_vacuum').get().auto_vacuum===2, 'Final block schema/page semantics differ.');
  requireProof(JSON.stringify(db.prepare("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name").all().map(x=>x.name))===JSON.stringify(['exceptions','meta','metadata','ranges','sources']), 'An extra body/projection/proof table remains.');
  requireProof(db.prepare('PRAGMA quick_check').all().every(x=>x.quick_check==='ok') && db.prepare('PRAGMA foreign_key_check').all().length===0, 'Final block integrity failed.');
  const proof=JSON.parse(db.prepare("SELECT value FROM meta WHERE key='source-block-upgrade-result'").get()?.value ?? '{}');
  requireProof(JSON.stringify(proof)===JSON.stringify(upgrade) && !db.prepare("SELECT 1 FROM meta WHERE key='source-block-upgrade'").get() && !db.prepare('SELECT 1 FROM exceptions WHERE records<1 OR records>64 OR end_offset<offset LIMIT 1').get(), 'Block transition is incomplete or differs from the verified receipt.');
  requireProof(db.prepare('SELECT coalesce(sum(records),0) AS n FROM exceptions').get().n===before.exceptions, 'Logical exception cardinality differs.');
  if(p.version===3) requireProof(JSON.stringify(JSON.parse(db.prepare("SELECT value FROM meta WHERE key='source-block-packing-result'").get()?.value ?? '{}'))===JSON.stringify(packing) && !db.prepare("SELECT 1 FROM meta WHERE key='source-block-packing'").get(),'Physical packing is incomplete.');
  db.exec('COMMIT');
 } finally {db.close();}
 const bytes=['','-journal','-wal','-shm'].reduce((n,s)=>n+allocated(database+s),0);
 requireProof(bytes>0 && bytes<2**30 && bytes<before.file.bytes && finalReady.bytes_after<2**30, 'Real complete trajectory store has not reached the accepted capacity target.');
 const protection=json(p.protected_files), baseline=json(before.protected_files);
 requireProof(protection.before_sha256===before.protected_files.sha256 && protection.database_path===database && protection.input_sha256===(p.version===3?packingBuild.input_sha256:p.input_sha256),'Protected packing inventory is unbound.');
 for(const kind of ['sessions','history','usage','annotations']) verifyProtected(baseline[kind],protection[kind]);
 const catalog=json(p.source_catalog);
 requireProof(catalog.database_path===database && catalog.input_sha256===p.input_sha256 && Array.isArray(catalog.sources),'Source catalog is unbound.');
 const queries=p.queries.map(json);
 for(const text of ['research','bagakit-researcher']) for(const state of ['cold','warm']) {
  const pair=['cli','rpc'].map(transport=>{
   const q=queries.find(x=>x.text===text&&x.state===state&&x.transport===transport);
   requireProof(q && q.binary_sha256===p.binary_sha256 && q.input_sha256===p.input_sha256 && q.database_path===database && q.limit===20 && q.count===false && !q.error && !q.result?.error && Number.isFinite(q.elapsed_ms) && q.elapsed_ms>=0 && q.elapsed_ms<6000 && sameFile(q.target_file,after.file),`Real ${text}/${state}/${transport} query failed.`);
   const identities=pageIdentity(q.result,{allowAuditPending:liveAudit}), raw=json(q.raw);
   requireProof(raw.binary_sha256===p.binary_sha256 && !raw.error,'Actual raw evidence is unbound.');
   return [identities,verifyRaw(raw,q.result.sessions[0].matched_ids[0],catalog.sources)];
  });
  requireProof(JSON.stringify(pair[0])===JSON.stringify(pair[1]),'Actual CLI/RPC identities or raw differ.');
 }
 const counts=p.explicit_counts.map(json);
 for(const text of ['research','bagakit-researcher']) {
  const pair=['cli','rpc'].map(transport=>{
   const q=counts.find(x=>x.text===text&&x.transport===transport);
   requireProof(q?.binary_sha256===p.binary_sha256 && q.input_sha256===p.input_sha256 && q.count===true && !q.error && !q.result?.error && q.elapsed_ms>=0 && q.elapsed_ms<60000 && q.result.matched_total>0,'Real explicit count is missing.');
   return [q.result.matched_total,pageIdentity(q.result,{count:true,allowAuditPending:liveAudit})];
  });requireProof(JSON.stringify(pair[0])===JSON.stringify(pair[1]),'Explicit CLI/RPC counts differ.');
 }
 const growth=json(p.post_catchup),frames=read(growth.frames).trim().split('\n').filter(Boolean).map(JSON.parse);
 requireProof(frames.length>=2 && Date.parse(frames.at(-1).at)-Date.parse(frames[0].at)>=300000 && frames.every(f=>f.binary_sha256===p.binary_sha256&&f.input_sha256===p.input_sha256&&f.pending_sources===0&&f.database_path===database&&sameFile(f.target_file,after.file)&&f.available_bytes>=2**30),'Actual post-catchup 300s capacity window is incomplete.');
 for(const frame of frames){const q=json(frame.readiness);requireProof(q.binary_sha256===p.binary_sha256 && q.at===frame.at && !q.error,'Readiness frame is unbound.');pageIdentity(q.result,{allowAuditPending:liveAudit});}
 requireProof(growth.unchanged_sources_rewritten===0 && growth.start_allocated_bytes===frames[0].allocated_bytes && growth.end_allocated_bytes===frames.at(-1).allocated_bytes && stableFileDigest(database)===frames.at(-1).target_file.sha256,'Growth/checkpoint or final target differs.');
 const capacity=json(p.capacity),samples=read(capacity.frames).trim().split('\n').filter(Boolean).map(JSON.parse);
 requireProof(samples.length>=2 && samples[0].at<=maintenance.started_at && samples.at(-1).at>=maintenance.finished_at && samples.every(s=>s.available_bytes>=2**30 && s.whole_shadows===0 && number(s.journal_allocated_bytes)&&number(s.swap_allocated_bytes)),'Actual preserving in-place peak-space observations are missing.');
 if(p.version===3){const old=read(json(p.upgrade_capacity).frames).trim().split('\n').filter(Boolean).map(JSON.parse);requireProof(old.length>=2 && old[0].at<=original.started_at && old.at(-1).at>=original.finished_at && old.every(s=>s.available_bytes>=2**30 && s.whole_shadows===0),'Original upgrade capacity window is missing.');}
 requireProof(sourceStorageInputDigest(root)===p.input_sha256 && sameFile(fileBefore,statSync(database)), 'Candidate/store changed during acceptance.');
 console.log(`Verified v3 whole-store upgrade (live audit gaps retained): ${(before.file.bytes/2**20).toFixed(1)} -> ${(bytes/2**20).toFixed(1)} MiB; ${before.exceptions} exact exception facts, actual queries/raw and 300s growth.`);
}
