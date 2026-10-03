# Working Instructions

- Run routine commands and tests without asking for command-by-command approval.
- Commit coherent, tested increments regularly and push them to the configured remote, as requested by the project owner.
- Never commit credentials, private keys, local deployment state, or backup decryption identities.
- Keep core enforcement separate from replaceable integrations. New plugins must pass scope, approval, unknown-outcome, and credential-isolation tests before support claims expand.
- Report test evidence honestly. A running soak is not a completed soak, and a fixture test is not validation of every production provider or deployment.
