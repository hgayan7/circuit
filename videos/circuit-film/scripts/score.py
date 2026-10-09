"""Original seeded cinematic underscore for Circuit. Requires NumPy."""
import numpy as np,wave
from pathlib import Path
sr=44100;duration=22;t=np.arange(sr*duration)/sr;rng=np.random.default_rng(43)
# Dark E-minor texture, restrained pulse, growing harmonic tension.
score=.055*np.sin(2*np.pi*41.203*t)+.032*np.sin(2*np.pi*82.407*t)*(.6+.4*np.sin(t*1.7))
score+=.018*np.sin(2*np.pi*123.471*t)*(.7+.3*np.sin(t*.6))
score+=.009*np.sin(2*np.pi*164.814*t+np.sin(t*.9)*.7)
noise=rng.uniform(-1,1,len(t));noise=np.convolve(noise,np.ones(9)/9,mode='same')
for cue in [.6,5.5,9.5,16.4]:
 u=np.clip((t-cue)/.8,0,1);env=np.where((t>=cue)&(t<cue+1.3),np.sin(np.pi*np.clip((t-cue)/1.3,0,1))**3,0)
 score+=.1*noise*env
for cue in [1.6,6,10,17]:
 s=t-cue;env=np.where(s>=0,np.exp(-np.maximum(s,0)*4),0)
 score+=.52*np.sin(2*np.pi*(37*s+4*(1-np.exp(-np.maximum(s,0)*25))))*env
 score+=.12*np.sin(2*np.pi*164.814*s)*env
for cue in np.arange(6,17,.5):
 s=t-cue;env=np.where(s>=0,np.exp(-np.maximum(s,0)*14),0)
 score+=.12*np.sin(2*np.pi*82.407*s)*env
# Resolve into a quieter identity hold.
score*=np.minimum(1,t/.04)*np.clip((duration-t)/.75,0,1)
score=score/max(abs(score))*.8
left=score;right=score+.003*np.sin(2*np.pi*246.94*t)*np.minimum(1,t/.1)*np.clip((duration-t)/.75,0,1)
data=(np.stack([left,right],axis=1)*32767).astype('<i2')
p=Path(__file__).resolve().parents[1]/'assets/cinematic-score.wav'
with wave.open(str(p),'wb') as w:w.setnchannels(2);w.setsampwidth(2);w.setframerate(sr);w.writeframes(data.tobytes())
print('Original cinematic score: 22 seconds')
