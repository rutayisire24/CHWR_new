#!/usr/bin/env python3
"""Convert the eCHIS deployment hierarchy into importer-canonical CSVs.

The companion to convert_odk_export.py, for the other national roster:
cht.mv_chw_hierarchy in uganda_dwh. Same contract -- placement is resolved
here, against the hierarchy the rows will be imported into, and handed to the
importer as location_code. The name columns are written back from the resolved
chain so the importer's own code-vs-name check has something to agree with.

Writes nothing to the database.

eCHIS is a deployment hierarchy, not a survey. It fills ten of the importer's
twenty-eight columns and carries no sex, no NIN and no age, so this script
sorts its output into two directories rather than one:

    ready/     rows the importer can take today -- sex known
    pending/   rows resolved and placed, with sex blank

A pending file is importer-canonical in every other respect: a district fills
one column and uploads it. Nothing else about the row needs their attention,
which is the whole reason placement is resolved here rather than left to the
importer's strict cascade.

Supply sex with SEX_FILE and the split disappears -- see below.
"""
import csv, collections, json, os, re, subprocess, sys

csv.field_size_limit(10**9)

if len(sys.argv) < 3:
    sys.exit("usage: convert_echis_export.py <mv_chw_hierarchy.csv|.parquet> <output-dir>\n"
             "      reads the hierarchy, facilities and the register from $DATABASE_URL\n"
             "  env: CADRE=vht|chew          one cadre per run (default vht)\n"
             "       SEX_FILE=path.csv       chw_id,sex -- fills the one column eCHIS lacks\n"
             "       NAME_FROM_USERNAME=1    derive a name from the username slug when the\n"
             "                               name cell is empty; off by default, because a\n"
             "                               name read off a slug is a guess about a person")
SRC, OUT = sys.argv[1], sys.argv[2]

DB = os.environ.get('DATABASE_URL')
if not DB: sys.exit("DATABASE_URL is not set")

# Cadre decides placement, so it decides which rows a run is about, exactly as
# in the ODK converter: a VHT run places at village, a CHEW run at parish, and
# one report covering both levels would not say which question a refusal answers.
CADRE = os.environ.get('CADRE', 'vht').strip().lower()
if CADRE not in ('vht', 'chew'): sys.exit("CADRE must be vht or chew")
NAME_FROM_USERNAME = os.environ.get('NAME_FROM_USERNAME', '') not in ('', '0', 'no')

os.makedirs(os.path.join(OUT, 'ready'), exist_ok=True)
os.makedirs(os.path.join(OUT, 'pending'), exist_ok=True)

def dump(sql):
    """Read a table out of the register. The hierarchy the codes are resolved
    against must be the one they will be imported into."""
    out = subprocess.run(['psql', DB, '-tA', '-F\t', '-c', sql],
                         capture_output=True, text=True)
    if out.returncode: sys.exit("psql failed: " + out.stderr.strip())
    return [l.split('\t') for l in out.stdout.splitlines() if l.strip()]

def norm(s): return re.sub(r"[ \t\n\r.'’\-–_/]", '', (s or '').upper())

# ---------------------------------------------------------------- source
def read_source(path):
    """eCHIS is extracted as parquet by echis_dwh's export_echis.py, but a psql
    \\copy of the same view is a CSV. Both are the same thirteen columns."""
    if path.lower().endswith('.parquet'):
        try:
            import pandas as pd
        except ImportError:
            sys.exit("reading .parquet needs pandas; export the view to CSV instead")
        df = pd.read_parquet(path)
        return [{k: ('' if v is None or v != v else str(v)) for k, v in rec.items()}
                for rec in df.to_dict('records')]
    with open(path, newline='', encoding='utf-8-sig') as f:
        return list(csv.DictReader(f))

# The view has been spelled both ways in different extracts.
ALIASES = {'subcounty': 'sub_county', 'sub_county': 'subcounty'}
def cell(r, key):
    v = r.get(key)
    if v is None and key in ALIASES: v = r.get(ALIASES[key])
    return (v or '').strip()

# ---------------------------------------------------------------- hierarchy
byid, kids = {}, collections.defaultdict(list)
for p in dump("select id, coalesce(parent_id::text,''), level::text, name, code_path from locations"):
    if len(p) < 5: continue
    i, par, lvl, name, cp = p
    i = int(i); par = int(par) if par else None
    byid[i] = dict(parent=par, level=lvl, name=name, code_path=cp)
    kids[par].append(i)

dnames = collections.defaultdict(list)
for i, v in byid.items():
    if v['level'] == 'district': dnames[norm(v['name'])].append(i)

