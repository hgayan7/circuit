const { readFileSync } = require('node:fs');
const { Agent, fetch } = require('undici');
const { Circuit } = require('./dist');

function fromEnvironment() {
    const caFile = process.env.CIRCUIT_CA_CERT;
    const agent = new Agent({ connect: { ca: caFile ? readFileSync(caFile) : undefined, minVersion: 'TLSv1.3' } });
    const circuit = new Circuit({
        url: process.env.CIRCUIT_GATEWAY_URL,
        token: readFileSync(process.env.CIRCUIT_TOKEN_FILE, 'utf8').trim(),
        fetchApi: (input, init) => fetch(input, { ...init, dispatcher: agent }),
    });
    circuit.close = () => agent.close();
    return circuit;
}
module.exports = { fromEnvironment };
