"""Original deterministic 120 BPM Circuit X soundtrack; standard library only."""
import math, random, wave, struct
from pathlib import Path
RATE=44100
DURATION=22
samples=[0.0]*int(RATE*DURATION)
rng=random.Random(702)
def mix(start,duration,fn):
    offset=int(start*RATE)
    for i in range(min(int(duration*RATE),len(samples)-offset)):
        samples[offset+i]+=fn(i/RATE)
for beat in range(44):
    t=beat*.5
    # Short descending sine kick; deterministic clap on the backbeats.
    mix(t,.24,lambda s:.72*math.sin(2*math.pi*(48*s+5.5*(1-math.exp(-s*32))))*math.exp(-s*20))
    if beat%2:
        mix(t,.11,lambda s:.22*rng.uniform(-1,1)*math.exp(-s*33))
    if t>=6:
        for h in [0,.25]:
            mix(t+h,.045,lambda s:.07*rng.uniform(-1,1)*math.exp(-s*90))
notes=[40,52,55,59,50,52,55,62]
for step in range(88):
    t=step*.25
    note=notes[step%8]
    freq=440*2**((note-69)/12)
    gain=.12 if t<6 else .18
    mix(t,.22,lambda s,f=freq,g=gain:g*(math.sin(2*math.pi*f*s)+.25*math.sin(4*math.pi*f*s))*min(1,s/.009)*math.exp(-s*15))
# Resolve on E; all instruments decay into the last card without a clipped tail.
mix(20,2,lambda s:.17*(math.sin(2*math.pi*164.81*s)+.4*math.sin(2*math.pi*246.94*s))*math.exp(-s*2.6)*min(1,s/.03))
peak=max(abs(v) for v in samples)
frames=bytearray()
for i,v in enumerate(samples):
    t=i/RATE
    envelope=min(1,t/.006,(DURATION-t)/.65)
    value=int(max(-1,min(1,v/peak*.85*envelope))*32767)
    frames.extend(struct.pack('<hh',value,value))
p=Path(__file__).resolve().parents[1]/'assets'/'circuit-beat.wav'
with wave.open(str(p),'wb') as w:
    w.setnchannels(2);w.setsampwidth(2);w.setframerate(RATE);w.writeframes(frames)
print(p.name, '22s / 120 BPM / original synthesized score')