DIST_ALIAS = {'LUWERO': 'LUWEERO'}

_dcache = {}
def dlevels(did):
    if did in _dcache: return _dcache[did]
    out = {'county': [], 'subcounty': [], 'parish': [], 'village': []}
    stack = [did]
    while stack:
        n = stack.pop()
        for c in kids[n]:
            if byid[c]['level'] in out: out[byid[c]['level']].append(c)
            stack.append(c)
    _dcache[did] = out
    return out

# eCHIS writes the administrative tier into the cell -- "Kyotera District",
# "Kyebe Subcounty", "Gombe Ward". Which of those words is decoration depends on
# the level, exactly as it does in internal/importer/resolve.go, and for the same
# reason: 588 subcounties really are called "... TOWN COUNCIL" and 3,228 parishes
# really are called "... WARD". So the literal cell is always tried first and a
# stripped reading only ever second -- a fragment can widen the search, never
# overrule a name that already matched.
TIER = {
    'district':  ('DISTRICT',),
    'subcounty': ('SUBCOUNTY', 'SUBCOUNTIES', 'SC'),
    'parish':    ('PARISH',),
    'village':   ('VILLAGE', 'CELL', 'ZONE'),
}

def frags(level, c):
    """Every sensible reading of an eCHIS location cell, best first."""
    c = (c or '').strip()
    if not c: return []
    out = [c]
    n = norm(c)
    # T/C and TC are the abbreviation of a name, not decoration: expand rather
    # than drop, so MPIGI T/C meets MPIGI TOWN COUNCIL and not MPIGI.
    if level == 'subcounty' and n.endswith('TC') and len(n) > 2:
        out.append(n[:-2] + 'TOWNCOUNCIL')
    for suf in TIER.get(level, ()):
        if n.endswith(suf) and len(n) > len(suf): out.append(n[:-len(suf)])
    return [f for f in out if f.strip()]

def match(cands, level, c):
    for f in frags(level, c):
        fn = norm(f)
        m = [i for i in cands if norm(byid[i]['name']) == fn]
        if m: return m
    return []

def under(ids, ancestors):
    if not ancestors: return ids
    anc, out = set(ancestors), []
    for i in ids:
        n = byid[i]['parent']
        while n is not None:
            if n in anc: out.append(i); break
            n = byid[n]['parent']
    return out

def chain_of(vid):
    out, n = {}, vid
    while n is not None:
        out[byid[n]['level']] = byid[n]['name']
        n = byid[n]['parent']
    return out

# ---------------------------------------------------------------- facilities
fac_by_district = collections.defaultdict(dict)
for p in dump("select id, district_id, name from facilities"):
    if len(p) < 3: continue
    fid, did, name = p
    fac_by_district[int(did)].setdefault(norm(name), name)

# ---------------------------------------------------------------- the register
# eCHIS carries no NIN, so chws_nin_uniq cannot catch a person this file shares
# with the 44,446 already imported from the ODK export. Nothing downstream will
# either: the importer only *warns* on a duplicate name at one location, and a
# CHW is never deleted, so a duplicate created here is permanent. Hence the probe
# is done at conversion time, on the same (location_id, last, first) the register
# indexes -- and on the token set rather than the pair, because eCHIS stores one
# name string whose order is unreliable and "Nandala Moses" must meet "Moses
# Nandala".
already = set()
for p in dump("select location_id, lower(first_name), lower(last_name) from chws"):
    if len(p) < 3: continue
    already.add((int(p[0]), frozenset(re.split(r'\s+', (p[1] + ' ' + p[2]).strip()))))

# ---------------------------------------------------------------- sex
# The one column eCHIS does not have and the schema will not do without: chws.sex
# is NOT NULL and the importer requires it. It is never guessed -- a defaulted sex
# is a wrong fact about a person that no one will ever re-examine, and it would
# flow straight into the reporting the register exists to feed.
SEXMAP = {}
sf = os.environ.get('SEX_FILE', '').strip()
if sf:
    with open(sf, newline='', encoding='utf-8-sig') as f:
        for r in csv.DictReader(f):
            key = (r.get('chw_id') or r.get('username') or '').strip()
            val = (r.get('sex') or '').strip().lower()
            val = {'m': 'male', 'f': 'female'}.get(val, val)
            if key and val in ('male', 'female'): SEXMAP[key] = val

COLS = ['first_name','last_name','sex','cadre','age_years','nin',
        'district','subcounty','parish','village','location_code',
        'phone_owner','phone_primary','phone_for_reporting','phone_alternate',
        'facility','service_start_year','households_served','education',
        'english','other_languages','receives_incentive','incentive_frequency',
        'incentive_amount_ugx','tools','tools_functional','services','trained']

