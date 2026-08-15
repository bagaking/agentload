import {readFileSync,readdirSync,existsSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {join,resolve} from 'node:path';
export function compactStorageInputDigest(root){
 const paths=['go.mod','go.sum','build_macos_app.sh','scripts/verify_trajectory_dictionary.mjs','docs/trajectory-requirements.md','docs/api-reference.md'];
 function walk(directory,accept){for(const entry of readdirSync(join(root,directory),{withFileTypes:true})){const path=join(directory,entry.name);if(entry.isDirectory())walk(path,accept);else if(accept(path))paths.push(path)}}
 for(const name of readdirSync(root)){if(/\.(go|m|h)$/.test(name))paths.push(name)}
 walk('internal',path=>path.endsWith('.go'));walk('ui/src',()=>true);walk('ui/dist',()=>true);
 const h=createHash('sha256');for(const path of [...new Set(paths)].sort()){h.update(path+'\0');h.update(readFileSync(join(root,path)));h.update('\0')};return h.digest('hex');
}
if(process.argv[1]===new URL(import.meta.url).pathname){
 const root=resolve(import.meta.dirname,'..'),path=join(root,'.bagakit/feature-tracker/features/f-22duuagpj/artifacts/storage-compact-acceptance.json');
 if(!existsSync(path))throw Error('Missing real integrated storage proof.');const p=JSON.parse(readFileSync(path,'utf8'));
 if(p.input_sha256!==compactStorageInputDigest(root))throw Error('Proof belongs to another implementation.');
 if(!p.complete||!p.resumed||!p.protected_files_preserved||!p.runtime_owner_enforced)throw Error('Preservation/recovery/ownership proof incomplete.');
 for(const name of ['canonical','search']){const x=p[name];if(!(x.before_bytes>x.after_bytes&&x.after_bytes>0&&x.records>0&&/^[a-f0-9]{64}$/.test(x.logical_digest)))throw Error('Missing real '+name+' reduction/digest.');}
 if(!p.search.all_rows_verified||!p.search.external_content_verified)throw Error('Search logical verification incomplete.');
 if(!p.frozen_queries?.length||p.frozen_queries.some(q=>!q.identities_equal||q.events_before!==q.events_after||q.sessions_before!==q.sessions_after))throw Error('Frozen query meaning changed.');
 if(!p.runtime_queries?.length||p.runtime_queries.some(q=>q.error||!(q.matched_total>0)||q.elapsed_ms>=6000)||!p.cli_rpc_identity_equal||!p.raw_available)throw Error('Installed query/read path incomplete.');
 if(!p.commit_recovery?.negative_control_failed||!p.commit_recovery?.fixed_fixture_passed)throw Error('Failed SQLite commit recovery lacks a real regression counterexample.');
 if(p.startup.samples.length<2||p.startup.samples.some(q=>q.error||!(q.matched_total>0)||q.elapsed_ms>=6000))throw Error('Subsequent installed query checkpoint failed.');
 if(p.installed_binary_sha256!==p.query_binary_sha256||!/^[a-f0-9]{64}$/.test(p.installed_binary_sha256))throw Error('Installed/query candidate mismatch.');
 if(p.installed_binary_sha256!==p.maintenance_binary_sha256){
  const format=p.query_only_successor?.physical_format;
  if(format?.codec!=='3'||format?.search_schema!==407||format?.dictionary_sha256!=='d2463b04a197fb2233119cf9ce81885e5b2d47e456c10092551c84e8ddb59717'||!p.query_only_successor.index_files_byte_identical)throw Error('Query successor changed completed physical storage.');
  if(p.query_only_successor.whole_corpus_queries?.length!==2||p.query_only_successor.whole_corpus_queries.some(q=>!q.equal_to_whole_literal_baseline||!(q.events>0)||!/^[a-f0-9]{64}$/.test(q.identity_sha256)))throw Error('Query successor lacks whole-corpus literal identity proof.');
 }
 if(!(p.peak?.samples>0&&p.peak?.min_available_bytes>0&&p.peak?.max_swap_allocation>=0))throw Error('Peak space accounting missing.');
 console.log(`Integrated storage: ${((p.canonical.before_bytes+p.search.before_bytes)/2**30).toFixed(2)} -> ${((p.canonical.after_bytes+p.search.after_bytes)/2**30).toFixed(2)} GiB; full logical rows, exact query identities, interruption and installed candidate verified. Startup caught up: ${p.startup.caught_up}.`);
}
