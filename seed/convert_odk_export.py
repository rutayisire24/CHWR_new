#!/usr/bin/env python3
"""Convert the ODK 'National CHWR' export into importer-canonical CSVs.

VHT rows only. Placement is resolved offline and emitted as location_code,
which the importer treats as authoritative; the name columns are written from
the resolved chain so the importer's own code-vs-name check passes.

Writes nothing to the database. Every row that does not make it into a
district file appears in REJECTS.csv with a reason, and every value that was
blanked rather than carried appears in NOTES.csv.
"""
import csv, collections, json, os, re, subprocess, sys

csv.field_size_limit(10**9)

if len(sys.argv) < 3:
    sys.exit("usage: convert_odk_export.py <odk-export.csv> <output-dir>\n"
             "      reads the hierarchy and facilities from $DATABASE_URL")
SRC, OUT = sys.argv[1], sys.argv[2]
os.makedirs(OUT, exist_ok=True)

DB = os.environ.get('DATABASE_URL')
if not DB: sys.exit("DATABASE_URL is not set")

# Cadre decides placement, so it decides which rows this run is even about.
# One cadre per run: the two produce different files for different levels of the
# hierarchy, and mixing them in one upload would hide which rows a report is about.
CADRE = os.environ.get('CADRE', 'vht').strip().lower()
if CADRE not in ('vht', 'chew'): sys.exit("CADRE must be vht or chew")

def dump(sql):
    """Read a table out of the register. The hierarchy the codes are resolved
    against must be the one they will be imported into, which is why this reads
    the database rather than a checked-in extract."""
    out = subprocess.run(['psql', DB, '-tA', '-F\t', '-c', sql],
                         capture_output=True, text=True)
    if out.returncode: sys.exit("psql failed: " + out.stderr.strip())
    return [l.split('\t') for l in out.stdout.splitlines() if l.strip()]

def norm(s): return re.sub(r"[ \t\n\r.'’\-–_/]", '', (s or '').upper())

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

def frags(cell):
    """Every sensible reading of an ODK location cell. Exact matching only --
    the form tags names with a district suffix, on either side, inconsistently."""
    c = (cell or '').strip()
    if not c: return []
    out = [c]
    if '_' in c:
        head, _, tail = c.rpartition('_')
        out += [head, tail, c.replace('_', ' ')]
    n = norm(c)
    for suf in ('SUBCOUNTY', 'SC'):
        if n.endswith(suf) and len(n) > len(suf): out.append(n[:-len(suf)])
    return [f for f in out if f.strip()]

def match(cands, cell):
    for f in frags(cell):
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

# ---------------------------------------------------------------- vocabularies
TOOL = {'bicycle':'bicycle','gumboots':'gumboots','thermometer':'thermometer',
        'medicine_box':'medicine_box','muac_tape':'muac_tape','torches':'torch',
        'register':'register'}
TOOL_DROP = {'none', 'other'}

DOMAIN = {
 'managementofcommonchildhoodillnesses':'iccm',
 'maternalandchildhealth':'maternal_newborn',
 'hiv':'hiv',
 'preventionandcontrolofnoncommunicablediseases':'ncd',
 'immunizationservices':'immunization',
 'sexualandreproductivehealthservicesandrights':'srhr',
 'nutritionservices':'nutrition',
 'integratedessentialclinicalcareservices':'essential_clinical',
 'communitybasedmanagementinformationsystem':'cbmis',
 'environmentalhealthandsanitationservices':'environmental_health',
 'epidemicsanddisasterpreparednessandresponse':'epidemic_response',
 'schoolhealthservices':'school_health',
}
EDU  = {'none','ple','uce','uace','tertiary'}
FREQ = {'monthly','quarterly','annually','one_off'}

COLS = ['first_name','last_name','sex','cadre','age_years','nin',
        'district','subcounty','parish','village','location_code',
        'phone_owner','phone_primary','phone_for_reporting','phone_alternate',
        'facility','service_start_year','households_served','education',
        'english','other_languages','receives_incentive','incentive_frequency',
        'incentive_amount_ugx','tools','tools_functional','services','trained']

NIN_RE = re.compile(r'^[A-Z]{2}[A-Z0-9]{11}[A-Z]$')

def yn(v):
    v = (v or '').strip().lower()
    return 'yes' if v == 'yes' else 'no' if v == 'no' else ''

def num(v, lo, hi, cut=None):
    v = (v or '').strip()
    if not v: return None, False
    try: n = int(v[:cut] if cut else v)
    except ValueError: return None, True
    return (str(n), False) if lo <= n <= hi else (None, True)

# ---------------------------------------------------------------- convert
rows = list(csv.DictReader(open(SRC, newline='', encoding='utf-8-sig')))
out_rows, rejects, notes = [], [], []
seen_nin = {}
stat = collections.Counter()

