const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const ts = require("../web/node_modules/typescript");

const source = fs.readFileSync(path.join(__dirname, "../web/src/lib/product-options.ts"), "utf8");
const compiled = ts.transpileModule(source, {
  compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
}).outputText;
const loaded = { exports: {} };
new Function("module", "exports", compiled)(loaded, loaded.exports);
const cases = JSON.parse(fs.readFileSync(0, "utf8"));
for (const test of cases) {
  const before = JSON.stringify(test.option);
  assert.deepEqual(loaded.exports.productOptionAmounts(test.option, test.cycle, test.quantity), test.expected, test.name);
  assert.equal(JSON.stringify(test.option), before, `${test.name}: preview mutated pricing`);
}
console.log(`Go/TypeScript option pricing parity: ${cases.length} cases passed`);
