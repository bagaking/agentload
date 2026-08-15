import { spawnSync } from 'node:child_process';
import { readFileSync,existsSync,readdirSync } from 'node:fs';
import { join } from 'node:path';
import { createHash } from 'node:crypto';

export function candidateInputDigest(root) {
 const result=spawnSync('git',['ls-files','-c','-o','--exclude-standard','-z'],{cwd:root});
 if(result.status!==0)throw Error('Cannot identify native candidate inputs.');
 const paths=[...new Set(result.stdout.toString().split('\0').filter(Boolean))].filter(p=>existsSync(join(root,p)) && (/\.(go|m|h)$/.test(p)||p==='go.mod'||p==='go.sum'||p.startsWith('ui/src/')||p.startsWith('ui/dist/')||/^ui\/(package.*json|tsconfig.*json|vite.config.*)$/.test(p)||p.startsWith('macos/'))).sort();
 const hash=createHash('sha256');
 for(const p of paths){hash.update(p+'\0');hash.update(readFileSync(join(root,p)));hash.update('\0')}
 return hash.digest('hex');
}
export function fixtureDigest(root) {
 const hash=createHash('sha256');
 const visit=(path,relative)=>{for(const e of readdirSync(path,{withFileTypes:true}).sort((a,b)=>a.name.localeCompare(b.name))){const p=join(path,e.name),r=relative+'/'+e.name;if(e.isDirectory())visit(p,r);else if(e.name.endsWith('.jsonl')){hash.update(r+'\0');hash.update(readFileSync(p));hash.update('\0')}}};
 for(const vendor of ['claude','codex','grok','trae'])visit(join(root,vendor),vendor);
 return hash.digest('hex');
}
export function fileDigest(path) {return createHash('sha256').update(readFileSync(path)).digest('hex')}
