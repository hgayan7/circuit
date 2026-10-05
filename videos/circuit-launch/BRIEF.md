---
workflow: product-launch-video
flow: automation
storyboard: no
message: "Control what your AI agents can execute."
destination: linkedin
aspect: 1080x1080
language: en
audience: Developers and teams building tool-using AI agents
length: 40s
angle: problem-solution-demo
narration: yes
---

## Intent

Create the launch video proposed in chat. The user approved creation with "do it" and then clarified: "should we focus on what it solves and how it works". Lead with the risk of unintended actions from agents holding broad credentials; explain scoped requests, policy checks, sensitive-action approval, gateway-owned credentials, and recorded execution. Demonstrate a local file-write and exact-head merge approval. End on Circuit v0.2.0, open source, self-hosted, and the GitHub demo CTA.

## Assets

- ../../assets/logo.jpg — existing Circuit brand mark.
- Local Circuit gateway demo — actual product UI and simulated action records, labeled Local demo.

## Customizations

- Readable on-screen captions and restrained motion.
- Square 1080x1080 primary video plus vertical 1080x1920 adaptation.

## Notes

- Source facts from repository README and verified local demo; no external site crawl needed.
- Defaults: English, 40 seconds, captions with local Kokoro narration; choose visual style and available audio locally.
- Autonomous execution under the user's "do it" instruction; complete the requested rendered videos.
- Claim enforcement only for governed requests and configured gateway-only Docker isolation. No universal safety, zero-code, production-certification, exactly-once, or unsupported provider claims.

- User requested checking the local model; cached Kokoro-82M is used through a project-local Python environment. No music bed.

## Revision — 2026-10-05

User requests a more modern, catchy film. Build and export a shorter visual revision with kinetic typography, larger real UI, causal request/approval motion, shorter local narration and subtle original action cues.
