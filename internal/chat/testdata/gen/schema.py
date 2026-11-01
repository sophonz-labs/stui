# Key names and JSON types only -- no values -- of the records in this
# machine's Claude Code transcripts, per CLI version and record type, into
# schema.json. Nothing of a conversation is written.
#   python3 schema.py   (edit vers below for other versions)
import json,glob,os,re,collections
vers=['2.1.231','2.1.270','2.1.284','2.1.293']
files=glob.glob(os.path.expanduser('~/.claude/projects/*/*.jsonl'))
byver=collections.defaultdict(list)
for f in files:
    with open(f) as fh:
        for i,l in enumerate(fh):
            m=re.search(r'"version":"([0-9.]+)"',l)
            if m: byver[m.group(1)].append(f); break
            if i>50: break
def jt(v):
    return {str:'s',bool:'b',int:'n',float:'n',dict:'o',list:'a',type(None):'z'}[type(v)]
out={}
for v in vers:
    sch=collections.defaultdict(dict)
    for f in byver[v]:
        for l in open(f):
            try: r=json.loads(l)
            except: continue
            if r.get('version')!=v: continue
            t=r.get('type')
            if t=='system': t='system:'+str(r.get('subtype'))
            for k,val in r.items():
                sch[t].setdefault(k,jt(val))
    out[v]=sch
json.dump(out,open(os.path.join(os.path.dirname(os.path.abspath(__file__)),'schema.json'),'w'),indent=0)
for v in vers: print(v, {t:len(k) for t,k in out[v].items() if t in('user','assistant') or t.startswith('system')})
