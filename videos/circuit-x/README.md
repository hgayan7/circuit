# Circuit X cut

22 seconds · 1080×1080 · 30 fps · original 120 BPM score · readable without sound.

A separate revision of `../circuit-launch`, built for X on 2026-10-09. It preserves the prior launch composition and exports. This project pins HyperFrames 0.8.143; the prior launch remains on 0.8.134.

```sh
npm run check -- --samples 15
npx hyperframes@0.8.143 preview --background
npm run render -- --quality delivery --output circuit-x.mp4
```

Review the assembled preview before the delivery render. The editor supports text edits, timeline adjustments, and export. Stop the persistent preview with `npx hyperframes@0.8.143 preview --stop`.

The mechanism is an explanatory diagram of configured Docker isolation. The captured interface shows local simulation results, labelled on-screen. Installing an SDK on an unrestricted host does not supply isolation.

`BRIEF.md` records intent and claims; `STORYBOARD.md` records the six beats; `MEDIA.md` records provenance. The opening headline and final CTA have explicit motion assertions in `index.motion.json`. `python3 scripts/soundtrack.py` recreates the original score without dependencies.

Catalog primitives: rgb-glitch-text, kinetic-type-swap, logo-brand-close. Their source recipes are retained under `compositions/components`; the scenes adapt the motion to Circuit's copy, gold palette, and square framing.
