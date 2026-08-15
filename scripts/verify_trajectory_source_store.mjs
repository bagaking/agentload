// Development acceptance only. Never creates a database, runs maintenance,
// starts an app or supplies missing evidence. The app remains Go-only.
import { existsSync, readFileSync, statSync, realpathSync, openSync, readSync, closeSync, fstatSync } from 'node:fs';
import { join, resolve } from 'node:path';
import { homedir } from 'node:os';
import { createHash } from 'node:crypto';
import { DatabaseSync } from 'node:sqlite';
import { verifySourceBlockUpgrade } from './verify_trajectory_block_upgrade.mjs';
import { candidateInputDigest, fileDigest } from './trajectory_acceptance_inputs.mjs';

export function sourceStorageInputDigest(root) {
  const hash = createHash('sha256').update(candidateInputDigest(root));
  for (const path of ['build_macos_app.sh', 'docs/trajectory-requirements.md',
    'docs/api-reference.md', 'scripts/verify_trajectory_source_store.mjs',
    'scripts/trajectory_acceptance_inputs.mjs','scripts/verify_trajectory_block_upgrade.mjs']) {
    hash.update(path + '\0').update(readFileSync(join(root, path))).update('\0');
  }
  return hash.digest('hex');
}

const requireProof = (condition, message) => { if (!condition) throw Error(message); };
const sha = value => /^[a-f0-9]{64}$/.test(value ?? '');
const nonnegative = value => Number.isSafeInteger(value) && value >= 0;
const positive = value => Number.isSafeInteger(value) && value > 0;
const allocated = path => existsSync(path) ? statSync(path).blocks * 512 : 0;
const hash = bytes => createHash('sha256').update(bytes).digest('hex');
const timestamp = value => {
  requireProof(typeof value === 'string' && /^\d{4}-\d\d-\d\dT.*Z$/.test(value) && Number.isFinite(Date.parse(value)), 'Invalid observation timestamp.');
  return Date.parse(value);
};
const sameFile = (a, b) => String(a.dev) === String(b.dev) && String(a.ino) === String(b.ino);
const canonicalHistory = () => join(homedir(), 'Library/Application Support/AgentLoad/history.jsonl');
const sessionID = value => typeof value === 'string' && /^s\.[a-f0-9]{16}\.[a-f0-9]{16}$/.test(value);
const eventID = value => typeof value === 'string' && /^e\.[a-f0-9]{16}\.[a-f0-9]{16}\.[0-9a-z]+\.\d+\.[a-f0-9]{16}$/.test(value);
export const pageIdentity = (result, {count = false, allowAuditPending = false} = {}) => {
  requireProof(Array.isArray(result?.sessions) && result.sessions.length > 0 && result.sessions.length <= 20 &&
    new Set(result.sessions.map(s => s.id)).size === result.sessions.length, 'Invalid or duplicated session page.');
  for (const s of result.sessions) {
    requireProof(sessionID(s.id) && (count ? positive(s.matched_count) : s.matched_count === null) && Array.isArray(s.matched_ids) &&
      s.matched_ids.length > 0 && (s.matched_count === null || s.matched_ids.length <= s.matched_count) &&
      new Set(s.matched_ids).size === s.matched_ids.length && s.matched_ids.every(id => eventID(id) &&
        id.split('.').slice(1,3).join('.') === s.id.slice(2)), 'Malformed matched session/event identities.');
  }
  verifyCoverage(result.coverage, {allowAuditPending});
  return result.sessions.map(s => [s.id, s.matched_count, s.matched_ids]);
};

export function verifyCoverage(coverage, {allowAuditPending = false} = {}) {
  const c = coverage, i = c?.index;
  requireProof(Array.isArray(c?.gaps) && c.gaps.every(g => typeof g === 'string') && i &&
    positive(i.known_sources) && i.decoded_sources === i.known_sources && i.searchable_sources === i.known_sources &&
    nonnegative(i.decoded_events) && i.decoded_events === i.searchable_events &&
    !c.gaps.some(g => !(allowAuditPending && g === 'source_audit_pending') &&
      /pending|unavailable|storage_low|unreadable|cancelled|discovery_incomplete|source_changed_during_query/.test(g)),
    'Actual query preparation/read coverage is incomplete.');
}

