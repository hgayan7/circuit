const fs = require('node:fs');
const path = require('node:path');
// Normalize only the generator's inventory, never dependencies or handwritten files.
for (const language of ['typescript', 'python', 'go']) {
    const root = path.join('sdk', language);
    const files = fs.readFileSync(path.join(root, '.openapi-generator/FILES'), 'utf8').trim().split('\n');
    for (const name of files) {
        const file = path.join(root, name);
        if (!fs.existsSync(file)) continue;
        const source = fs.readFileSync(file, 'utf8');
        if (source.length) fs.writeFileSync(file, source.replace(/[ \t]+$/gm, '').replace(/\n+$/, '\n'));
    }
}