# SUPER CHEW is a CHEW with extra duties; the register's cadre is two-valued by
# decision, and the original survives in REJECTS/NOTES rather than in a third value.
ROLE = {'VHT': 'vht', 'CHEW': 'chew', 'SUPER CHEW': 'chew'}

def phone9(raw):
    d = re.sub(r'\D', '', raw or '')
    if d.startswith('256'): d = d[3:]
    return d.lstrip('0') if len(d.lstrip('0')) == 9 else ''

# ---------------------------------------------------------------- convert
rows = read_source(SRC)
ready, pending, rejects, notes = [], [], [], []
seen_id, seen_person = set(), set()
stat = collections.Counter()

def reject(r, stage, reason):
    rejects.append({'reason': reason, 'stage': stage,
                    'chw_id': cell(r,'chw_id'), 'username': cell(r,'username'),
                    'chw_name': cell(r,'chw_name'), 'role': cell(r,'role'),
                    'district': cell(r,'district'), 'county': cell(r,'county'),
                    'subcounty': cell(r,'sub_county'), 'parish': cell(r,'parish'),
                    'village': cell(r,'village')})
    stat[reason] += 1

def note(r, field, dropped, why):
    notes.append({'chw_id': cell(r,'chw_id'), 'field': field,
                  'dropped_value': dropped, 'why': why})

for r in rows:
    # --- cadre -------------------------------------------------------------
    role = cell(r, 'role').upper()
    cadre = ROLE.get(role)
    if cadre is None:
        reject(r, 'cadre', 'role is not a register cadre'); continue
    if cadre != CADRE:
        reject(r, 'cadre', 'is a ' + cadre + ', not a ' + CADRE); continue
    if role == 'SUPER CHEW':
        note(r, 'cadre', role, 'imported as chew; the register cadre is two-valued')

    # --- the same person listed twice --------------------------------------
    # 317 rows repeat a chw_id, one person against more than one VHT area.
    cid = cell(r, 'chw_id')
    if cid:
        if cid in seen_id:
            reject(r, 'identity', 'duplicate chw_id (already used by an earlier row)'); continue
        seen_id.add(cid)

    # --- identity ----------------------------------------------------------
    # eCHIS stores one name string, often surname-first. Token one is taken as
    # the first name and the rest as the last, which is what the source supports
    # and no more; both columns are NOT NULL, so a single token cannot be split
    # into a person and is refused rather than doubled.
    raw_name = cell(r, 'chw_name')
    if not raw_name and NAME_FROM_USERNAME:
        slug = re.split(r'_area_|_area$', cell(r, 'username'))[0]
        raw_name = ' '.join(w for w in slug.split('_') if not w.isdigit()).strip()
        if raw_name: note(r, 'chw_name', cell(r,'username'), 'name derived from the username slug')
    toks = [t for t in re.split(r'\s+', raw_name) if t]
    if not toks:
        reject(r, 'identity', 'no name recorded'); continue
    if len(toks) < 2:
        reject(r, 'identity', 'only one name token; last_name cannot be filled'); continue
    first, last = toks[0], ' '.join(toks[1:])

    # --- placement ---------------------------------------------------------
    dcell = cell(r, 'district')
    nd = None
    for f in frags('district', dcell):
        cand = DIST_ALIAS.get(norm(f), norm(f))
        if cand in dnames: nd = cand; break
    if nd is None:
        # district_id is a slug of the same name and occasionally cleaner.
        cand = DIST_ALIAS.get(norm(cell(r,'district_id')), norm(cell(r,'district_id')))
        if cand in dnames: nd = cand
    if nd is None:
        reject(r, 'placement', 'district not in hierarchy'); continue
    did = dnames[nd][0]; L = dlevels(did)

    chit = match(L['county'],    'district',  cell(r, 'county'))
    shit = match(L['subcounty'], 'subcounty', cell(r, 'sub_county'))
    shit = under(shit, chit) or shit
    phit = match(L['parish'],    'parish',    cell(r, 'parish'))
    phit = under(phit, shit) or phit

    if CADRE == 'vht':
        vcell = cell(r, 'village')
        if not vcell:
            reject(r, 'placement', 'no village (a VHT is placed at village)'); continue
        vhit = match(L['village'], 'village', vcell)
        if not vhit:
            reject(r, 'placement', 'village not in hierarchy'); continue
        v2 = under(vhit, phit) or under(vhit, shit) or vhit
        if len(v2) > 1:
            reject(r, 'placement', 'village name is ambiguous in this district'); continue
        lid = v2[0]
    else:
        if not cell(r, 'parish'):
            reject(r, 'placement', 'no parish (a CHEW is placed at parish)'); continue
        if not phit:
            reject(r, 'placement', 'parish not in hierarchy'); continue
        if len(phit) > 1:
            reject(r, 'placement', 'parish name is ambiguous in this district'); continue
        lid = phit[0]
    ch = chain_of(lid)

    # --- already on the register, or twice in this file ---------------------
    person = (lid, frozenset(t.lower() for t in toks))
    if person in already:
        reject(r, 'identity', 'already on the register (same name at the same location)'); continue
    if person in seen_person:
        reject(r, 'identity', 'duplicate person in this file (same name at the same location)'); continue
    seen_person.add(person)

    # --- what eCHIS can fill -----------------------------------------------
    # A number in the CHT is a number this CHW answers on, so phone_owner is yes
    # where there is one -- and blank, not no, where there is not: "no" and "not
    # asked" are different answers and eCHIS never asked.
    prim = phone9(cell(r, 'phone'))
    if cell(r, 'phone') and not prim:
        note(r, 'phone_primary', cell(r,'phone'), 'not a nine-digit number')
    # phone_for_reporting is left blank deliberately: the CHT contact number is
    # not evidence that reporting is done on it.
    owner = 'yes' if prim else ''

    fac = ''
    fcell = cell(r, 'facility')
    if fcell:
        hit = fac_by_district.get(did, {}).get(norm(fcell))
        if hit: fac = hit
        else: note(r, 'facility', fcell, 'no facility of that name in the district')

    sex = SEXMAP.get(cid) or SEXMAP.get(cell(r, 'username')) or ''

    out = {c: '' for c in COLS}
    out.update({
        'first_name': first, 'last_name': last, 'sex': sex, 'cadre': CADRE,
        'district': ch.get('district',''), 'subcounty': ch.get('subcounty',''),
        'parish': ch.get('parish',''),
        'village': ch.get('village','') if CADRE == 'vht' else '',
        'location_code': byid[lid]['code_path'],
        'phone_owner': owner, 'phone_primary': prim, 'facility': fac,
    })
    (ready if sex else pending).append(out)

