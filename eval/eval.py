#!/usr/bin/env python3
"""
sage-wiki eval — performance benchmark & quality evaluation
=============================================================
Run against any sage-wiki project to measure infrastructure performance
and wiki compilation quality.

Usage:
    python3 eval.py [path/to/sage-wiki/project]
    python3 eval.py --perf-only [path]
    python3 eval.py --quality-only [path]
    python3 eval.py --json [path]         # machine-readable output

Requirements:
    Python 3.10+
    numpy (optional, speeds up vector benchmarks ~10x)

The script is read-only — it never modifies your wiki or database.
"""

import sqlite3
import json
import os
import sys
import time
import struct
import math
import re
import hashlib
import statistics
import argparse
import shutil
from pathlib import Path
from collections import Counter, defaultdict
from concurrent.futures import ThreadPoolExecutor, as_completed
from difflib import SequenceMatcher

try:
    import numpy as np
    HAS_NUMPY = True
except ImportError:
    HAS_NUMPY = False

# ─── CLI ───────────────────────────────────────────────────────────────────────

parser = argparse.ArgumentParser(
    description="sage-wiki eval — performance benchmark & quality evaluation",
    formatter_class=argparse.RawDescriptionHelpFormatter,
    epilog="""
Examples:
    python3 eval.py .                      # evaluate current directory
    python3 eval.py ~/my-wiki              # evaluate a specific project
    python3 eval.py --perf-only .          # performance benchmarks only
    python3 eval.py --quality-only .       # quality metrics only
    python3 eval.py --json . > report.json # machine-readable output
    """,
)
parser.add_argument("path", nargs="?", default=".", help="path to sage-wiki project (default: current directory)")
parser.add_argument("--perf-only", action="store_true", help="run only performance benchmarks")
parser.add_argument("--quality-only", action="store_true", help="run only quality evaluation")
parser.add_argument("--json", action="store_true", help="output results as JSON")
args = parser.parse_args()

run_perf = not args.quality_only
run_quality = not args.perf_only

# ─── Resolve paths ─────────────────────────────────────────────────────────────

DATA_DIR = os.path.abspath(args.path)
DB_PATH = os.path.join(DATA_DIR, ".sage", "wiki.db")
MANIFEST_PATH = os.path.join(DATA_DIR, ".manifest.json")
WIKI_DIR = os.path.join(DATA_DIR, "_wiki")

for p, label in [(DB_PATH, ".sage/wiki.db"), (WIKI_DIR, "_wiki/")]:
    if not os.path.exists(p):
        print(f"Error: {label} not found at {p}", file=sys.stderr)
        print(f"Are you pointing to a sage-wiki project directory?", file=sys.stderr)
        sys.exit(1)

# ─── Helpers ───────────────────────────────────────────────────────────────────

class Timer:
    def __init__(self):
        self.elapsed = 0
    def __enter__(self):
        self._start = time.perf_counter()
        return self
    def __exit__(self, *a):
        self.elapsed = (time.perf_counter() - self._start) * 1000

def get_db():
    db = sqlite3.connect(f"file:{DB_PATH}?mode=ro", uri=True)
    try:
        db.execute("PRAGMA journal_mode=WAL")
    except sqlite3.OperationalError:
        pass  # read-only DB, WAL not writable — that's fine
    db.execute("PRAGMA cache_size=-64000")
    db.execute("PRAGMA mmap_size=268435456")
    return db

def decode_vector(blob):
    if HAS_NUMPY:
        return np.frombuffer(blob, dtype=np.float32)
    return struct.unpack(f'{len(blob)//4}f', blob)

def cosine_sim(a, b):
    if HAS_NUMPY:
        a, b = np.asarray(a), np.asarray(b)
        d = np.dot(a, b)
        n = np.linalg.norm(a) * np.linalg.norm(b)
        return float(d / n) if n > 0 else 0.0
    d = sum(x * y for x, y in zip(a, b))
    na = math.sqrt(sum(x * x for x in a))
    nb = math.sqrt(sum(x * x for x in b))
    return d / (na * nb) if na > 0 and nb > 0 else 0.0

def batch_cosine_search(qvec, matrix, k=10):
    if not HAS_NUMPY or len(matrix) == 0:
        return []
    k = min(k, len(matrix))
    q = np.asarray(qvec).reshape(1, -1)
    sims = (matrix @ q.T).flatten()
    norms = np.linalg.norm(matrix, axis=1)
    qn = np.linalg.norm(q)
    sims = sims / (norms * qn + 1e-10)
    top = np.argpartition(sims, -k)[-k:]
    top = top[np.argsort(sims[top])[::-1]]
    return [(float(sims[i]), i) for i in top]

