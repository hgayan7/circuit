# Circuit — The Boundary

A 22-second, 1920×1080 tech-thriller promo for X. Actual catalog scenes provide a wireframe camera journey, action particles, a moving Circuit boundary card, and a glass title reveal. No scene numbers appear in the film.

Voiceover uses local Kokoro `am_michael` at 1.08×. The original synthesized score ducks beneath narration. The measured offline mix peaks at −2.0 dBFS with no clipped samples. This is a sample-peak check, not a broadcast loudness certification.

```sh
npm run dev
npm run check -- --timeout 30000 --samples 15 --snapshots
npm run render -- --skill=product-launch-video --quality high --output renders/video.mp4
```

The approved delivery export is `renders/circuit-film.mp4`: H.264/AAC, 1920×1080, 30 fps, 22.0 seconds. The README web copy is `../../assets/circuit-launch.mp4` (5.3 MB, 22.016 seconds with AAC frame rounding). Source is pinned to HyperFrames 0.8.143. The catalog blocks carry separate Three.js runtimes; validation reports duplicate-runtime warnings and two source-size warnings. The glass matcap exceeds the bundle inline limit, so keep the project assets alongside the source when moving it. The local preview loads those assets successfully.

The footage illustrates Circuit. The recorded results are explicitly labelled a local simulation. Docker isolation requires configuration; the SDK alone does not provide isolation. The square `../circuit-x` project is preserved.

See `STORYBOARD.md`, `SCRIPT.md`, and `MEDIA.md` for timing, narration, and provenance.
