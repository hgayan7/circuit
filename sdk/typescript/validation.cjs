const assert = require('node:assert/strict');
const { fromEnvironment } = require('./node.cjs');
const { ActionStopped } = require('./dist');
const circuit = fromEnvironment();
const request = operation => ({ operation, customTool: 'inventory', args: { sku: 'typescript' } });
(async () => {
    try {
        assert.equal((await circuit.execute(request('lookup'), 'typescript-read')).state, 'succeeded');
        const pending = await circuit.submit(request('reserve'), 'typescript-write');
        assert.equal(pending.state, 'pending');
        assert.equal((await circuit.execute(request('reserve'), 'typescript-write')).id, pending.id);
        await assert.rejects(circuit.execute(request('outside_scope'), 'typescript-denied'), error => error instanceof ActionStopped && error.action.state === 'denied');
        for (let i = 0; i < 2; i++) {
            await assert.rejects(circuit.execute(request('lose'), 'typescript-uncertain'), error => error instanceof ActionStopped && error.action.state === 'uncertain');
        }
        console.log('PASS TypeScript: verified TLS, read, exact approval, denial and uncertain no-replay');
    } finally { await circuit.close(); }
})().catch(error => { console.error(error); process.exitCode = 1; });