def fmt(ms):
    if ms < 1: return f"{ms*1000:.0f}µs"
    if ms < 1000: return f"{ms:.2f}ms"
    return f"{ms/1000:.2f}s"

def pct(n, total):
    return f"{n/total*100:.1f}%" if total > 0 else "N/A"

def pstats(times):
    if not times:
        return {}
    s = sorted(times)
    return {
        "p50": statistics.median(s),
        "p95": s[int(len(s) * 0.95)] if len(s) >= 20 else max(s),
        "p99": s[int(len(s) * 0.99)] if len(s) >= 100 else max(s),
        "avg": statistics.mean(s),
        "min": min(s),
        "max": max(s),
        "qps": 1000.0 / statistics.mean(s) if statistics.mean(s) > 0 else 0,
    }

def print_stats(label, times):
    if args.json:
        return
    st = pstats(times)
    if not st:
        return
    print(f"  {label:.<52s} p50={fmt(st['p50']):>8s}  p95={fmt(st['p95']):>8s}  avg={fmt(st['avg']):>8s}  qps={st['qps']:,.0f}")

def section(title):
    if not args.json:
        print(f"\n{'─'*80}")
        print(f"  {title}")
        print(f"{'─'*80}")

def normalize(s):
    return re.sub(r'[^a-z0-9]', '', s.lower())

# ─── Discover dataset ─────────────────────────────────────────────────────────

db = get_db()

tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()]
has_fts = "entries" in tables
has_vec = "vec_entries" in tables
has_entities = "entities" in tables
has_relations = "relations" in tables

entry_count = db.execute("SELECT COUNT(*) FROM entries_content").fetchone()[0] if has_fts else 0
vec_count = db.execute("SELECT COUNT(*) FROM vec_entries").fetchone()[0] if has_vec else 0
entity_count = db.execute("SELECT COUNT(*) FROM entities").fetchone()[0] if has_entities else 0
relation_count = db.execute("SELECT COUNT(*) FROM relations").fetchone()[0] if has_relations else 0

vec_dims = 0
if has_vec and vec_count > 0:
    vec_dims = db.execute("SELECT dimensions FROM vec_entries LIMIT 1").fetchone()[0]

summaries_dir = Path(WIKI_DIR) / "summaries"
concepts_dir = Path(WIKI_DIR) / "concepts"
summary_files = {f.stem: f for f in summaries_dir.glob("*.md")} if summaries_dir.exists() else {}
concept_files = {f.stem: f for f in concepts_dir.glob("*.md")} if concepts_dir.exists() else {}

manifest = {}
if os.path.exists(MANIFEST_PATH):
    with open(MANIFEST_PATH) as f:
        manifest = json.load(f)
source_count = len(manifest.get("sources", {}))

db_size_mb = os.path.getsize(DB_PATH) / 1024 / 1024

if not args.json:
    print(f"\nsage-wiki eval")
    print(f"{'─'*80}")
    print(f"  Project:    {DATA_DIR}")
    print(f"  DB:         {db_size_mb:.1f} MB")
    print(f"  Sources:    {source_count:,}")
    print(f"  FTS entries:{entry_count:,}")
    print(f"  Vectors:    {vec_count:,} × {vec_dims}d")
    print(f"  Entities:   {entity_count:,}")
    print(f"  Relations:  {relation_count:,}")
    print(f"  Summaries:  {len(summary_files):,} files")
    print(f"  Concepts:   {len(concept_files):,} files")
    print(f"  numpy:      {'yes' if HAS_NUMPY else 'no (install for faster vector benchmarks)'}")

results = {"dataset": {
    "path": DATA_DIR, "db_size_mb": round(db_size_mb, 1),
    "sources": source_count, "fts_entries": entry_count,
    "vectors": vec_count, "vec_dims": vec_dims,
    "entities": entity_count, "relations": relation_count,
    "summary_files": len(summary_files), "concept_files": len(concept_files),
}}

import random
random.seed(42)

# ─── Load shared data ─────────────────────────────────────────────────────────

all_entity_ids = []
all_entity_names = []
entities = {}
relations_list = []

