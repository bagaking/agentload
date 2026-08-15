import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { createHash } from 'node:crypto';
import { resolve, join } from 'node:path';

export function storageInputDigest(root) {
  const hash = createHash('sha256');
  const paths = ['go.mod', 'go.sum', 'main.go', 'trajectory_cli.go', 'trajectory_rpc.go',
    'trajectory_access.go', 'archive_recovery.go', 'server.go', 'scripts/verify_trajectory_storage.mjs'];
  for (const directory of ['internal/trajectory', 'internal/historyfile', 'internal/snapshot']) {
    for (const name of readdirSync(join(root, directory))) {
      if (name.endsWith('.go') && !name.endsWith('_test.go')) paths.push(`${directory}/${name}`);
    }
  }
  for (const path of paths.sort()) {
    hash.update(`${path}\0`); hash.update(readFileSync(join(root, path))); hash.update('\0');
  }
  return hash.digest('hex');
}

if (process.argv[1] === new URL(import.meta.url).pathname) {
  const root = resolve(import.meta.dirname, '..');
  const path = join(root, '.bagakit/feature-tracker/features/f-22duuagpj/artifacts/storage-acceptance.json');
  if (!existsSync(path)) throw Error('Missing actual storage acceptance evidence.');
  const proof = JSON.parse(readFileSync(path, 'utf8'));
  if (proof.storage_input_sha256 !== storageInputDigest(root)) throw Error('Storage evidence belongs to another implementation.');
  if (!proof.complete || !proof.resumed || !proof.source_and_authored_files_preserved || !proof.runtime_owner_enforced) throw Error('Storage preservation/recovery proof incomplete.');
  for (const index of ['canonical', 'search']) {
    const measurement = proof[index];
    if (!(measurement.before_bytes > measurement.after_bytes && measurement.after_bytes > 0 && measurement.records > 0)) throw Error(`Invalid ${index} measurement.`);
  }
  if (!/^[0-9a-f]{64}$/.test(proof.canonical.logical_digest) ||
      proof.canonical.logical_digest !== proof.canonical.original_logical_digest ||
      !proof.search.external_content_verified) throw Error('Logical records/FTS content were not verified.');
  const before = proof.canonical.before_bytes + proof.search.before_bytes;
  const after = proof.canonical.after_bytes + proof.search.after_bytes;
  if (after / before > 0.5) throw Error('Real index reduction below 50%.');
  if (!(proof.growth?.current_bytes < proof.growth?.uncompressed_bytes * 0.5)) throw Error('Fixed-input incremental storage reduction missing.');
  if (!proof.queries?.length || proof.queries.some(query => query.error || !(query.matched_total > 0) || query.elapsed_ms >= 6000)) throw Error('Actual ordinary-word query regression remains.');
  console.log(`Trajectory storage: ${(before / 2 ** 30).toFixed(2)} -> ${(after / 2 ** 30).toFixed(2)} GiB, ${(100 * (1 - after / before)).toFixed(1)}% reduction; all logical records, FTS content and preserved files verified.`);
}
