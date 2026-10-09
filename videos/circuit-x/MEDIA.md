# Media provenance
- Circuit logo and demo-stats.png: copied from existing approved circuit-launch source assets. Demo is a local simulation, labelled on-screen.
- Space Grotesk and Inter: copied from existing source fonts with OFL license files.
- circuit-beat.wav: original synthesized score, deterministic seed and 120 BPM. Rebuild with `python3 scripts/soundtrack.py`. No third-party recording or model involved.
- impact.mp3 / glitch.mp3: HyperFrames bundled SFX, resolved through media-use keys impact-bass-1 and glitch-1. Gains 0.42 / 0.18; closing impact 0.24.
- RGB glitch: adapted rgb-glitch-text catalog offset mechanism; action roll and letter cascade informed by kinetic-type-swap / logo-brand-close recipes. Catalog originals retained under compositions/components.

- Voiceover: local Kokoro-82M, am_michael, speed 1.15; generated from narration.json. Measured WAV durations recorded in narration_meta.json. No cloud service used. Score ducks from 0.36 to 0.14 during speech; SFX lowered to protect voice clarity.