def reject(r, stage, reason):
    rejects.append({'reason': reason, 'stage': stage,
                    'key': r.get('KEY',''),
                    'first_name': r.get('Hirechy-first_name',''),
                    'last_name': r.get('Hirechy-last_name',''),
                    'nin': r.get('Hirechy-nin',''),
                    'district': r.get('Hirechy-district',''),
                    'subcounty': r.get('Hirechy-subcounty',''),
                    'parish': r.get('Hirechy-parish_select',''),
                    'village': r.get('Hirechy-village_select',''),
                    'chw_type': r.get('Capacity-chw_type','')})
    stat[reason] += 1

def note(r, field, dropped, why):
    notes.append({'key': r.get('KEY',''), 'field': field,
                  'dropped_value': dropped, 'why': why})

for r in rows:
    # --- cadre: one cadre per run, and never a row naming both -------------
    # A row naming both says village and parish at once. There is no neutral
    # reading of it, so it is refused by either run rather than guessed at.
    toks = set((r.get('Capacity-chw_type') or '').lower().split())
    other = 'chew' if CADRE == 'vht' else 'vht'
    if other in toks:
        reject(r, 'cadre', 'names ' + other); continue
    if CADRE not in toks:
        reject(r, 'cadre', 'no ' + CADRE + ' cadre'); continue

    # --- identity ----------------------------------------------------------
    first = (r.get('Hirechy-first_name') or '').strip()
    last  = (r.get('Hirechy-last_name') or '').strip()
    if not first or not last:
        reject(r, 'identity', 'missing name'); continue
    sex = (r.get('other_individual-Sex') or '').strip().lower()
    if sex not in ('male','female'):
        reject(r, 'identity', 'sex not male/female'); continue

    age, bad = num(r.get('other_individual-Age'), 18, 99)
    if bad: note(r, 'age_years', r.get('other_individual-Age',''), 'outside 18-99')

    raw_nin = (r.get('Hirechy-nin') or '').strip()
    nin = ''
    if raw_nin:
        cand = re.sub(r'[^A-Za-z0-9]', '', raw_nin).upper()
        if NIN_RE.match(cand): nin = cand
        else: note(r, 'nin', raw_nin, 'not a valid NIN; imported without one')

    # --- placement: VHT sits at village ------------------------------------
    dcell = (r.get('Hirechy-district') or '').strip()
    nd = DIST_ALIAS.get(norm(dcell), norm(dcell))
    dm = dnames.get(nd)
    if not dm:
        reject(r, 'placement', 'district not in hierarchy'); continue
    did = dm[0]; L = dlevels(did)

    phit = match(L['parish'],  r.get('Hirechy-parish_select'))
    shit = match(L['subcounty'] + L['county'], r.get('Hirechy-subcounty'))

    if CADRE == 'vht':
        vcell = (r.get('Hirechy-village_select') or '').strip()
        if not vcell:
            reject(r, 'placement', 'no village (a VHT is placed at village)'); continue
        vhit = match(L['village'], vcell)
        if not vhit:
            reject(r, 'placement', 'village not in hierarchy'); continue
        v2 = under(vhit, phit) or under(vhit, shit) or vhit
        if len(v2) > 1:
            reject(r, 'placement', 'village name is ambiguous in this district'); continue
        vid = v2[0]
    else:
        # A CHEW is placed at parish, so the village column is not consulted at
        # all — a CHEW row that happens to carry one is still a parish record.
        pcell = (r.get('Hirechy-parish_select') or '').strip()
        if not pcell:
            reject(r, 'placement', 'no parish (a CHEW is placed at parish)'); continue
        if not phit:
            reject(r, 'placement', 'parish not in hierarchy'); continue
        p2 = under(phit, shit) or phit
        if len(p2) > 1:
            reject(r, 'placement', 'parish name is ambiguous in this district'); continue
        vid = p2[0]
    ch = chain_of(vid)

    # --- duplicate NIN: first row wins -------------------------------------
    if nin:
        if nin in seen_nin:
            reject(r, 'identity', 'duplicate NIN (already used by an earlier row)'); continue
        seen_nin[nin] = True

    # --- profile -----------------------------------------------------------
    owner = yn(r.get('other_individual-phones'))
    prim  = (r.get('other_individual-phone_number') or '').strip()
    rep   = yn(r.get('other_individual-phone_reporting'))
    alt   = (r.get('other_individual-other_number') or '').strip()
    if owner == 'yes': alt = ''
    elif owner == 'no': prim, rep = '', ''
    else: prim, rep, alt = '', '', ''

    fac = ''
    fcell = (r.get('facility-facility') or '').strip()
    if fcell:
        hit = fac_by_district.get(did, {}).get(norm(fcell))
        if hit: fac = hit
        else: note(r, 'facility', fcell, 'no facility of that name in the district')

    yr, bad = num(r.get('facility-service_year'), 1960, 2100, cut=4)
    if bad: note(r, 'service_start_year', r.get('facility-service_year',''), 'outside 1960-2100')
    hh, bad = num(r.get('capacity_other-households'), 3, 100000)
    if bad: note(r, 'households_served', r.get('capacity_other-households',''), 'outside 3-100,000')

    edu = (r.get('capacity_other-education') or '').strip().lower()
    if edu and edu not in EDU:
        note(r, 'education', edu, 'not a known education level'); edu = ''

    eng = []
    for t in (r.get('Language-English') or '').split():
        t = t.lower()
        if t in ('speak','read','write'): eng.append(t)
        elif t == 'none': eng = ['none']; break
    english = ';'.join(dict.fromkeys(eng))

    inc = yn(r.get('Financial-recieve_financial'))
    freq = (r.get('Financial-Frequency') or '').strip().lower()
    amt, bad = num(r.get('Financial-financial_incentive'), 1000, 500000)
    if bad: note(r, 'incentive_amount_ugx', r.get('Financial-financial_incentive',''), 'outside 1,000-500,000')
    if inc != 'yes': freq, amt = '', None
    if freq and freq not in FREQ:
        note(r, 'incentive_frequency', freq, 'not a known frequency'); freq = ''

    def maplist(cell, table, drop=(), field=None):
        out = []
        for t in (cell or '').split():
            k = t.lower()
            if k in drop: 
                if field and k == 'other': note(r, field, t, 'or_other value has no place in the closed vocabulary')
                continue
            if k in table: out.append(table[k])
            elif field: note(r, field, t, 'not in the register vocabulary')
        return list(dict.fromkeys(out))

    tools = maplist(r.get('Tooling-Tool'), TOOL, TOOL_DROP, 'tools')
    func  = maplist(r.get('Tooling-tool_functional'), TOOL, TOOL_DROP, 'tools_functional')
    func  = [t for t in func if t in tools]
    svcs  = maplist(r.get('Training-Service_domains'), DOMAIN, (), 'services')
    trn   = maplist(r.get('Training-training'), DOMAIN, (), 'trained')
    trn   = [t for t in trn if t in svcs]

    out_rows.append({
        'first_name': first, 'last_name': last, 'sex': sex, 'cadre': CADRE,
        'age_years': age or '', 'nin': nin,
        'district': ch.get('district',''), 'subcounty': ch.get('subcounty',''),
        'parish': ch.get('parish',''),
        'village': ch.get('village','') if CADRE == 'vht' else '',
        'location_code': byid[vid]['code_path'],
        'phone_owner': owner, 'phone_primary': prim,
        'phone_for_reporting': rep, 'phone_alternate': alt,
        'facility': fac, 'service_start_year': yr or '',
        'households_served': hh or '', 'education': edu,
        'english': english,
        'other_languages': (r.get('Language-Other_language') or '').strip(),
        'receives_incentive': inc, 'incentive_frequency': freq,
        'incentive_amount_ugx': amt or '',
        'tools': ';'.join(tools), 'tools_functional': ';'.join(func),
        'services': ';'.join(svcs), 'trained': ';'.join(trn),
    })

