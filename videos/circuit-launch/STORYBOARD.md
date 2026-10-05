---
format: 1080x1080
duration: 40.6s
message: "Control what your AI agents can execute."
arc: Problem → Solution → Checks → Approval and execution → CTA
audience: Developers building tool-using AI agents
mode: autonomous
music: none
---

## Video direction

Use Circuit's dark canvas, warm yellow accent, and light ink from frame.md. Large Space Grotesk display, Inter supporting labels. Restrained smooth sequential reveals, with still holds for the actual UI. No bouncing, decorative drifting, invented performance metrics, or fake customer logos. Captured UI is the source of truth. Lower 17% reserved for captions. Each scene is a clean editorial cut, no continued elements across boundaries. Problem and CTA use strong typography; the mechanism is a diagram; proof uses actual local demo captures labeled Local demo. Narration Michael from cached local Kokoro. All facts trace to README and demo records.

## Frame 1 — The problem
- type: hook
- scene: What stops one wrong action?
- duration: 7.8s
- poster: 5s
- transition_in: cut
- status: built
- src: compositions/frames/01-problem.html
- voiceover: Your agent can write files and merge pull requests. What stops one wrong action from becoming a real change?
- asset_candidates: assets/circuit-logo.jpg
- blueprint: compose
- focal: Unintended action
- roles: assets/circuit-logo.jpg = supporting

Visible text: "Your agent can ship." then action rows "Write files" and "Merge PRs"; payoff "Who controls the action?". Small eyebrow "THE PROBLEM".
Scene 1 (0–1.5s): Large two-line claim enters high left via sequential reveal (dynamic-content-sequencing). Logo in top rail.
Scene 2 (1.5–4s): Two actual workflow action rows accumulate beneath the claim (dynamic-content-sequencing), joined by a warm accent vertical spine.
Scene 3 (4–7.5s): Question replaces the lead line in-place (discrete-text-sequence), accent underline draws beneath "controls" (svg-path-draw), then hold for reading.

## Frame 2 — The boundary
- type: product_intro
- scene: Circuit controls governed execution
- duration: 7s
- poster: 5s
- transition_in: cut
- status: built
- src: compositions/frames/02-boundary.html
- voiceover: Circuit puts a governed gateway between your agent and its tools. You control what it can execute.
- asset_candidates: assets/circuit-logo.jpg
- blueprint: compose
- focal: Circuit gateway
- roles: assets/circuit-logo.jpg = cutout

Visible text: "Control what agents execute." Nodes "Agent", "Circuit", "Tools". Supporting line "Scoped requests. Gateway-held credentials." Footer "Enforced routing requires configured isolation."
Scene 1 (0–1.7s): Hero claim lands with a smooth reveal; Agent node appears.
Scene 2 (1.7–4.2s): Circuit logo node and then Tools node reveal, connectors self-draw in request order (svg-path-draw).
Scene 3 (4.2–7s): "Scoped requests" and "Gateway-held credentials" reveal one after the other (dynamic-content-sequencing). Small isolation qualifier appears and holds.

## Frame 3 — Check before execution
- type: feature_showcase
- scene: Scope, policy, budget, then review
- duration: 8.4s
- poster: 6s
- transition_in: cut
- status: built
- src: compositions/frames/03-checks.html
- voiceover: Requests pass scope, policy, and budget checks. Sensitive actions wait for a human to review the exact payload.
- asset_candidates: assets/demo-pending.png
- blueprint: compose
- focal: assets/demo-pending.png
- roles: assets/demo-pending.png = supporting

Visible text: "Check. Then act." Three numbered steps "Scope", "Policy", "Budget" followed by "Sensitive write → Awaiting approval". Small fixed badge "LOCAL DEMO". Actual pending file-write UI as a framed screenshot.
Scene 1 (0–3.2s): Headline lands; check labels reveal sequentially following narration (dynamic-content-sequencing), using registry grid-card-assemble settle recipe.
Scene 2 (3.2–6.3s): Pending write UI slides into the lower hero area; waiting badge highlights (press-release-spring without bounce).
Scene 3 (6.3–9s): Label "Review the exact payload" reveals beneath the picture, diagram line from checks to human-review badge draws, hold still.

## Frame 4 — Approval becomes execution
- type: benefit_highlight
- scene: Exact approval, execution, recorded result
- duration: 9.2s
- poster: 7s
- transition_in: cut
- status: built
- src: compositions/frames/04-execution.html
- voiceover: After approval, Circuit executes with gateway-held credentials and records the result. Merges bind to the approved commit.
- asset_candidates: assets/demo-complete.png, assets/demo-merge.png
- blueprint: compose
- focal: assets/demo-complete.png
- roles: assets/demo-complete.png = supporting, assets/demo-merge.png = supporting

Visible text: "Approved. Executed. Recorded." Small badge "LOCAL DEMO". Show actual completed queue, then separately show actual pending merge. Supporting label "Merge approval binds to the exact commit" and a shortened approved head "aaaaaaaa…" from the demo fixture. Clarify this second UI is the merge review before execution.
Scene 1 (0–3s): Completed local demo UI enters; title "Approved" reveals first and "Executed" then "Recorded" reveal on spoken cues (dynamic-content-sequencing).
Scene 2 (3–6s): Proof label "Gateway holds service credentials" appears and recorded-result badge highlights. No floating or slow push.
Scene 3 (6–9.5s): Switch UI to actual merge review (discrete-text-sequence), label "Exact commit approval" reveals with the short SHA and holds for reading. Caption spells out the relation.

## Frame 5 — Try Circuit
- type: cta
- scene: Circuit open source, self-hosted, try the demo
- duration: 8.2s
- poster: 5s
- transition_in: cut
- status: built
- src: compositions/frames/05-close.html
- voiceover: Keep agents moving. Keep control. Circuit is open source and self-hosted. Try the demo on GitHub.
- asset_candidates: assets/circuit-logo.jpg
- blueprint: compose
- focal: assets/circuit-logo.jpg
- roles: assets/circuit-logo.jpg = cutout

Visible text: "Keep agents moving." and "Keep control." Official logo, "Circuit v0.2.0", "Open source · Self-hosted", and "github.com/hgayan7/circuit" CTA with "Try the demo". Small release qualifier "Evaluate with your own workload."
Scene 1 (0–2.5s): First claim then accent payoff reveal sequentially (dynamic-content-sequencing).
Scene 2 (2.5–4.5s): Logo and version lockup land cleanly using installed cta-lockup proportion, release traits reveal beneath.
Scene 3 (4.5–7s): CTA and repository URL reveal; calm still hold to final frame.
