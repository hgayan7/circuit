"""Recreate narration with HyperFrames local Kokoro (set HYPERFRAMES_PYTHON if needed)."""
import json,subprocess
from pathlib import Path
root=Path(__file__).resolve().parents[1]
request=json.loads((root/'narration.json').read_text())
for line in request['lines']:
    subprocess.run(['npx','--yes','hyperframes@0.8.143','tts',line['text'],'--voice',request['voice'],'--speed',str(request['speed']),'--output',f"assets/voice/{line['id']}.wav",'--json'],cwd=root,check=True)
