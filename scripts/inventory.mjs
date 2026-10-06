import { readFile, readdir, writeFile, mkdir } from "node:fs/promises";
import path from "node:path";
const root=process.argv[2] ?? "/www/wwwroot/zjmf-manger-decoded-副本";
const routes=[];
for(const name of (await readdir(path.join(root,"data/route"))).filter(n=>n.endsWith(".php")).sort()){
 const text=await readFile(path.join(root,"data/route",name),"utf8");
 for(const [index,line] of text.split(/\r?\n/).entries()){
  const match=line.match(/Route::(get|post|put|patch|delete|rule|any)\((.*)/);
  if(!match)continue;
  const controller=match[2].match(/,\s*"([^"\n]+)"/);
  routes.push({file:`data/route/${name}`,line:index+1,method:match[1].toUpperCase(),expression:match[2],controller:controller?.[1]??null,status:"pending"});
 }
}
const sql=await readFile(path.join(root,"public/install/thinkcmf.sql"),"utf8");
const tables=[...sql.matchAll(/CREATE TABLE\s+`([^`]+)`/g)].map(m=>m[1]);
const grouped=Object.fromEntries([...new Set(routes.map(r=>r.file))].map(file=>[file,routes.filter(r=>r.file===file).length]));
await mkdir("docs/generated",{recursive:true});
await writeFile("docs/generated/legacy-inventory.json",JSON.stringify({source:root,sourceVersion:(await readFile(path.join(root,"version"),"utf8")).trim(),generatedAt:new Date().toISOString(),notes:"Static source calls only; group prefixes and runtime middleware must be traced before parity claims. Status pending is not implemented parity.",routeCallCount:routes.length,grouped,tableCount:tables.length,tables,routes},null,2)+"\n");
console.log(JSON.stringify({routeCallCount:routes.length,grouped,tableCount:tables.length}));
