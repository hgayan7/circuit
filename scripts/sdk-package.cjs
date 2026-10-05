// Deterministic package metadata for the handwritten Node TLS adapter.
const fs = require('node:fs');
const path = 'sdk/typescript/package.json';
const pkg = JSON.parse(fs.readFileSync(path, 'utf8'));
pkg.dependencies = { undici: '8.11.2' };
pkg.engines = { node: '>=22.19.0' };
pkg.files = ['dist', 'node.cjs'];
pkg.license = 'MIT';
pkg.description = 'Circuit agent client with verified TLS and explicit action-state handling';
fs.writeFileSync(path, JSON.stringify(pkg, null, 2) + '\n');
const tsPath = 'sdk/typescript/tsconfig.json';
const ts = JSON.parse(fs.readFileSync(tsPath, 'utf8'));
ts.compilerOptions.target = 'es2020';
ts.compilerOptions.lib = ['es2020', 'dom'];
fs.writeFileSync(tsPath, JSON.stringify(ts, null, 2) + '\n');
// The generator's pyproject name otherwise overrides its setup.py projectName.
const pyPath = 'sdk/python/pyproject.toml';
const py = fs.readFileSync(pyPath, 'utf8').replace(/^name = "circuit_client"$/m, 'name = "circuit-agent-client"');
fs.writeFileSync(pyPath, py);
for (const dir of ['sdk/typescript', 'sdk/python', 'sdk/go']) {
    fs.copyFileSync('LICENSE', dir + '/LICENSE');
}
