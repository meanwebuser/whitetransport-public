#!/usr/bin/env python3
from pathlib import Path
from PIL import Image, ImageDraw, ImageFont, ImageFilter
import math

ROOT = Path(__file__).resolve().parents[2]
res = ROOT / 'app/src/main/res'
sizes = {'mdpi':48,'hdpi':72,'xhdpi':96,'xxhdpi':144,'xxxhdpi':192}
fg_sizes = {'mdpi':108,'hdpi':162,'xhdpi':216,'xxhdpi':324,'xxxhdpi':432}

def font(size):
    for p in ['/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf','/usr/share/fonts/truetype/liberation2/LiberationSans-Bold.ttf']:
        if Path(p).exists(): return ImageFont.truetype(p, size)
    return ImageFont.load_default()

def draw_icon(n, round_mask=False):
    im = Image.new('RGBA',(n,n),(0,0,0,0)); d=ImageDraw.Draw(im)
    # bg gradient
    for y in range(n):
        for x in range(n):
            dx=(x-n*.5)/(n*.72); dy=(y-n*.45)/(n*.72); r=min(1, math.sqrt(dx*dx+dy*dy))
            c0=(24,41,74); c1=(2,4,10)
            col=tuple(int(c0[i]*(1-r)+c1[i]*r) for i in range(3))+(255,)
            im.putpixel((x,y),col)
    # rounded mask
    mask=Image.new('L',(n,n),0); md=ImageDraw.Draw(mask); rad=int(n*.22); md.rounded_rectangle([0,0,n-1,n-1], radius=rad, fill=255)
    if round_mask:
        mask=Image.new('L',(n,n),0); ImageDraw.Draw(mask).ellipse([0,0,n-1,n-1], fill=255)
    im.putalpha(mask); d=ImageDraw.Draw(im)
    web = (130,245,255,190); web2=(120,120,255,120); sw=max(2,n//56)
    def pts(seq): return [(int(x*n/1024), int(y*n/1024)) for x,y in seq]
    # web curves as polylines
    curves=[[(156,198),(320,100),(555,96),(784,214)],[(110,520),(245,322),(498,210),(868,232)],[(164,826),(292,640),(548,480),(914,392)],[(250,138),(330,342),(348,596),(268,898)],[(514,102),(486,342),(502,612),(588,918)],[(806,190),(656,390),(618,660),(752,900)],[(142,360),(330,308),(562,318),(876,318)],[(128,666),(350,594),(604,542),(920,498)]]
    for i,c in enumerate(curves): d.line(pts(c), fill=web if i<6 else web2, width=sw, joint='curve')
    d.ellipse([n*.18,n*.18,n*.82,n*.82], outline=(20,232,255,60), width=max(1,n//120))
    d.ellipse([n*.12,n*.12,n*.88,n*.88], outline=(139,92,255,45), width=max(1,n//150))
    # B glow
    txt='B'; fs=int(n*.72); f=font(fs)
    bbox=d.textbbox((0,0),txt,font=f,stroke_width=max(1,n//80)); tw=bbox[2]-bbox[0]; th=bbox[3]-bbox[1]
    x=(n-tw)//2-bbox[0]; y=int(n*.50-th*.58)-bbox[1]
    glow=Image.new('RGBA',(n,n),(0,0,0,0)); gd=ImageDraw.Draw(glow)
    gd.text((x,y),txt,font=f,fill=(0,220,255,255),stroke_width=max(1,n//36),stroke_fill=(145,92,255,200))
    im=Image.alpha_composite(im, glow.filter(ImageFilter.GaussianBlur(max(2,n//24))))
    d=ImageDraw.Draw(im)
    d.text((x,y),txt,font=f,fill=(0,215,255,255),stroke_width=max(1,n//90),stroke_fill=(235,255,255,230))
    d.line([(int(n*.31),int(n*.24)),(int(n*.69),int(n*.24))], fill=(255,255,255,120), width=max(2,n//45))
    d.line([(int(n*.31),int(n*.77)),(int(n*.69),int(n*.77))], fill=(102,247,255,115), width=max(2,n//55))
    return im

for dpi,n in sizes.items():
    out=res/f'mipmap-{dpi}'; out.mkdir(parents=True,exist_ok=True)
    draw_icon(n).save(out/'ic_launcher.webp','WEBP',quality=95)
    draw_icon(n, True).save(out/'ic_launcher_round.webp','WEBP',quality=95)
for dpi,n in fg_sizes.items():
    out=res/f'mipmap-{dpi}'; out.mkdir(parents=True,exist_ok=True)
    # transparent foreground only B/web inside adaptive icon safe zone
    draw_icon(n).save(out/'ic_launcher_foreground.webp','WEBP',quality=95)
