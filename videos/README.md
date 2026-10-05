# Circuit launch video

Two editable HyperFrames projects explain what Circuit solves and how it works:

- `circuit-launch`: square, 1080 × 1080, 40.6 seconds.
- `circuit-launch-vertical`: portrait, 1080 × 1920, 40.6 seconds.

Both use local Kokoro narration (`am_michael`) and word-timed captions. The voice model was discovered in the local HyperFrames cache while investigating the Transcription project; that app itself uses Apple speech recognition. No changes were made to Transcription or toPDF. Model files and Python environments are excluded from this repository.

## Edit and render

Run `npm run dev`, `npm run check`, or `npm run render -- --quality delivery` inside either project. HyperFrames is pinned in package.json. Edit `compositions/frames/*.html`; the host is `index.html`. The script, storyboard, design tokens, narration, and selected source images are included. MP4 exports and temporary captures stay local under ignored directories.

## Evidence and scope

The UI images were captured from Circuit's actual local demo gateway, using `examples/github-agent.py --demo`. The demo produced five succeeded actions after both file-write and exact-head merge approvals. It did not operate on a real GitHub repository. Screenshots intentionally show pending states before approval and the completed queue afterward. The demo's approved SHA is `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`.

Claims follow the README and implemented scoped-request, policy, budget, approval, credential-isolation and recorded-execution behavior. Routing enforcement requires the documented isolation configuration. The video presents v0.2.0 for evaluation; it makes no production-certification claim.

Validation: HyperFrames check passes for both projects; visual snapshots were reviewed at scene holds, around cuts, and at the closing CTA. A duplicate-logo media-discovery warning refers to the same intentional logo in the top rail and closing lockup.

Assets: Circuit logo comes from the repository's `assets/logo.jpg`. Space Grotesk and Inter fonts come from Google Fonts, with their OFL licenses included. Registry components `grid-card-assemble` and `cta-lockup` supplied layout/reveal references; scene motion uses smooth deterministic GSAP timelines.

Final exports verified with ffprobe: H.264 video and AAC audio, 30 fps, 40.6 seconds, at the declared dimensions. Square export is 4.3 MiB; portrait export is 5.0 MiB.