# ---------------------------------------------------------------- write
def slug(s): return re.sub(r'[^a-z0-9]+','-', s.lower()).strip('-')

def write_set(rs, subdir):
    per = collections.defaultdict(list)
    for o in rs: per[o['district']].append(o)
    made = []
    for d, group in sorted(per.items()):
        fn = os.path.join(OUT, subdir, f'{slug(d)}.csv')
        with open(fn, 'w', newline='', encoding='utf-8') as f:
            w = csv.DictWriter(f, fieldnames=COLS); w.writeheader(); w.writerows(group)
        made.append({'district': d, 'file': f'{subdir}/{os.path.basename(fn)}', 'rows': len(group)})
    return made

manifest = {'cadre': CADRE, 'source_rows': len(rows),
            'ready': write_set(ready, 'ready'),
            'pending_sex': write_set(pending, 'pending')}

with open(os.path.join(OUT,'REJECTS.csv'),'w',newline='',encoding='utf-8') as f:
    w = csv.DictWriter(f, fieldnames=['reason','stage','chw_id','username','chw_name',
        'role','district','county','subcounty','parish','village'])
    w.writeheader(); w.writerows(rejects)
with open(os.path.join(OUT,'NOTES.csv'),'w',newline='',encoding='utf-8') as f:
    w = csv.DictWriter(f, fieldnames=['chw_id','field','dropped_value','why'])
    w.writeheader(); w.writerows(notes)
json.dump(manifest, open(os.path.join(OUT,'manifest.json'),'w'), indent=1)

placed = len(ready) + len(pending)
print(f"source rows        : {len(rows)}")
print(f"placed ({CADRE:>4})       : {placed}   across {len({o['district'] for o in ready+pending})} districts")
print(f"  ready/           : {len(ready)}")
print(f"  pending/ (no sex): {len(pending)}")
print(f"rejected           : {len(rejects)}")
print(f"field-level notes  : {len(notes)}\n")
print("rejections by reason:")
for k,v in stat.most_common(): print(f"  {v:7}  {k}")
if pending and not SEXMAP:
    print("\nNo SEX_FILE given, so nothing is import-ready: chws.sex is NOT NULL and the")
    print("importer requires it. The pending/ files are placed and canonical in every")
    print("other column -- fill sex and they upload as they are.")
