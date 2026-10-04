// Deterministic package metadata for the handwritten Node TLS adapter.
const fs = require('node:fs');
const path = 'sdk/typescript/package.json';
const pkg = JSON.parse(fs.readFileSync(path, 'utf8'));
pkg.dependencies = { undici: '8.11.2' };
pkg.engines = { node: '>=22.19.0' };
pkg.files = ['dist', 'node.cjs'];
fs.writeFileSync(path, JSON.stringify(pkg, null, 2) + '\n');
const tsPath = 'sdk/typescript/tsconfig.json';
const ts = JSON.parse(fs.readFileSync(tsPath, 'utf8'));
ts.compilerOptions.target = 'es2020';
ts.compilerOptions.lib = ['es2020', 'dom'];
fs.writeFileSync(tsPath, JSON.stringify(ts, null, 2) + '\n');