# ---------------------------------------------------------------- write
per = collections.defaultdict(list)
for o in out_rows: per[o['district']].append(o)

def slug(s): return re.sub(r'[^a-z0-9]+','-', s.lower()).strip('-')

manifest = []
for d, rs in sorted(per.items()):
    fn = os.path.join(OUT, f'{slug(d)}.csv')
    with open(fn, 'w', newline='', encoding='utf-8') as f:
        w = csv.DictWriter(f, fieldnames=COLS); w.writeheader(); w.writerows(rs)
    manifest.append({'district': d, 'file': os.path.basename(fn), 'rows': len(rs)})

with open(os.path.join(OUT,'national.csv'), 'w', newline='', encoding='utf-8') as f:
    w = csv.DictWriter(f, fieldnames=COLS); w.writeheader(); w.writerows(out_rows)

with open(os.path.join(OUT,'REJECTS.csv'),'w',newline='',encoding='utf-8') as f:
    w = csv.DictWriter(f, fieldnames=['reason','stage','key','first_name','last_name',
        'nin','district','subcounty','parish','village','chw_type'])
    w.writeheader(); w.writerows(rejects)
with open(os.path.join(OUT,'NOTES.csv'),'w',newline='',encoding='utf-8') as f:
    w = csv.DictWriter(f, fieldnames=['key','field','dropped_value','why'])
    w.writeheader(); w.writerows(notes)
json.dump(manifest, open(os.path.join(OUT,'manifest.json'),'w'), indent=1)

print(f"source rows      : {len(rows)}")
print(f"importable ({CADRE:>4}) : {len(out_rows)}   across {len(per)} districts")
print(f"rejected         : {len(rejects)}")
print(f"field-level notes: {len(notes)}\n")
print("rejections by reason:")
for k,v in stat.most_common(): print(f"  {v:7}  {k}")