if has_entities:
    for row in db.execute("SELECT id, type, name, definition, article_path FROM entities"):
        entities[row[0]] = {"type": row[1], "name": row[2], "definition": row[3], "article_path": row[4]}
    all_entity_ids = list(entities.keys())
    all_entity_names = [e["name"] for e in entities.values() if e["type"] == "concept"]

if has_relations:
    relations_list = db.execute("SELECT source_id, target_id, relation FROM relations").fetchall()

# Build FTS queries from concept names
fts_queries = []
for name in all_entity_names:
    words = name.split()
    if words:
        fts_queries.append(words[0])
        if len(words) >= 2:
            fts_queries.append(f"{words[0]} {words[1]}")
random.shuffle(fts_queries)
fts_queries = fts_queries[:300]
fallbacks = ["algorithm", "network", "memory", "cache", "protocol", "database", "tree", "hash"]
while len(fts_queries) < 100:
    fts_queries.append(random.choice(fallbacks))

db.close()

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#  PERFORMANCE BENCHMARKS
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

if run_perf:
    perf = {}

    # ── FTS5 search ────────────────────────────────────────────────────────────

    if has_fts and entry_count > 0:
        section("Performance: FTS5 BM25 search")
        db = get_db()

        for label, limit in [("top-1", 1), ("top-10", 10)]:
            times = []
            for q in fts_queries[:200]:
                with Timer() as t:
                    try:
                        db.execute(f"SELECT id, rank FROM entries WHERE entries MATCH ? ORDER BY rank LIMIT {limit}", (q,)).fetchall()
                    except:
                        pass
                times.append(t.elapsed)
            print_stats(f"FTS MATCH {label} ({len(times)} queries)", times)
            perf[f"fts_{label}"] = pstats(times)

        # Prefix
        times = []
        for q in fts_queries[:100]:
            if len(q) >= 3:
                with Timer() as t:
                    try:
                        db.execute("SELECT id FROM entries WHERE entries MATCH ? LIMIT 10", (q[:3] + "*",)).fetchall()
                    except:
                        pass
                times.append(t.elapsed)
        if times:
            print_stats(f"FTS prefix search ({len(times)} queries)", times)
            perf["fts_prefix"] = pstats(times)

        db.close()

    # ── Vector search ──────────────────────────────────────────────────────────

    if has_vec and vec_count > 0:
        section("Performance: Vector similarity search")
        db = get_db()

        with Timer() as t_load:
            all_vecs = db.execute("SELECT id, embedding FROM vec_entries").fetchall()
            vec_data = [(vid, decode_vector(vblob)) for vid, vblob in all_vecs]
            if HAS_NUMPY:
                vec_ids = [v[0] for v in vec_data]
                vec_matrix = np.vstack([v[1] for v in vec_data])
        if not args.json: print(f"  {'Vector load':.<52s} {fmt(t_load.elapsed):>8s}  ({vec_count} × {vec_dims}d)")
        perf["vec_load_ms"] = t_load.elapsed

        n_queries = min(30, vec_count)
        times = []
        for i in range(n_queries):
            qvec = vec_data[i][1]
            with Timer() as t:
                if HAS_NUMPY:
                    batch_cosine_search(qvec, vec_matrix, k=10)
                else:
                    scores = [(cosine_sim(qvec, v), vid) for vid, v in vec_data]
                    sorted(scores, reverse=True)[:10]
            times.append(t.elapsed)
        print_stats(f"Cosine top-10 brute force ({n_queries}q × {vec_count})", times)
        perf["vec_top10"] = pstats(times)

        # Hybrid RRF
        times_h = []
        K = 60
        for i in range(min(100, len(fts_queries))):
            q = fts_queries[i]
            with Timer() as t:
                try:
                    bm25 = db.execute("SELECT id, rank FROM entries WHERE entries MATCH ? ORDER BY rank LIMIT 20", (q,)).fetchall()
                except:
                    bm25 = []
                qvec = vec_data[i % len(vec_data)][1]
                if HAS_NUMPY:
                    vtop = batch_cosine_search(qvec, vec_matrix, k=20)
                    vtop = [(s, vec_ids[idx]) for s, idx in vtop]
                else:
                    vs = [(cosine_sim(qvec, v), vid) for vid, v in vec_data]
                    vtop = sorted(vs, reverse=True)[:20]
                rrf = defaultdict(float)
                for rank, (doc_id, _) in enumerate(bm25):
                    rrf[doc_id] += 1.0 / (K + rank + 1)
                for rank, (_, doc_id) in enumerate(vtop):
                    rrf[doc_id] += 1.0 / (K + rank + 1)
                sorted(rrf.items(), key=lambda x: x[1], reverse=True)[:10]
            times_h.append(t.elapsed)
        print_stats("Hybrid RRF (BM25 + vec → top-10)", times_h)
        perf["hybrid_rrf"] = pstats(times_h)

        db.close()

    # ── Graph traversal ────────────────────────────────────────────────────────

    if has_relations and relation_count > 0:
        section("Performance: Ontology graph")
        db = get_db()

        adj = defaultdict(list)
        for s, t, r in relations_list:
            adj[s].append((t, r))

        times = []
        for start in random.choices(all_entity_ids, k=min(200, len(all_entity_ids))):
            with Timer() as t:
                visited = set()
                queue = [(start, 0)]
                while queue:
                    node, depth = queue.pop(0)
                    if node in visited or depth > 5:
                        continue
                    visited.add(node)
                    for nb, _ in adj.get(node, []):
                        if nb not in visited:
                            queue.append((nb, depth + 1))
            times.append(t.elapsed)
        print_stats(f"BFS depth≤5 ({len(times)} traversals)", times)
        perf["bfs"] = pstats(times)

        # Cycle detection
        WHITE, GRAY, BLACK = 0, 1, 2
        color = {eid: WHITE for eid in all_entity_ids}
        cycle_state = {"count": 0}
        sys.setrecursionlimit(max(10000, len(all_entity_ids) + 100))
        def dfs(u):
            color[u] = GRAY
            for v, _ in adj.get(u, []):
                if color.get(v) == GRAY:
                    cycle_state["count"] += 1
                elif color.get(v) == WHITE:
                    dfs(u=v)
            color[u] = BLACK
        with Timer() as t_cyc:
            for eid in all_entity_ids:
                if color[eid] == WHITE:
                    try:
                        dfs(eid)
                    except RecursionError:
                        pass
        cycles = cycle_state["count"]
        if not args.json: print(f"  {'Cycle detection (full DFS)':.<52s} {fmt(t_cyc.elapsed):>8s}  cycles={cycles}")
        perf["cycle_detection_ms"] = t_cyc.elapsed
        perf["cycles_found"] = cycles

        db.close()

    # ── Write throughput ───────────────────────────────────────────────────────

    section("Performance: Write throughput")
    tmp_db = os.path.join(DATA_DIR, ".sage", "_bench_tmp.db")
    shutil.copy2(DB_PATH, tmp_db)
    db = sqlite3.connect(tmp_db)
    db.execute("PRAGMA journal_mode=WAL")
    db.execute("PRAGMA synchronous=NORMAL")

    for batch in [1, 10, 100]:
        with Timer() as t:
            for i in range(batch):
                eid = f"bench-{i}-{random.randint(0,999999)}"
                db.execute("INSERT INTO entries(id,content,tags,article_path) VALUES(?,?,?,?)",
                          (eid, f"Benchmark content {'x'*200}", "bench", f"_wiki/bench/{eid}.md"))
            db.commit()
        eps = batch * 1000 / t.elapsed if t.elapsed > 0 else 0
        if not args.json: print(f"  {'FTS insert batch=' + str(batch):.<52s} {fmt(t.elapsed):>8s}  {eps:,.0f} entries/s")
        perf[f"write_fts_batch{batch}"] = {"total_ms": t.elapsed, "per_entry_ms": t.elapsed / batch, "eps": eps}

    if has_vec and vec_dims > 0:
        with Timer() as t:
            for i in range(10):
                vid = f"bench-vec-{i}-{random.randint(0,999999)}"
                blob = struct.pack(f'{vec_dims}f', *[random.gauss(0, 1) for _ in range(vec_dims)])
                db.execute("INSERT INTO vec_entries(id,embedding,dimensions) VALUES(?,?,?)", (vid, blob, vec_dims))
            db.commit()
        if not args.json: print(f"  {'Vector insert batch=10 (' + str(vec_dims) + 'd)':.<52s} {fmt(t.elapsed):>8s}  {fmt(t.elapsed/10)}/entry")
        perf["write_vec_batch10"] = {"total_ms": t.elapsed, "per_entry_ms": t.elapsed / 10}

    db.close()
    os.remove(tmp_db)
    for ext in ["-wal", "-shm"]:
        p = tmp_db + ext
        if os.path.exists(p):
            os.remove(p)

    # ── Sustained load ─────────────────────────────────────────────────────────

    section("Performance: Sustained load (3 seconds)")
    db = get_db()
    t_start = time.perf_counter()
    duration = 3.0
    ops = 0
    op_times = []
    while time.perf_counter() - t_start < duration:
        op = random.choice(["fts", "fts", "fts", "entity", "neighbor"])
        if op == "fts" and has_fts:
            with Timer() as t:
                try:
                    db.execute("SELECT id FROM entries WHERE entries MATCH ? LIMIT 10", (random.choice(fts_queries),)).fetchall()
                except:
                    pass
        elif op == "entity" and has_entities:
            with Timer() as t:
                db.execute("SELECT * FROM entities WHERE id=?", (random.choice(all_entity_ids),)).fetchone()
        elif op == "neighbor" and has_relations:
            with Timer() as t:
                db.execute("SELECT * FROM relations WHERE source_id=?", (random.choice(all_entity_ids),)).fetchall()
        else:
            continue
        op_times.append(t.elapsed)
        ops += 1

    actual = time.perf_counter() - t_start
    throughput = ops / actual
    print_stats(f"Mixed read ops ({ops:,} in {actual:.1f}s)", op_times)
    if not args.json: print(f"  {'Sustained throughput':.<52s} {throughput:,.0f} ops/s")
    perf["sustained"] = {**pstats(op_times), "ops": ops, "throughput": round(throughput)}
    db.close()

    # ── File I/O ───────────────────────────────────────────────────────────────

    section("Performance: File I/O")
    all_files = list(summary_files.values()) + list(concept_files.values())
    if all_files:
        with Timer() as t:
            total_bytes = sum(f.read_bytes().__len__() for f in all_files)
        if not args.json: print(f"  {'Read all wiki files (' + str(len(all_files)) + ')':.<52s} {fmt(t.elapsed):>8s}  {total_bytes/1024/1024:.1f} MB")
        perf["read_all_files"] = {"ms": t.elapsed, "files": len(all_files), "mb": round(total_bytes / 1024 / 1024, 1)}

        with Timer() as t:
            for f in all_files:
                hashlib.sha256(f.read_bytes()).hexdigest()
        if not args.json: print(f"  {'SHA-256 hash all files':.<52s} {fmt(t.elapsed):>8s}")
        perf["hash_all_files_ms"] = t.elapsed

    results["performance"] = perf

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#  QUALITY EVALUATION
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