// Only a bounded matched record is read here. Full DTO/pair/metadata equality
// remains in the Go migrator; this corroborates the actual raw source locator.
export function verifyRaw(raw, focus, catalog) {
  const r = raw.result, e = r?.events?.find(e => e.id === focus), c = r?.raw_chunk;
  requireProof(r?.focus_id === focus && e && c?.encoding === 'base64' && typeof c.data === 'string' &&
    /^[A-Za-z0-9+/]+={0,2}$/.test(c.data) && c.data.length % 4 === 0 && c.offset === 0,
    'Malformed canonical raw result.');
  const bytes = Buffer.from(c.data, 'base64');
  requireProof(bytes.toString('base64') === c.data && bytes.length > 0 && positive(c.total_bytes), 'Invalid base64 raw bytes.');
  const ref = e.source, parts = focus.split('.'), source = catalog.find(s => s.id === parts[1]);
  requireProof(source && source.generation===parts[2] && ref.id === parts[1] && ref.generation === parts[2] && ref.offset === parseInt(parts[3],36) &&
    ref.block === Number(parts[4]) && ref.digest === parts[5] && nonnegative(ref.offset) &&
    positive(ref.length) && c.total_bytes === ref.length && bytes.length <= ref.length &&
    hash(Buffer.from(source.agent + '\0' + source.path)).slice(0,16) === source.id,
    'Raw result lost its actual source identity/locator.');
  const fd = openSync(source.path, 'r'), before = fstatSync(fd), fullHash = createHash('sha256');
  try {
    requireProof(source.file && sameFile(source.file,before) && before.isFile() &&
      nonnegative(source.file.bytes) && before.size>=source.file.bytes && before.size >= ref.offset + ref.length, 'Raw source is unavailable.');
    const buffer = Buffer.alloc(64 * 1024);
    let offset = 0;
    while (offset < ref.length) {
      const n = readSync(fd, buffer, 0, Math.min(buffer.length, ref.length-offset), ref.offset+offset);
      requireProof(n > 0, 'Raw source ended early.');
      fullHash.update(buffer.subarray(0,n));
      const overlap = Math.min(n, Math.max(0, bytes.length-offset));
      requireProof(!overlap || buffer.subarray(0,overlap).equals(bytes.subarray(offset,offset+overlap)), 'Raw bytes differ from source.');
      offset += n;
    }
    requireProof(fullHash.digest('hex').slice(0,16) === ref.digest, 'Full raw source-record digest differs.');
    const after = fstatSync(fd), path = statSync(source.path);
    requireProof(sameFile(before,after) && sameFile(after,path) && before.size === after.size &&
      before.mtimeMs === after.mtimeMs && path.size === after.size && path.mtimeMs === after.mtimeMs,
      'Raw source changed during verification.');
  } finally { closeSync(fd); }
  return hash(bytes);
}

