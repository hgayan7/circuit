# Circuit videos

The current README video is `circuit-film`: a 22-second, 1920 × 1080 cinematic promo using catalog 3D scenes, local Kokoro narration, and an original score. Its editable source, timing, and provenance are in [circuit-film](circuit-film/README.md). `assets/circuit-launch.mp4` at the repository root is the published README video.

The earlier projects below remain available for editing.

Two editable HyperFrames projects explain what Circuit solves and how it works:

- `circuit-launch`: square, 1080 × 1080, 28.62 seconds.
- `circuit-launch-vertical`: portrait, 1080 × 1920, 28.62 seconds.

Both use local Kokoro narration (`am_michael`) and word-timed captions. The voice model was discovered in the local HyperFrames cache while investigating the Transcription project; that app itself uses Apple speech recognition. No changes were made to Transcription or toPDF. Model files and Python environments are excluded from this repository.

## Edit and render

Run `npm run dev`, `npm run check`, or `npm run render -- --quality delivery` inside either project. HyperFrames is pinned in package.json. Edit `compositions/frames/*.html`; the host is `index.html`. The script, storyboard, design tokens, narration, and selected source images are included. MP4 exports and temporary captures stay local under ignored directories.

## Evidence and scope

The UI images were captured from Circuit's actual local demo gateway, using `examples/github-agent.py --demo`. The demo produced five succeeded actions after both file-write and exact-head merge approvals. It did not operate on a real GitHub repository. Screenshots intentionally show pending states before approval and the completed queue afterward. The demo's approved SHA is `aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa`.

Claims follow the README and implemented scoped-request, policy, budget, approval, credential-isolation and recorded-execution behavior. Routing enforcement requires the documented isolation configuration. The video presents v0.2.0 for evaluation; it makes no production-certification claim.

The previous v2 responds to the request for a modern, catchy film: kinetic opening, varied entrances, request routing motion, sequential checks, magnified actual approval control with an illustrative cursor, recorded completion, and original quiet action cues. The 40.6-second v1 exports remain local; the first storyboard is archived in each project.

Validation: HyperFrames check passes for both projects; visual snapshots were reviewed at scene holds, around cuts, and at the closing CTA. A duplicate-logo media-discovery warning refers to the same intentional logo in the top rail and closing lockup.

Assets: Circuit logo comes from the repository's `assets/logo.jpg`. Space Grotesk and Inter fonts come from Google Fonts, with their OFL licenses included. Registry components `grid-card-assemble` and `cta-lockup` supplied layout/reveal references; scene motion uses smooth deterministic GSAP timelines.

V2 exports use H.264 video and AAC audio, 30 fps, 28.62 seconds, at the declared dimensions.

V2 ffprobe verification: both MP4 exports contain H.264 video, AAC audio, 30 fps, and exactly 27.0 seconds at 1080×1080 and 1080×1920 respectively.

V3 centers the documented Docker/runc sandbox: non-root execution, restricted mounts, gateway-only network access, and provider credentials outside the agent. Scope/policy/budget checks and declared tool integrations follow. Approval is only a brief when-required note. The sandbox diagram explains the architecture; it is not recorded sandbox test footage. V2 exports remain local and its storyboard is preserved.

V3 exports verified: H.264/AAC, 30 fps, 28.633 seconds after frame rounding, at 1080×1080 and 1080×1920. HyperFrames runtime/layout checks pass for both.