if run_quality:
    qual = {}
    link_pattern = re.compile(r'\[\[([^\]|]+)(?:\|[^\]]+)?\]\]')

    # Parse all files
    def parse_wiki_file(path):
        text = path.read_text(errors="replace")
        fm = {}
        if text.startswith("---"):
            end = text.find("---", 3)
            if end > 0:
                for line in text[3:end].strip().split("\n"):
                    if ":" in line:
                        k, v = line.split(":", 1)
                        fm[k.strip()] = v.strip()
        return {"text": text, "fm": fm, "words": len(text.split()), "chars": len(text)}

    summary_data = {n: parse_wiki_file(p) for n, p in summary_files.items()}
    concept_data = {n: parse_wiki_file(p) for n, p in concept_files.items()}

    # ── 1. Fact extraction density ─────────────────────────────────────────────

    section("Quality: Fact extraction density")

    summary_claims = {}
    for name, d in summary_data.items():
        m = re.search(r'## Key claims\s*\n(.*?)(?=\n##|\Z)', d["text"], re.DOTALL)
        if m:
            summary_claims[name] = len([l for l in m.group(1).split("\n") if l.strip().startswith(("*", "-", "•"))])
        else:
            summary_claims[name] = 0

    concept_facts = {}
    for name, d in concept_data.items():
        body = d["text"].split("---", 2)[-1] if "---" in d["text"] else d["text"]
        concept_facts[name] = len([l for l in body.split("\n") if l.strip().startswith(("*", "-", "•", "1.", "2.", "3.")) and len(l.strip()) > 15])

    total_claims = sum(summary_claims.values())
    with_claims = sum(1 for v in summary_claims.values() if v > 0)
    avg_claims = statistics.mean(summary_claims.values()) if summary_claims else 0
    avg_facts = statistics.mean(concept_facts.values()) if concept_facts else 0

    qual["fact_extraction"] = {
        "total_claims": total_claims,
        "summaries_with_claims": with_claims,
        "summaries_total": len(summary_data),
        "rate": round(with_claims / len(summary_data), 3) if summary_data else 0,
        "avg_claims_per_summary": round(avg_claims, 1),
        "avg_facts_per_concept": round(avg_facts, 1),
    }
    if not args.json:
        print(f"  Summaries with claims:              {with_claims} / {len(summary_data)} ({pct(with_claims, len(summary_data))})")
        print(f"  Avg claims per summary:             {avg_claims:.1f}")
        print(f"  Avg facts per concept:              {avg_facts:.1f}")

    # ── 2. Concept coverage ────────────────────────────────────────────────────

    section("Quality: Concept coverage")

    mentioned = set()
    for d in summary_data.values():
        m = re.search(r'## Concepts\s*\n(.*?)(?=\n##|\Z)', d["text"], re.DOTALL)
        if m:
            raw = m.group(1).strip()
            concepts = [c.strip().strip("*-• ") for c in (raw.split(",") if "," in raw else raw.split("\n")) if c.strip()]
            mentioned.update(c for c in concepts if len(c) > 1)

    cfn = {normalize(n): n for n in concept_files}
    matched = sum(1 for c in mentioned if normalize(c) in cfn)
    coverage = matched / len(mentioned) if mentioned else 0

    qual["concept_coverage"] = {
        "mentioned": len(mentioned),
        "matched_to_article": matched,
        "rate": round(coverage, 3),
    }
    if not args.json:
        print(f"  Concepts mentioned in summaries:    {len(mentioned)}")
        print(f"  Matched to concept article:         {matched} ({coverage:.1%})")

    # ── 3. Cross-reference integrity ───────────────────────────────────────────

    section("Quality: Cross-reference integrity")

    all_stems = set(summary_files) | set(concept_files)
    all_stems_n = {normalize(s): s for s in all_stems}
    total_links = valid = broken = self_links = 0
    broken_targets = Counter()

    for name, d in concept_data.items():
        for link in link_pattern.findall(d["text"]):
            total_links += 1
            lc = link.strip()
            if normalize(lc) == normalize(name):
                self_links += 1
                continue
            if normalize(lc) in all_stems_n or lc in all_stems:
                valid += 1
            else:
                broken += 1
                broken_targets[lc] += 1

    integrity = valid / (total_links - self_links) if (total_links - self_links) > 0 else 0
    qual["crossref"] = {
        "total_links": total_links,
        "valid": valid,
        "broken": broken,
        "self_links": self_links,
        "integrity_rate": round(integrity, 3),
        "top_broken": broken_targets.most_common(10),
    }
    if not args.json:
        print(f"  Total wikilinks:                    {total_links}")
        print(f"  Valid:                              {valid} ({pct(valid, total_links)})")
        print(f"  Broken:                             {broken} ({pct(broken, total_links)})")
        print(f"  Link integrity rate:                {integrity:.1%}")
        if broken_targets:
            print(f"  Top broken targets:")
            for tgt, cnt in broken_targets.most_common(5):
                print(f"    · {tgt} ({cnt}×)")

    # ── 4. Search recall ───────────────────────────────────────────────────────

    section("Quality: Search recall")

    db = get_db()
    sample = list(concept_data.items())[:500]
    r1 = r5 = r10 = searchable = 0

    for name, d in sample:
        query = name.replace("-", " ").replace("_", " ")
        if len(query) < 2:
            continue
        searchable += 1
        try:
            rows = db.execute("SELECT article_path FROM entries WHERE entries MATCH ? ORDER BY rank LIMIT 10", (query,)).fetchall()
        except:
            continue
        for i, (rp,) in enumerate(rows):
            if rp and (name in rp or normalize(name) in normalize(rp)):
                if i == 0: r1 += 1
                if i < 5: r5 += 1
                if i < 10: r10 += 1
                break

    qual["search_recall"] = {
        "searched": searchable,
        "recall_at_1": r1, "recall_at_1_rate": round(r1 / searchable, 3) if searchable else 0,
        "recall_at_5": r5, "recall_at_5_rate": round(r5 / searchable, 3) if searchable else 0,
        "recall_at_10": r10, "recall_at_10_rate": round(r10 / searchable, 3) if searchable else 0,
    }
    if not args.json:
        print(f"  Recall@1:                           {r1} / {searchable} ({pct(r1, searchable)})")
        print(f"  Recall@5:                           {r5} / {searchable} ({pct(r5, searchable)})")
        print(f"  Recall@10:                          {r10} / {searchable} ({pct(r10, searchable)})")
    db.close()

    # ── 5. Source citation coverage ────────────────────────────────────────────

    section("Quality: Source citation coverage")

    with_sources = sum(1 for d in concept_data.values() if d["fm"].get("sources", "").strip() not in ("", "[]"))
    cite_rate = with_sources / len(concept_data) if concept_data else 0

    qual["citation"] = {"concepts_with_sources": with_sources, "total": len(concept_data), "rate": round(cite_rate, 3)}
    if not args.json:
        print(f"  Concepts citing sources:            {with_sources} / {len(concept_data)} ({cite_rate:.1%})")

    # ── 6. Ontology health ─────────────────────────────────────────────────────

    section("Quality: Ontology health")

    adj_set = defaultdict(set)
    rev_set = defaultdict(set)
    for s, t, r in relations_list:
        adj_set[s].add(t)
        rev_set[t].add(s)

    orphans = sum(1 for eid in entities if eid not in adj_set and eid not in rev_set)
    rel_types = Counter(r for _, _, r in relations_list)
    type_dist = Counter(e["type"] for e in entities.values())
    degree = {eid: len(adj_set.get(eid, set())) + len(rev_set.get(eid, set())) for eid in entities}

    qual["ontology"] = {
        "entities": entity_count,
        "relations": relation_count,
        "orphans": orphans,
        "orphan_rate": round(orphans / entity_count, 3) if entity_count else 0,
        "entity_types": dict(type_dist),
        "relation_types": dict(rel_types),
        "avg_degree": round(statistics.mean(degree.values()), 2) if degree else 0,
    }
    if not args.json:
        print(f"  Orphan entities:                    {orphans} / {entity_count} ({pct(orphans, entity_count)})")
        print(f"  Avg degree:                         {statistics.mean(degree.values()):.2f}" if degree else "")
        print(f"  Relation types:                     {dict(rel_types)}")

    # ── 7. Content quality ─────────────────────────────────────────────────────

    section("Quality: Content depth & structure")

    sw = [d["words"] for d in summary_data.values()]
    cw = [d["words"] for d in concept_data.values()]

    expected_sum = ["Key claims", "Methodology", "Results", "Concepts"]
    expected_con = ["Definition", "How it works"]
    sum_sections = {s: sum(1 for d in summary_data.values() if f"## {s}" in d["text"]) for s in expected_sum}
    con_sections = {s: sum(1 for d in concept_data.values() if f"## {s}" in d["text"]) for s in expected_con}

    # Confidence
    conf_dist = Counter(d["fm"].get("confidence", "unset") for d in concept_data.values())

    qual["content"] = {
        "summary_words": {"avg": round(statistics.mean(sw)), "median": round(statistics.median(sw)), "std": round(statistics.stdev(sw))} if sw else {},
        "concept_words": {"avg": round(statistics.mean(cw)), "median": round(statistics.median(cw)), "std": round(statistics.stdev(cw))} if cw else {},
        "summary_sections": {s: {"count": c, "rate": round(c / len(summary_data), 3)} for s, c in sum_sections.items()} if summary_data else {},
        "concept_sections": {s: {"count": c, "rate": round(c / len(concept_data), 3)} for s, c in con_sections.items()} if concept_data else {},
        "confidence_distribution": dict(conf_dist.most_common(5)),
    }
    if not args.json:
        print(f"  Summary avg words:                  {statistics.mean(sw):.0f} (σ={statistics.stdev(sw):.0f})" if sw else "")
        print(f"  Concept avg words:                  {statistics.mean(cw):.0f} (σ={statistics.stdev(cw):.0f})" if cw else "")
        for s, c in sum_sections.items():
            print(f"  '## {s}' in summaries:   {c} / {len(summary_data)} ({pct(c, len(summary_data))})")
        for s, c in con_sections.items():
            print(f"  '## {s}' in concepts:    {c} / {len(concept_data)} ({pct(c, len(concept_data))})")

    # ── 8. Deduplication ───────────────────────────────────────────────────────

    section("Quality: Deduplication")

    names = list(concept_files.keys())
    dupes = []
    for i, n1 in enumerate(names):
        a = normalize(n1)
        for n2 in names[i + 1:]:
            b = normalize(n2)
            if a == b:
                dupes.append((n1, n2, 1.0))
            elif len(a) > 4 and len(b) > 4 and SequenceMatcher(None, a, b).ratio() > 0.85:
                dupes.append((n1, n2, SequenceMatcher(None, a, b).ratio()))

    with_aliases = sum(1 for d in concept_data.values() if d["fm"].get("aliases", "").strip() not in ("", "[]"))

    qual["dedup"] = {
        "near_duplicate_pairs": len(dupes),
        "concepts_with_aliases": with_aliases,
        "alias_rate": round(with_aliases / len(concept_data), 3) if concept_data else 0,
    }
    if not args.json:
        print(f"  Near-duplicate pairs (>85%):        {len(dupes)}")
        print(f"  Concepts with aliases:              {with_aliases} / {len(concept_data)} ({pct(with_aliases, len(concept_data))})")
        if dupes:
            print(f"  Sample:")
            for n1, n2, r in sorted(dupes, key=lambda x: -x[2])[:5]:
                print(f"    {r:.0%}  {n1}  ↔  {n2}")

    # ── 9. Wiki connectivity ───────────────────────────────────────────────────

    section("Quality: Wiki connectivity")

    inbound = Counter()
    for name, d in concept_data.items():
        for link in link_pattern.findall(d["text"]):
            ln = normalize(link.strip())
            if ln in cfn:
                inbound[cfn[ln]] += 1

    with_inbound = sum(1 for c in concept_files if inbound.get(c, 0) > 0)
    orphan_concepts = sum(1 for c in concept_files if inbound.get(c, 0) == 0)
    inbound_values = [inbound.get(c, 0) for c in concept_files]

    qual["connectivity"] = {
        "with_inbound": with_inbound,
        "orphan_concepts": orphan_concepts,
        "connectivity_rate": round(with_inbound / len(concept_files), 3) if concept_files else 0,
        "avg_inbound": round(statistics.mean(inbound_values), 2) if inbound_values else 0,
        "max_inbound": max(inbound_values) if inbound_values else 0,
        "top_hubs": [(name, count) for name, count in inbound.most_common(10)],
    }
    if not args.json:
        print(f"  Concepts with inbound links:        {with_inbound} / {len(concept_files)} ({pct(with_inbound, len(concept_files))})")
        print(f"  Orphan concepts:                    {orphan_concepts} ({pct(orphan_concepts, len(concept_files))})")
        print(f"  Avg inbound links:                  {statistics.mean(inbound_values):.2f}" if inbound_values else "")
        print(f"  Top hubs:")
        for name, cnt in inbound.most_common(5):
            print(f"    {cnt:>4d}  {name}")

    results["quality"] = qual

    # ── Scorecard ──────────────────────────────────────────────────────────────

    section("QUALITY SCORECARD")

    scores = [
        ("Fact extraction rate",        qual["fact_extraction"]["rate"]),
        ("Concept coverage",            qual["concept_coverage"]["rate"]),
        ("Cross-reference integrity",   qual["crossref"]["integrity_rate"]),
        ("Search recall@10",            qual["search_recall"]["recall_at_10_rate"]),
        ("Source citation rate",         qual["citation"]["rate"]),
        ("Structural — Key claims",     qual["content"]["summary_sections"].get("Key claims", {}).get("rate", 0)),
        ("Structural — Definition",     qual["content"]["concept_sections"].get("Definition", {}).get("rate", 0)),
        ("Alias coverage",              qual["dedup"]["alias_rate"]),
        ("Wiki connectivity",           qual["connectivity"]["connectivity_rate"]),
    ]

    if not args.json:
        print()
        for label, score in scores:
            bar = "█" * int(score * 30) + "░" * (30 - int(score * 30))
            print(f"  {label:.<45s} {bar} {score:>6.1%}")

        overall = statistics.mean(s for _, s in scores)
        print(f"\n  {'OVERALL':.<45s} {'':>30s} {overall:>6.1%}")

    results["quality"]["scorecard"] = {label: round(score, 3) for label, score in scores}
    results["quality"]["overall_score"] = round(statistics.mean(s for _, s in scores), 3)

# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
#  OUTPUT
# ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

if args.json:
    # Clean up non-serializable values
    def clean(obj):
        if isinstance(obj, dict):
            return {k: clean(v) for k, v in obj.items()}
        if isinstance(obj, list):
            return [clean(v) for v in obj]
        if isinstance(obj, float):
            if math.isinf(obj) or math.isnan(obj):
                return None
            return round(obj, 3)
        return obj
    print(json.dumps(clean(results), indent=2))