export function observeFrames(record, read, binding, from, to, phases = []) {
  requireProof(record.binary_sha256 === binding.binary_sha256 && record.input_sha256 === binding.input_sha256 &&
    record.database_path === binding.database_path && record.input_identity === binding.input_identity,
    'Observation window belongs to another candidate/store/data lineage.');
  const frames = read(record.frames).trim().split('\n').filter(Boolean).map(JSON.parse);
  requireProof(frames.length >= 2 && frames.every((frame,i)=>frame.binary_sha256===binding.binary_sha256 &&
    frame.input_sha256===binding.input_sha256 && frame.database_path===binding.database_path &&
    frame.input_identity===binding.input_identity && timestamp(frame.at)>=from && timestamp(frame.at)<=to &&
    (!i || timestamp(frame.at)>=timestamp(frames[i-1].at))), 'Unbound or unordered actual window frames.');
  requireProof(timestamp(frames[0].at)===from && timestamp(frames.at(-1).at)===to &&
    phases.every(phase=>frames.some(f=>f.phase===phase)), 'Actual critical/start/end window frames are missing.');
  for(const frame of frames) {
    const output=read(frame.observation), measured=JSON.parse(output);
    requireProof(measured.command==='readonly-source-checkpoint-window' && measured.at===frame.at &&
      measured.database_path===binding.database_path && sameFile(measured.target_file,frame.target_file) &&
      measured.target_file.sha256===frame.target_file.sha256 &&
      measured.allocated_bytes===frame.allocated_bytes && measured.available_bytes===frame.available_bytes &&
      ['shadow_allocated_bytes','journal_allocated_bytes','swap_allocated_bytes','whole_shadows'].every(key=>
        !(key in frame) || (nonnegative(frame[key]) && measured[key]===frame[key])),
      'Frame is not bound to its recorded physical observation.');
    if(frame.readiness) {
      const ready=JSON.parse(read(frame.readiness));
      requireProof(ready.at===frame.at && ready.binary_sha256===binding.binary_sha256 &&
        ready.input_sha256===binding.input_sha256 && ready.database_path===binding.database_path &&
        ready.input_identity===binding.input_identity && !ready.error && !ready.result?.error &&
        ready.selector?.collection==='sessions' && ready.selector.limit===1 && Object.keys(ready.selector).length===2,
        'Actual Go preparation observation is missing or belongs to another frame.');
      verifyCoverage(ready.result?.coverage);
    }
    if(!frame.checkpoint) continue; // Capacity preflight may inspect the original schema.
    const checkpoint=read(frame.checkpoint), rows=checkpoint.trim().split('\n').filter(Boolean).map(JSON.parse);
    requireProof(hash(Buffer.from(checkpoint))===frame.checkpoint_sha256 && rows.length>0 &&
      rows.every(r=>/^[a-f0-9]{16}$/.test(r.id) && /^[a-f0-9]{16}$/.test(r.generation) &&
        (r.complete===0 || r.complete===1) && nonnegative(r.search_count) &&
        typeof r.checkpoint==='string' && r.checkpoint.length>0 &&
        Buffer.from(r.checkpoint,'base64').toString('base64')===r.checkpoint) &&
      new Set(rows.map(r=>r.id)).size===rows.length && measured.checkpoint_sha256===frame.checkpoint_sha256 &&
      frame.sources===rows.length && frame.pending_sources===rows.filter(r=>!r.complete).length &&
      frame.appended_events===rows.reduce((sum,r)=>sum+r.search_count,0),
      'Frame checkpoint/source counters differ from actual sealed rows.');
  }
  return frames;
}

export function stableFileDigest(path) {
  const fd=openSync(path,'r'), before=fstatSync(fd), digest=createHash('sha256'), bytes=Buffer.alloc(64*1024);
  try {
    let total=0,n;
    while((n=readSync(fd,bytes,0,bytes.length,null))>0){digest.update(bytes.subarray(0,n));total+=n;}
    const after=fstatSync(fd), current=statSync(path);
    requireProof(sameFile(before,after) && sameFile(after,current) && before.size===after.size &&
      before.mtimeMs===after.mtimeMs && after.size===current.size && after.mtimeMs===current.mtimeMs &&
      total===before.size, 'Actual final target changed during digest observation.');
    return digest.digest('hex');
  } finally { closeSync(fd); }
}

export function sealedReceipt(root, reference) {
  requireProof(reference?.path && sha(reference.sha256), 'Missing sealed evidence reference.');
  const path = resolve(root, reference.path);
  requireProof(existsSync(path), 'Evidence file disappeared: ' + reference.path);
  const bytes = readFileSync(path);
  requireProof(hash(bytes) === reference.sha256, 'Evidence file changed: ' + reference.path);
  return bytes.toString('utf8');
}

export function verifyProtected(original, after, approvedAbsent = []) {
  const absent = new Map(approvedAbsent.map(f => [f.path, f]));
  requireProof(Array.isArray(original) && Array.isArray(after) &&
    absent.size === approvedAbsent.length &&
    approvedAbsent.every(f => original.some(o => o.path === f.path && o.bytes === f.bytes && o.sha256 === f.sha256)) &&
    new Set(original.map(f=>f.path)).size === original.length && after.length === original.length &&
    new Set(after.map(f=>f.path)).size === after.length && original.every(f=>{
      const observed=after.find(x=>x.path===f.path);
      if (absent.has(f.path)) {
        return sha(f.sha256) && nonnegative(f.bytes) && observed?.bytes === f.bytes &&
          observed.missing === true && observed.prefix_sha256 === null && observed.current_bytes === null &&
          !existsSync(f.path);
      }
      return typeof f.path==='string' && sha(f.sha256) && nonnegative(f.bytes) && observed &&
        observed.bytes===f.bytes && observed.prefix_sha256===f.sha256 && observed.missing===false &&
        nonnegative(observed.current_bytes) && observed.current_bytes>=f.bytes;
    }), 'Protected file/prefix inventory is incomplete.');
}
export const requestMatches=(selector,text,count)=> selector &&
  Object.keys(selector).length===4 && selector.collection==='sessions' && selector.text===text &&
  selector.limit===20 && selector.count===count;

