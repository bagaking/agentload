import { existsSync, readFileSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { join, resolve } from 'node:path';
import { spawnSync } from 'node:child_process';
import { candidateInputDigest, fileDigest } from './trajectory_acceptance_inputs.mjs';

export function unifiedStorageInputDigest(root) {
  const hash = createHash('sha256').update(candidateInputDigest(root));
  for (const path of ['build_macos_app.sh', 'docs/trajectory-requirements.md',
    'docs/api-reference.md', 'scripts/verify_trajectory_unified.mjs']) {
    hash.update(path + '\0').update(readFileSync(join(root, path))).update('\0');
  }
  return hash.digest('hex');
}

function requireProof(condition, message) {
  if (!condition) throw Error(message);
}
const sha = value => /^[a-f0-9]{64}$/.test(value ?? '');
const positive = value => Number.isSafeInteger(value) && value > 0;

if (process.argv[1] === new URL(import.meta.url).pathname) {
  const root = resolve(import.meta.dirname, '..');
  const path = join(root, '.bagakit/feature-tracker/features/f-22duuagpj/artifacts/storage-unified-acceptance.json');
  requireProof(existsSync(path), 'Missing installed whole-corpus unified storage proof.');
  const p = JSON.parse(readFileSync(path, 'utf8'));
  requireProof(p.version === 3 && p.input_sha256 === unifiedStorageInputDigest(root), 'Proof belongs to another implementation.');
  requireProof(sha(p.installed_binary_sha256) &&
    p.installed_binary_sha256 === p.query_binary_sha256 && p.installed_binary_sha256 ===
    fileDigest('/Applications/Agent Load.app/Contents/MacOS/agentload'), 'Installed, migrated and queried candidates differ.');
  const maintenancePath = resolve(root, p.maintenance_receipt ?? 'missing');
  requireProof(existsSync(maintenancePath) && fileDigest(maintenancePath) === p.maintenance_receipt_sha256,
    'Full-store maintenance receipt is not sealed.');
  const maintenance = JSON.parse(readFileSync(maintenancePath));
  requireProof(maintenance.exit_code === 0 && maintenance.binary_sha256 === p.maintenance_binary_sha256,
    'Full-store maintenance did not succeed on its recorded candidate.');
  if (p.maintenance_binary_sha256 !== p.installed_binary_sha256) {
    const reuse = p.maintenance_reuse;
    requireProof(reuse?.target_binary_sha256 === p.installed_binary_sha256 &&
      reuse.target_input_sha256 === p.input_sha256 && reuse.base_input_sha256 === maintenance.input_sha256 &&
      reuse.same_migration_lineage && sha(reuse.delta_sha256), 'Storage proof reuse lineage incomplete.');
    const basePath = resolve(root, reuse.base_manifest ?? 'missing');
    requireProof(existsSync(basePath) && fileDigest(basePath) === reuse.base_manifest_sha256,
      'Storage proof baseline manifest is not sealed.');
    const base = JSON.parse(readFileSync(basePath));
    requireProof(base.binary_sha256 === maintenance.binary_sha256 && base.input_sha256 === maintenance.input_sha256,
      'Storage proof baseline belongs to another maintenance candidate.');
    const inventory = spawnSync('git', ['ls-files', '-c', '-o', '--exclude-standard', '-z'], {cwd: root});
    requireProof(inventory.status === 0, 'Cannot inventory storage proof inputs.');
    const currentPaths = [...new Set(inventory.stdout.toString().split('\0').filter(Boolean))].filter(path =>
      existsSync(join(root, path)) && (/\.(go|m|h)$/.test(path) || path === 'go.mod' || path === 'go.sum' ||
        path.startsWith('ui/src/') || path.startsWith('ui/dist/') ||
        /^ui\/(package.*json|tsconfig.*json|vite.config.*)$/.test(path) || path.startsWith('macos/')));
    currentPaths.push('build_macos_app.sh', 'docs/trajectory-requirements.md', 'docs/api-reference.md',
      'scripts/verify_trajectory_unified.mjs');
    requireProof(JSON.stringify([...new Set(currentPaths)].sort()) === JSON.stringify(Object.keys(base.files).sort()),
      'Storage proof input inventory changed; a new full-store proof is required.');
    const allowed = new Set(['internal/trajectory/store_search.go', 'internal/trajectory/watch.go',
      'internal/trajectory/search.go', 'internal/trajectory/knowledge_test.go',
      'internal/trajectory/search_test.go', 'internal/trajectory/watch_test.go',
      'internal/trajectory/store_blocks.go', 'internal/trajectory/search_text_test.go',
      'evidence_index.go', 'evidence_index_test.go', 'internal/trajectory/store.go',
      'tray.go', 'tray_test.go', 'trajectory_rpc.go', 'internal/trajectory/service.go',
      'transcripts.go',
      'trajectory_claude_test.go',
      'internal/trajectory/store_test.go', 'internal/trajectory/index.go',
      'internal/trajectory/store_migration.go', 'internal/trajectory/store_migration_test.go',
      'internal/trajectory/storage_maintenance.go',
      'docs/trajectory-requirements.md', 'docs/api-reference.md',
      'internal/snapshot/trajectory.go', 'internal/trajectory/catalog.go',
      'internal/trajectory/recovery_test.go', 'internal/trajectory/recovery.go', 'internal/trajectory/service_test.go',
      'internal/trajectory/entities.go', 'internal/trajectory/knowledge.go',
      'internal/trajectory/relations.go', 'internal/trajectory/context.go',
      'internal/trajectory/attention.go', 'trajectory_cli.go', 'trajectory_read_path_test.go',
      'ui/src/knowledge/trajectoryApi.ts', 'scripts/verify_trajectory_unified.mjs']);
    const delta = [];
    for (const [path, before] of Object.entries(base.files).sort(([a], [b]) => a.localeCompare(b))) {
      const absolute = join(root, path);
      requireProof(existsSync(absolute), 'Baseline input disappeared: ' + path);
      const after = fileDigest(absolute);
      if (before !== after) {
        requireProof(allowed.has(path), 'Unreviewed storage proof input changed: ' + path);
        delta.push({path, before, after});
      }
    }
    requireProof(JSON.stringify(delta) === JSON.stringify(reuse.delta_files) &&
      createHash('sha256').update(JSON.stringify(delta)).digest('hex') === reuse.delta_sha256,
      'Storage proof delta does not match the current exact inputs.');
    const reviewPath = resolve(root, reuse.review_receipt ?? 'missing');
    requireProof(existsSync(reviewPath) && fileDigest(reviewPath) === reuse.review_receipt_sha256,
      'Storage proof reuse lacks a sealed review.');
    const review = JSON.parse(readFileSync(reviewPath));
    requireProof(review.storage_inert && review.delta_sha256 === reuse.delta_sha256 &&
      review.target_input_sha256 === p.input_sha256 && !review.p0_p1,
      'Storage layout, writes, migration, encoding, transactions, capacity or verifier changes cannot reuse this proof.');
  }
  const m = p.migration;
  requireProof(m?.complete && m.resumed && m.old_structures_retired && m.originals_preserved_until_verified &&
    m.full_event_identity_equal && m.full_checkpoint_equal && m.full_pair_identity_equal &&
    m.full_source_state_equal && m.search_ready_boundary_equal && m.recovery_metadata_equal &&
    sha(m.logical_digest) && positive(m.records) && m.records === m.verified_records,
  'Whole migration identity, recovery or preservation proof incomplete.');
  for (const kind of ['sessions', 'history', 'usage', 'annotations']) {
    const x = p.protected?.[kind];
    const accountedFiles = positive(x?.files) || (kind === 'annotations' && x?.files === 0 && x.absence_preserved);
    requireProof(x?.all_files_preserved && accountedFiles && sha(x.before_digest) && x.before_digest === x.after_digest,
      'Missing protected ' + kind + ' preservation proof.');
  }
  requireProof(p.layout?.schema === 1 && p.layout.page_size === 4096 && positive(p.layout.before_bytes) &&
    positive(p.layout.after_bytes) && p.layout.after_bytes < p.layout.before_bytes &&
    p.layout.single_body && p.layout.single_entity_facts, 'Missing actual unified layout reduction.');
  requireProof(p.frozen_queries?.length >= 2 && ['research', 'bagakit-researcher'].every(text =>
    p.frozen_queries.some(q => q.text === text)) && p.frozen_queries.every(q => positive(q.events_before) &&
    q.events_before === q.events_after && q.sessions_before === q.sessions_after &&
    sha(q.identity_before) && q.identity_before === q.identity_after), 'Frozen whole-corpus query identities differ.');
  const queries = p.runtime_queries;
  requireProof(queries?.length >= 8 && ['research', 'bagakit-researcher'].every(text =>
    ['cold', 'warm'].every(state => ['cli', 'rpc'].every(transport =>
      queries.some(q => q.text === text && q.state === state && q.transport === transport)))) && queries.every(q =>
    !q.error && q.limit === 20 && q.count === false && q.matched_total === undefined &&
    positive(q.returned_items) && Number.isFinite(q.elapsed_ms) && q.elapsed_ms >= 0 &&
    q.elapsed_ms < 6000 && sha(q.identity_digest)) && p.cli_rpc_identity_equal && p.raw_available,
  'Installed default-20 cold/warm CLI/RPC exact page and raw proof incomplete.');
  const decision = resolve(root, p.query_contract_decision ?? 'missing');
  requireProof(existsSync(decision) && fileDigest(decision) === p.query_contract_decision_sha256 &&
    JSON.parse(readFileSync(decision)).authority === 'user-confirmed reply',
    'Changed default-count contract lacks recorded user authorization.');
  const counts = p.explicit_count_queries;
  requireProof(counts?.length >= 4 && ['research', 'bagakit-researcher'].every(text =>
    ['cli', 'rpc'].every(transport => counts.some(q => q.text === text && q.transport === transport))) &&
    counts.every(q => q.count === true && !q.error && positive(q.matched_total) &&
      q.server_binary_sha256 === p.installed_binary_sha256 && Number.isFinite(q.elapsed_ms) &&
      q.elapsed_ms >= 0 && q.elapsed_ms < (q.transport === 'cli' ? 61000 : 60000)) &&
    p.count_page_identity_equal, 'Explicit exact count and page-equivalence proof incomplete.');
  const growth = p.post_catchup;
  requireProof(p.backlog?.caught_up && p.backlog.pending_sources === 0 && positive(p.backlog.sources) &&
    growth?.duration_seconds >= 300 && growth.end_bytes >= growth.start_bytes && positive(growth.start_bytes) &&
    growth.appended_events >= 0 && growth.catalog_rechecks >= 1 && growth.unchanged_sources_not_rewritten,
  'Catch-up completion and subsequent real increment window missing.');
  requireProof(p.capacity?.max_whole_shadows === 1 && p.capacity.min_available_bytes >= 2 ** 30 &&
    p.capacity.max_journal_bytes >= 0 && p.capacity.max_swap_allocation >= 0 &&
    p.capacity.ordinary_refusal_preserves_checkpoint && p.capacity.migration_refusal_preserves_originals &&
    p.runtime_owner_enforced, 'Peak capacity, refusal or exclusive ownership proof incomplete.');
  console.log(`Verified installed unified store: ${(p.layout.before_bytes / 2 ** 30).toFixed(2)} -> ${(p.layout.after_bytes / 2 ** 30).toFixed(2)} GiB; exact identities, raw, catch-up and real increment window.`);
}