export function verifySigning(build, signing, binary, candidate, read) {
  requireProof(signing.before_sha256===build.binary_sha256 && signing.after_sha256===binary &&
    signing.candidate_input_sha256===candidate && signing.exit_code===0 &&
    signing.command==='codesign --force --sign -' && read(signing.output).length>0 &&
    timestamp(signing.at)>=timestamp(build.finished_at),
    'Signed executable is not linked to the actual build.');
}

export function verifySourceStore(root, p) {
  const receipt = reference => sealedReceipt(root,reference);
  const json = reference => JSON.parse(receipt(reference));
  requireProof(p.version === 1 && p.input_sha256 === sourceStorageInputDigest(root),
    'Source-store evidence belongs to another candidate.');
  const history = canonicalHistory(), database = history + '.trajectory/trajectory.sqlite';
  requireProof(p.history_file === history && p.database_path === database && realpathSync(database) === database,
    'Whole-corpus acceptance must inspect the actual production history/store.');
  const installed = '/Applications/Agent Load.app/Contents/MacOS/agentload';
  requireProof(sha(p.binary_sha256) && existsSync(installed) &&
    fileDigest(installed) === p.binary_sha256 &&
    fileDigest(join(root, 'dist/Agent Load.app/Contents/MacOS/agentload')) === p.binary_sha256,
    'Installed and built candidates differ.');

  const build = json(p.build);
  requireProof(sha(build.binary_sha256) &&
    build.candidate_input_sha256 === candidateInputDigest(root) && build.exit_code === 0 &&
    build.command === './build_macos_app.sh' && receipt(build.output).includes('Agent Load.app'), 'Current binary lacks its actual build/input binding.');
  verifySigning(build,json(p.signing),p.binary_sha256,candidateInputDigest(root),receipt);
  const corpus = json(p.before), maintenance = json(p.maintenance);
  const absence = p.external_source_absence ? json(p.external_source_absence) : null;
  const absentSessions = absence?.sessions ?? [];
  if (absence) {
    const observed = json(absence.observed_absence), counts = json(absence.original_fact_counts);
    requireProof(absence.decision === 'cutover_with_retained_facts_and_missing_raw' &&
      absence.user_instruction === '切换吧' && absence.before_sha256 === corpus.protected_files.sha256 &&
      absence.original_sha256 === corpus.file.sha256 && timestamp(absence.at) >= timestamp(corpus.at) &&
      Number.isFinite(Date.parse(observed.at)) && Date.parse(observed.at) <= timestamp(absence.at) &&
      Array.isArray(observed.missing) && new Set(observed.missing).size === observed.missing.length &&
      observed.missing.length === absentSessions.length &&
      counts.command === 'readonly-original-source-fact-counts' && counts.database_path === database &&
      counts.original_sha256 === corpus.file.sha256 && sameFile(counts.original_file, corpus.file) &&
      counts.original_file.bytes === corpus.bytes && counts.original_file.mtime_ns === String(corpus.file.mtime_ns) &&
      counts.total_facts === corpus.events && Array.isArray(counts.sources) &&
      counts.sources.length === corpus.sources.length &&
      new Set(counts.sources.map(s => s.id)).size === counts.sources.length &&
      counts.sources.reduce((n,s) => n + s.fact_count, 0) === corpus.events &&
      Array.isArray(absentSessions) && absentSessions.length > 0 &&
      new Set(absentSessions.map(s => s.id)).size === absentSessions.length &&
      absentSessions.every(s => s.agent === 'claude' && nonnegative(s.fact_count) &&
        observed.missing.includes(s.path) &&
        corpus.sources.some(o => o.id === s.id && o.generation === s.generation && o.path === s.path && o.agent === s.agent) &&
        counts.sources.some(o => o.id === s.id && o.generation === s.generation && o.fact_count === s.fact_count)),
      'Missing raw exceptions lack a bound, explicit cutover decision.');
  }
  requireProof(corpus.history_file === history && corpus.database_path === database && corpus.scope === 'whole_original_production_store' &&
    Array.isArray(corpus.sources) && corpus.sources.length > 0 &&
    corpus.sources.every(s => /^[a-f0-9]{16}$/.test(s.id)) &&
    new Set(corpus.sources.map(s => s.id)).size === corpus.sources.length && positive(corpus.events) &&
    positive(corpus.bytes) && positive(corpus.metadata_rows), 'Whole original corpus inventory is incomplete.');
  requireProof(corpus.input_identity===`${corpus.file.dev}:${corpus.file.ino}:${corpus.bytes}:${corpus.file.mtime_ns}` &&
    sha(corpus.file.sha256) && corpus.file.path===database, 'Original physical identity is not bound to the complete corpus.');
  requireProof(maintenance.exit_code === 0 && maintenance.binary_sha256 === p.binary_sha256 &&
    maintenance.input_sha256 === p.input_sha256 && maintenance.resumed === true &&
    maintenance.originals_preserved_until_verified === true && maintenance.runtime_owner_enforced === true &&
    maintenance.history_file === history && maintenance.database_path === database &&
    maintenance.input_identity === corpus.input_identity &&
    [ ['traj','compact','--history-file',history],
      ['traj','compact','--history-file',history,'--expected-input-sha256',corpus.file.sha256]
    ].some(args => JSON.stringify(maintenance.arguments) === JSON.stringify(args)) &&
    timestamp(maintenance.started_at) >= timestamp(corpus.at) && timestamp(maintenance.finished_at) >= timestamp(maintenance.started_at),
    'Actual resumable, exclusive and preserving maintenance evidence incomplete.');
  const progress = receipt(maintenance.progress).trim().split('\n').filter(Boolean).map(JSON.parse);
  const ready = progress.at(-1);
  const sealed = progress.findLast(x => x.phase === 'sealed');
  requireProof(ready?.phase === 'ready' && positive(ready.records) &&
    positive(ready.bytes_before) && positive(ready.bytes_after), 'No final real maintenance ready record.');
  requireProof(sealed && sha(sealed.target_sha256) && sealed.target_sha256 === ready.target_sha256 &&
    ready.target_sha256 === maintenance.target_file.sha256 && sealed.records === ready.records &&
    sealed.source_count === ready.source_count && ready.source_count === corpus.sources.length &&
    sealed.input_identity === ready.input_identity && ready.input_identity === corpus.input_identity &&
    sealed.input_sha256 === corpus.file.sha256 && ready.input_sha256 === corpus.file.sha256 &&
    /^volume-v1:[a-f0-9]{32}:\d+:\d+:\d+$/.test(ready.persistent_input_identity) &&
    sealed.persistent_input_identity === ready.persistent_input_identity &&
    ready.records === corpus.events && ready.bytes_before === corpus.bytes &&
    maintenance.target_file.bytes === ready.bytes_after,
    'Original and independently verified full-corpus cardinalities differ.');

  requireProof(p.database_path && existsSync(p.database_path), 'Actual target database is absent.');
  for (const path of [p.database_path + '.source-migrating', p.database_path + '.source-legacy',
    p.database_path + '.source-ready.json', join(resolve(p.database_path, '..'), 'index.bbolt'),
    join(resolve(p.database_path, '..'), 'search.sqlite')]) {
    requireProof(!existsSync(path), 'An old or unfinished storage structure remains: ' + path);
  }
  const fileBefore = statSync(database);
  requireProof(sameFile(fileBefore,maintenance.target_file), 'Current file is not the verified migration target.');
  const db = new DatabaseSync(p.database_path, { readOnly: true });
  let sourceCount, readableSourceCount;
  try {
    db.exec('PRAGMA query_only=ON; PRAGMA busy_timeout=200; PRAGMA cache_size=-4096; BEGIN;');
    requireProof(db.prepare('PRAGMA user_version').get().user_version === 2,
      'Actual store is not source-backed schema 2.');
    const tables = db.prepare("SELECT name FROM sqlite_schema WHERE type='table' ORDER BY name").all();
    requireProof(JSON.stringify(tables.map(t => t.name)) ===
      JSON.stringify(['exceptions', 'meta', 'metadata', 'ranges', 'sources']),
      'Full event, FTS or temporary proof tables remain in the target.');
    const integrity=db.prepare('PRAGMA quick_check').all();
    requireProof(integrity.length>0 && integrity.every(row => row.quick_check === 'ok') &&
      db.prepare('PRAGMA foreign_key_check').all().length === 0, 'Actual SQLite integrity check failed.');
    const state = JSON.parse(db.prepare("SELECT value FROM meta WHERE key='source_migration'").get()?.value ?? '{}');
    requireProof(state.version === 2 && state.phase === 'ready' && state.input === maintenance.input_identity &&
      state.records === ready.records && state.bytes_before === ready.bytes_before &&
      state.input_identity === ready.persistent_input_identity && state.input_sha256 === corpus.file.sha256 &&
      state.identities_complete === true,
      'Maintenance and committed migration state differ.');
    const ids = new Set(db.prepare('SELECT id FROM sources').all().map(s=>s.id));
    sourceCount = ids.size;
    readableSourceCount = db.prepare('SELECT count(*) AS n FROM sources WHERE active=1 AND missing=0').get().n;
    requireProof(corpus.sources.every(s=>ids.has(s.id)) &&
      db.prepare('SELECT count(*) AS n FROM metadata').get().n >= corpus.metadata_rows,
      'Original source membership or opaque recovery metadata was lost.');
    for (const source of absentSessions) {
      const stored = db.prepare('SELECT rowid,generation,missing FROM sources WHERE id=? AND generation=?').get(source.id, source.generation);
      requireProof(stored?.missing === 1 &&
        db.prepare('SELECT count(*) AS n FROM exceptions WHERE source=?').get(stored.rowid).n === source.fact_count &&
        db.prepare('SELECT count(*) AS n FROM ranges WHERE source=?').get(stored.rowid).n === 0,
        'Missing raw source lost original facts or remains presented as reconstructible.');
    }
    requireProof(sameFile(fileBefore,statSync(database)), 'Target pathname changed during read snapshot.');
    db.exec('COMMIT;');
  } finally { db.close(); }

  const currentBytes = ['','-journal','-wal','-shm'].reduce((sum, suffix) => sum + allocated(p.database_path + suffix), 0);
  requireProof(sameFile(fileBefore,statSync(database)), 'Sized target differs from the inspected snapshot.');
  requireProof(positive(currentBytes) && ready.bytes_after < 2 ** 30 && currentBytes < 2 ** 30 &&
    currentBytes < ready.bytes_before, 'Actual complete extra index has not reached the hundreds-of-MiB target.');
  const protectedFiles = json(p.protected_files), beforeFiles = json(corpus.protected_files);
  requireProof(protectedFiles.before_sha256 === corpus.protected_files.sha256 &&
    protectedFiles.input_sha256 === p.input_sha256 && protectedFiles.database_path === database &&
    timestamp(protectedFiles.at)>=timestamp(maintenance.finished_at),
    'Protected-file comparison belongs to another baseline/candidate/store.');
  const sourceCatalog = json(p.source_catalog);
  requireProof(sourceCatalog.database_path === database && sourceCatalog.input_sha256 === p.input_sha256 &&
    Array.isArray(sourceCatalog.sources) && sourceCatalog.sources.length>=corpus.sources.length &&
    new Set(sourceCatalog.sources.map(s=>s.id)).size===sourceCatalog.sources.length,
    'Raw source catalog is unbound/incomplete.');
  for (const kind of ['sessions', 'history', 'usage', 'annotations']) {
    verifyProtected(beforeFiles[kind],protectedFiles[kind],kind === 'sessions' ? absentSessions : []);
  }
  requireProof(beforeFiles.sessions.length>0 && Array.isArray(corpus.raw_sources) && corpus.raw_sources.length>0 &&
    new Set(corpus.raw_sources.map(s=>s.path)).size===corpus.raw_sources.length &&
    corpus.raw_sources.every(s=>beforeFiles.sessions.some(f=>f.path===s.path && f.bytes===s.bytes && f.sha256===s.sha256)),
    'Complete original raw-file inventory is missing from protection.');
  const protectedPaths=new Set(Object.values(beforeFiles).flat().filter(f=>f?.path).map(f=>f.path));
  for(const source of sourceCatalog.sources) {
    if(existsSync(source.path) && statSync(source.path).birthtimeMs<=timestamp(corpus.at)) {
      requireProof(protectedPaths.has(source.path), 'Existing original session omitted from protection: '+source.path);
    }
  }
  for(const path of [history,history+'.throughput-index/usage.bbolt',join(resolve(database,'..'),'annotations.bbolt')]) {
    if(existsSync(path) && statSync(path).birthtimeMs<=timestamp(corpus.at)) {
      requireProof(protectedPaths.has(path), 'Existing original useful file omitted from protection: '+path);
    }
  }

  const observations = (p.queries ?? []).map(json);
  for (const text of ['research', 'bagakit-researcher']) {
    for (const state of ['cold', 'warm']) {
      const pair = ['cli', 'rpc'].map(transport => {
        const q = observations.find(x => x.text === text && x.state === state && x.transport === transport);
        requireProof(q && q.binary_sha256 === p.binary_sha256 && q.limit === 20 && q.count === false &&
          Number.isFinite(q.elapsed_ms) && q.elapsed_ms >= 0 && q.elapsed_ms < 6000 &&
          !q.error && !q.result?.error && !Object.hasOwn(q.result ?? {}, 'matched_total'),
          `Actual default-20 ${text}/${state}/${transport} query failed.`);
        requireProof(q.database_path === database && q.input_identity === corpus.input_identity &&
          sameFile(q.target_file,maintenance.target_file) && timestamp(q.at)>=timestamp(maintenance.finished_at) &&
          timestamp(q.at)>=timestamp(protectedFiles.at) &&
          requestMatches(q.selector,text,false),
          'Query belongs to another target/data lineage/request.');
        const identity = pageIdentity(q.result), sessions = q.result.sessions;
        const raw = json(q.raw);
        requireProof(raw.binary_sha256 === p.binary_sha256 &&
          raw.result?.focus_id === sessions[0].matched_ids[0] &&
          raw.result?.raw_chunk?.encoding === 'base64' && raw.result.raw_chunk.data.length > 0 &&
          !raw.error && !raw.result.error, 'Matched canonical raw evidence is unavailable.');
        const rawDigest = verifyRaw(raw,sessions[0].matched_ids[0],sourceCatalog.sources);
        return {identity,rawDigest};
      });
      requireProof(JSON.stringify(pair[0]) === JSON.stringify(pair[1]),
        `CLI/RPC identities or selected-session counts differ: ${text}/${state}.`);
    }
  }
  const counts = (p.explicit_counts ?? []).map(json);
  for (const text of ['research', 'bagakit-researcher']) {
    const pair = ['cli','rpc'].map(transport => {
      const q = counts.find(x => x.text === text && x.transport === transport);
      requireProof(q?.binary_sha256 === p.binary_sha256 && q.count === true && !q.error &&
        !q.result?.error && positive(q.result?.matched_total) &&
        Number.isFinite(q.elapsed_ms) && q.elapsed_ms >= 0 && q.elapsed_ms < 60000,
        'Explicit exact count evidence incomplete.');
      requireProof(q.limit===20 && q.database_path===database && q.input_identity===corpus.input_identity &&
        sameFile(q.target_file,maintenance.target_file) && timestamp(q.at)>=timestamp(maintenance.finished_at) &&
        requestMatches(q.selector,text,true),
        'Explicit count scope/lineage/request differs.');
      const identity = pageIdentity(q.result, {count:true});
      requireProof(q.result.matched_total >= q.result.sessions.length, 'Exact total cannot be smaller than its page.');
      const warm = observations.find(x => x.text === text && x.state === 'warm' && x.transport === transport);
      requireProof(JSON.stringify(q.result.sessions.map((s,i) => [s.id, s.matched_ids.slice(0,warm.result.sessions[i]?.matched_ids.length ?? 0)])) ===
        JSON.stringify(warm.result.sessions.map(s => [s.id, s.matched_ids])),
        'Explicit count changed the default matched page.');
      return [q.result.matched_total, identity];
    });
    requireProof(JSON.stringify(pair[0]) === JSON.stringify(pair[1]), 'Explicit CLI/RPC counts differ.');
  }

  const growth = json(p.post_catchup);
  const start = timestamp(growth.start_at), end = timestamp(growth.end_at), duration = (end-start)/1000;
  const binding={binary_sha256:p.binary_sha256,input_sha256:p.input_sha256,database_path:database,input_identity:corpus.input_identity};
  const frames=observeFrames(growth,receipt,binding,start,end);
  requireProof(start>=Math.max(timestamp(maintenance.finished_at),...observations.map(q=>timestamp(q.at)),...counts.map(q=>timestamp(q.at))) &&
    frames.every(f=>f.checkpoint && f.readiness && sameFile(f.target_file,maintenance.target_file) && f.pending_sources===0 &&
      positive(f.sources) && positive(f.allocated_bytes) && nonnegative(f.appended_events) && sha(f.checkpoint_sha256)) &&
    growth.start_allocated_bytes===frames[0].allocated_bytes && growth.end_allocated_bytes===frames.at(-1).allocated_bytes &&
    growth.appended_events===frames.at(-1).appended_events-frames[0].appended_events,
    'Growth summary differs from real same-lineage post-query checkpoint frames.');
  requireProof(growth.binary_sha256 === p.binary_sha256 && duration >= 300 &&
    growth.pending_sources_start === 0 && growth.pending_sources_end === 0 &&
    growth.sources === readableSourceCount && growth.sources <= sourceCount && growth.sources===frames.at(-1).sources &&
    positive(growth.start_allocated_bytes) && positive(growth.end_allocated_bytes) &&
    nonnegative(growth.appended_events) && growth.catalog_rechecks===frames.length-1 &&
    (growth.appended_events!==0 || frames[0].checkpoint_sha256===frames.at(-1).checkpoint_sha256) &&
    growth.unchanged_sources_rewritten === 0,
    'Catch-up completion and actual subsequent 300-second increment window are missing.');
  const finalFrame=frames.at(-1);
  requireProof(sha(finalFrame.target_file.sha256) && stableFileDigest(database)===finalFrame.target_file.sha256,
    'Actual stable target differs from the final observed source-store seal.');
  const capacity = json(p.capacity);
  const samples=observeFrames(capacity,receipt,binding,timestamp(capacity.start_at),timestamp(capacity.end_at),['preflight','sealed','ready']);
  requireProof(timestamp(capacity.start_at)<=timestamp(maintenance.started_at) && timestamp(capacity.end_at)>=timestamp(maintenance.finished_at) &&
    samples.every(s => nonnegative(s.available_bytes) && s.available_bytes >= 2 ** 30 &&
      nonnegative(s.shadow_allocated_bytes) && nonnegative(s.journal_allocated_bytes) &&
      nonnegative(s.swap_allocated_bytes) && nonnegative(s.whole_shadows) && s.whole_shadows <= 1),
    'Actual peak-space samples or preserving capacity refusal evidence incomplete.');
  for(const reference of [capacity.ordinary_refusal,capacity.migration_refusal]) {
    const refusal=json(reference), output=receipt(refusal.output);
    requireProof(refusal.candidate_input_sha256===candidateInputDigest(root) && refusal.exit_code===0 &&
      /PASS/.test(output) && sha(refusal.before_checkpoint_sha256) &&
      refusal.before_checkpoint_sha256===refusal.after_checkpoint_sha256 &&
      sha(refusal.before_original_sha256) && refusal.before_original_sha256===refusal.after_original_sha256 &&
      refusal.actual_rejection===true, 'Preserving refusal lacks its actual candidate-bound output/checkpoint evidence.');
  }
  requireProof(sourceStorageInputDigest(root)===p.input_sha256 && fileDigest(installed)===p.binary_sha256 &&
    sameFile(fileBefore,statSync(database)), 'Candidate or target changed before final publication.');
  console.log(`Verified installed source-backed store: ${(ready.bytes_before / 2 ** 30).toFixed(2)} GiB -> ${(currentBytes / 2 ** 20).toFixed(1)} MiB; ${sourceCount} retained sources, ${absentSessions.length} approved missing raw sessions with original facts retained, remaining protected prefixes preserved, exact CLI/RPC/raw and actual ${duration.toFixed(0)}s growth window.`);
}

if (process.argv[1] === new URL(import.meta.url).pathname) {
  const root = resolve(import.meta.dirname, '..');
  const path = join(root, '.bagakit/feature-tracker/features/f-22duuagpj/artifacts/storage-source-acceptance.json');
  requireProof(existsSync(path), 'Missing actual installed whole-corpus source-store evidence.');
  const proof=JSON.parse(readFileSync(path,'utf8'));
  if([2,3].includes(proof.version)) verifySourceBlockUpgrade(root,proof); else verifySourceStore(root,proof);
}
