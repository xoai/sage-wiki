#!/usr/bin/env python3
"""
Test suite for sage-wiki eval.py

Generates a synthetic sage-wiki project with known properties,
runs eval.py against it, and validates the results.

Usage:
    python3 eval_test.py              # run all tests
    python3 eval_test.py -v           # verbose
    python3 eval_test.py -k test_fts  # run specific test
"""

import json
import os
import random
import shutil
import sqlite3
import struct
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

EVAL_SCRIPT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "eval.py")

# ─── Fixture generation ───────────────────────────────────────────────────────

def generate_fixture(path, *, n_sources=50, n_concepts=80, n_summaries=50,
                     n_vectors=40, vec_dims=128, n_entities=100,
                     n_relations=120, broken_link_ratio=0.3, alias_ratio=0.8,
                     seed=42):
    """
    Create a synthetic sage-wiki project at `path` with known properties.

    Returns a dict of expected values for assertion.
    """
    random.seed(seed)
    os.makedirs(path, exist_ok=True)

    # ── Directories ────────────────────────────────────────────────────────────
    sage_dir = os.path.join(path, ".sage")
    wiki_dir = os.path.join(path, "_wiki")
    summaries_dir = os.path.join(wiki_dir, "summaries")
    concepts_dir = os.path.join(wiki_dir, "concepts")
    for d in [sage_dir, summaries_dir, concepts_dir]:
        os.makedirs(d, exist_ok=True)

    # ── Concept names ──────────────────────────────────────────────────────────
    concept_names = [
        "binary-search", "hash-table", "linked-list", "dynamic-programming",
        "graph-traversal", "sorting-algorithm", "red-black-tree", "bloom-filter",
        "trie", "heap", "stack", "queue", "b-tree", "skip-list", "avl-tree",
        "merkle-tree", "radix-sort", "quicksort", "mergesort", "topological-sort",
        "dijkstra-algorithm", "bellman-ford", "a-star-search", "kruskal-algorithm",
        "prim-algorithm", "union-find", "segment-tree", "fenwick-tree",
        "suffix-array", "knuth-morris-pratt", "rabin-karp", "boyer-moore",
        "convex-hull", "voronoi-diagram", "delaunay-triangulation",
        "fast-fourier-transform", "matrix-multiplication", "linear-regression",
        "gradient-descent", "backpropagation", "attention-mechanism",
        "transformer-architecture", "convolutional-neural-network",
        "recurrent-neural-network", "generative-adversarial-network",
        "variational-autoencoder", "reinforcement-learning", "monte-carlo-method",
        "markov-chain", "bayesian-inference", "principal-component-analysis",
        "k-means-clustering", "random-forest", "support-vector-machine",
        "naive-bayes", "decision-tree", "xgboost", "dropout-regularization",
        "batch-normalization", "residual-connection", "self-attention",
        "multi-head-attention", "positional-encoding", "beam-search",
        "greedy-decoding", "teacher-forcing", "curriculum-learning",
        "transfer-learning", "fine-tuning", "knowledge-distillation",
        "model-pruning", "quantization", "neural-architecture-search",
        "hyperparameter-optimization", "cross-validation", "confusion-matrix",
        "precision-recall", "roc-curve", "f1-score", "mean-squared-error",
        "cross-entropy-loss", "adam-optimizer",
    ]
    # Extend if n_concepts exceeds the hardcoded list
    while len(concept_names) < n_concepts:
        concept_names.append(f"concept-{len(concept_names):04d}")
    concept_names = concept_names[:n_concepts]

    # ── Source names ───────────────────────────────────────────────────────────
    source_names = [f"source-doc-{i}" for i in range(n_sources)]

    # ── Generate concept articles ──────────────────────────────────────────────
    all_concept_links = []
    concepts_with_aliases = 0
    concepts_with_sources = 0
    total_valid_links = 0
    total_broken_links = 0

    for i, name in enumerate(concept_names):
        # Pick some links (mix of valid and broken)
        n_links = random.randint(3, 12)
        links = []
        for _ in range(n_links):
            if random.random() < broken_link_ratio:
                links.append(f"[[nonexistent-concept-{random.randint(0,999)}]]")
                total_broken_links += 1
            else:
                target = random.choice(concept_names)
                if target != name:
                    links.append(f"[[{target}]]")
                    total_valid_links += 1

        # Aliases
        has_alias = random.random() < alias_ratio
        if has_alias:
            concepts_with_aliases += 1
            aliases = f"[{name.replace('-', ' ').title()}, {name.upper().replace('-', '_')}]"
        else:
            aliases = "[]"

        # Source refs
        has_source = random.random() < 0.9
        if has_source:
            concepts_with_sources += 1
            src = random.choice(source_names)
            sources_str = f"[content/docs/{src}.md]"
        else:
            sources_str = "[]"

        # Confidence
        confidence = random.choice(["high", "high", "high", "medium", "high"])

        content = textwrap.dedent(f"""\
        ---
        concept: {name}
        aliases: {aliases}
        sources: {sources_str}
        confidence: {confidence}
        ---

        ## Definition
        **{name.replace('-', ' ').title()}** is a fundamental concept in computer science
        that relates to {' '.join(random.choices(['data structures', 'algorithms', 'machine learning', 'optimization'], k=2))}.

        ## How it works
        The mechanism involves several key steps:
        * First, the input data is preprocessed using {random.choice(concept_names).replace('-', ' ')}
        * Then, the core algorithm applies {random.choice(['divide and conquer', 'dynamic programming', 'greedy approach'])}
        * Finally, the result is validated against known constraints

        ## Connections
        This concept is related to {' '.join(links[:5])}.

        See also: {' '.join(links[5:])}.

        ## Applications
        - Used in database indexing for efficient lookups
        - Applied in network routing algorithms
        - Foundation for more advanced techniques like {random.choice(concept_names).replace('-', ' ')}
        """)

        (Path(concepts_dir) / f"{name}.md").write_text(content)

    # ── Generate summaries ─────────────────────────────────────────────────────
    summaries_with_claims = 0
    all_mentioned_concepts = set()

    for i in range(min(n_summaries, n_sources)):
        name = source_names[i]

        # Randomly include or exclude Key claims section
        has_claims = random.random() < 0.75
        if has_claims:
            summaries_with_claims += 1
            n_claims = random.randint(2, 6)
            claims = "\n".join(f"* Claim {j+1} about {random.choice(concept_names).replace('-', ' ')}" for j in range(n_claims))
            claims_section = f"## Key claims\n{claims}\n"
        else:
            claims_section = ""

        # Concepts section
        mentioned = random.sample(concept_names, min(random.randint(3, 8), len(concept_names)))
        # Add some that won't match (noise)
        mentioned += [f"$O(n^{random.randint(2,5)})$" for _ in range(random.randint(0, 3))]
        all_mentioned_concepts.update(mentioned)
        concepts_section = "## Concepts\n" + ", ".join(mentioned)

        content = textwrap.dedent(f"""\
        ---
        source: content/docs/{name}.md
        source_type: article
        compiled_at: 2026-04-04T10:00:00Z
        chunk_count: 1
        ---

        {claims_section}
        ## Methodology
        The source uses a {random.choice(['comparative', 'experimental', 'analytical'])} approach.

        ## Results
        * Finding 1: Performance improved by {random.randint(10,90)}%
        * Finding 2: The method generalizes to {random.choice(['graphs', 'trees', 'sequences'])}

        {concepts_section}
        """)

        (Path(summaries_dir) / f"{name}.md").write_text(content)

    # ── Generate manifest ──────────────────────────────────────────────────────
    manifest = {"version": 2, "sources": {}}
    for name in source_names:
        manifest["sources"][f"content/docs/{name}.md"] = {
            "hash": f"sha256:{random.randbytes(32).hex()}",
            "type": "article",
            "size_bytes": random.randint(500, 20000),
            "added_at": "2026-04-04T10:00:00Z",
            "compiled_at": "2026-04-04T10:00:00Z",
            "status": "compiled",
        }
    with open(os.path.join(path, ".manifest.json"), "w") as f:
        json.dump(manifest, f)

    # ── Generate database ──────────────────────────────────────────────────────
    db_path = os.path.join(sage_dir, "wiki.db")
    db = sqlite3.connect(db_path)

    db.execute("CREATE TABLE schema_version (version INTEGER NOT NULL)")
    db.execute("INSERT INTO schema_version VALUES (1)")

    db.execute("""CREATE VIRTUAL TABLE entries USING fts5(
        id, content, tags, article_path,
        tokenize='porter unicode61'
    )""")

    db.execute("""CREATE TABLE vec_entries (
        id TEXT PRIMARY KEY,
        embedding BLOB NOT NULL,
        dimensions INTEGER NOT NULL
    )""")

    db.execute("""CREATE TABLE entities (
        id TEXT PRIMARY KEY,
        type TEXT NOT NULL CHECK(type IN ('concept','technique','source','claim','artifact')),
        name TEXT NOT NULL,
        definition TEXT,
        article_path TEXT,
        metadata JSON,
        created_at TEXT,
        updated_at TEXT
    )""")

    db.execute("""CREATE TABLE relations (
        id TEXT PRIMARY KEY,
        source_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
        target_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
        relation TEXT NOT NULL CHECK(relation IN (
            'implements','extends','optimizes','contradicts',
            'cites','prerequisite_of','trades_off','derived_from'
        )),
        metadata JSON,
        created_at TEXT,
        UNIQUE(source_id, target_id, relation)
    )""")

    db.execute("CREATE TABLE learnings (id TEXT PRIMARY KEY, type TEXT NOT NULL, content TEXT NOT NULL, tags TEXT, created_at TEXT, source_lint_pass TEXT)")

    # Insert FTS entries
    for name in concept_names:
        content = (Path(concepts_dir) / f"{name}.md").read_text()
        db.execute("INSERT INTO entries(id,content,tags,article_path) VALUES(?,?,?,?)",
                  (f"concept:{name}", content, "concept", f"_wiki/concepts/{name}.md"))

    for i in range(min(n_summaries, n_sources)):
        name = source_names[i]
        content = (Path(summaries_dir) / f"{name}.md").read_text()
        db.execute("INSERT INTO entries(id,content,tags,article_path) VALUES(?,?,?,?)",
                  (f"source:{name}", content, "article", f"_wiki/summaries/{name}.md"))

    # Insert vectors
    for i in range(min(n_vectors, len(concept_names))):
        name = concept_names[i]
        vec = struct.pack(f'{vec_dims}f', *[random.gauss(0, 1) for _ in range(vec_dims)])
        db.execute("INSERT INTO vec_entries(id,embedding,dimensions) VALUES(?,?,?)",
                  (f"concept:{name}", vec, vec_dims))

    # Insert entities
    entity_ids = []
    for i, name in enumerate(concept_names[:n_entities // 2]):
        eid = f"concept:{name}"
        db.execute("INSERT INTO entities(id,type,name,article_path,created_at,updated_at) VALUES(?,?,?,?,datetime('now'),datetime('now'))",
                  (eid, "concept", name.replace("-", " ").title(), f"_wiki/concepts/{name}.md"))
        entity_ids.append(eid)

    for i in range(min(n_entities // 2, n_sources)):
        eid = f"source:{source_names[i]}"
        db.execute("INSERT INTO entities(id,type,name,article_path,created_at,updated_at) VALUES(?,?,?,?,datetime('now'),datetime('now'))",
                  (eid, "source", source_names[i], f"_wiki/summaries/{source_names[i]}.md"))
        entity_ids.append(eid)

    # Insert relations
    actual_relations = 0
    for i in range(n_relations):
        if len(entity_ids) < 2:
            break
        s = random.choice(entity_ids)
        t = random.choice(entity_ids)
        if s == t:
            continue
        rel = random.choice(["cites", "cites", "cites", "implements", "derived_from"])
        try:
            db.execute("INSERT INTO relations(id,source_id,target_id,relation,created_at) VALUES(?,?,?,?,datetime('now'))",
                      (f"rel-{i}", s, t, rel))
            actual_relations += 1
        except sqlite3.IntegrityError:
            pass

    db.commit()
    db.close()

    return {
        "n_sources": n_sources,
        "n_concepts": len(concept_names),
        "n_summaries": min(n_summaries, n_sources),
        "n_vectors": min(n_vectors, len(concept_names)),
        "vec_dims": vec_dims,
        "n_entities": len(entity_ids),
        "n_relations": actual_relations,
        "summaries_with_claims": summaries_with_claims,
        "concepts_with_aliases": concepts_with_aliases,
        "concepts_with_sources": concepts_with_sources,
        "concept_names": concept_names,
    }


# ─── Tests ─────────────────────────────────────────────────────────────────────

class TestEvalFixture(unittest.TestCase):
    """Test fixture generation itself."""

    @classmethod
    def setUpClass(cls):
        cls.tmpdir = tempfile.mkdtemp(prefix="sage-wiki-test-")
        cls.expected = generate_fixture(cls.tmpdir, n_sources=30, n_concepts=40,
                                         n_summaries=30, n_vectors=20, vec_dims=64,
                                         n_entities=60, n_relations=80)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmpdir, ignore_errors=True)

    def test_directories_created(self):
        self.assertTrue(os.path.isdir(os.path.join(self.tmpdir, ".sage")))
        self.assertTrue(os.path.isdir(os.path.join(self.tmpdir, "_wiki", "summaries")))
        self.assertTrue(os.path.isdir(os.path.join(self.tmpdir, "_wiki", "concepts")))

    def test_db_exists(self):
        self.assertTrue(os.path.isfile(os.path.join(self.tmpdir, ".sage", "wiki.db")))

    def test_manifest_exists(self):
        with open(os.path.join(self.tmpdir, ".manifest.json")) as f:
            m = json.load(f)
        self.assertEqual(len(m["sources"]), self.expected["n_sources"])

    def test_concept_files_created(self):
        files = list(Path(self.tmpdir, "_wiki", "concepts").glob("*.md"))
        self.assertEqual(len(files), self.expected["n_concepts"])

    def test_summary_files_created(self):
        files = list(Path(self.tmpdir, "_wiki", "summaries").glob("*.md"))
        self.assertEqual(len(files), self.expected["n_summaries"])

    def test_db_tables(self):
        db = sqlite3.connect(os.path.join(self.tmpdir, ".sage", "wiki.db"))
        tables = [r[0] for r in db.execute("SELECT name FROM sqlite_master WHERE type='table'").fetchall()]
        for t in ["entries", "vec_entries", "entities", "relations"]:
            self.assertIn(t, tables, f"Missing table: {t}")
        db.close()

    def test_db_entry_count(self):
        db = sqlite3.connect(os.path.join(self.tmpdir, ".sage", "wiki.db"))
        n = db.execute("SELECT COUNT(*) FROM entries_content").fetchone()[0]
        expected = self.expected["n_concepts"] + self.expected["n_summaries"]
        self.assertEqual(n, expected)
        db.close()

    def test_db_vector_count(self):
        db = sqlite3.connect(os.path.join(self.tmpdir, ".sage", "wiki.db"))
        n = db.execute("SELECT COUNT(*) FROM vec_entries").fetchone()[0]
        self.assertEqual(n, self.expected["n_vectors"])
        dims = db.execute("SELECT dimensions FROM vec_entries LIMIT 1").fetchone()[0]
        self.assertEqual(dims, self.expected["vec_dims"])
        db.close()


class TestEvalRuns(unittest.TestCase):
    """Test that eval.py runs successfully and produces valid output."""

    @classmethod
    def setUpClass(cls):
        cls.tmpdir = tempfile.mkdtemp(prefix="sage-wiki-test-")
        cls.expected = generate_fixture(cls.tmpdir, n_sources=30, n_concepts=40,
                                         n_summaries=30, n_vectors=20, vec_dims=64,
                                         n_entities=60, n_relations=80)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmpdir, ignore_errors=True)

    def _run_eval(self, *extra_args):
        result = subprocess.run(
            [sys.executable, EVAL_SCRIPT, self.tmpdir] + list(extra_args),
            capture_output=True, text=True, timeout=120,
        )
        return result

    def test_full_eval_exits_zero(self):
        r = self._run_eval()
        self.assertEqual(r.returncode, 0, f"eval.py failed:\n{r.stderr}\n{r.stdout[-500:]}")

    def test_perf_only(self):
        r = self._run_eval("--perf-only")
        self.assertEqual(r.returncode, 0)
        self.assertIn("FTS", r.stdout)
        self.assertNotIn("QUALITY SCORECARD", r.stdout)

    def test_quality_only(self):
        r = self._run_eval("--quality-only")
        self.assertEqual(r.returncode, 0)
        self.assertIn("QUALITY SCORECARD", r.stdout)
        self.assertNotIn("Sustained", r.stdout)

    def test_json_output_valid(self):
        r = self._run_eval("--json")
        self.assertEqual(r.returncode, 0, f"stderr: {r.stderr}")
        data = json.loads(r.stdout)
        self.assertIn("dataset", data)
        self.assertIn("performance", data)
        self.assertIn("quality", data)

    def test_json_dataset_correct(self):
        r = self._run_eval("--json")
        data = json.loads(r.stdout)
        ds = data["dataset"]
        self.assertEqual(ds["sources"], self.expected["n_sources"])
        self.assertEqual(ds["vectors"], self.expected["n_vectors"])
        self.assertEqual(ds["vec_dims"], self.expected["vec_dims"])
        self.assertEqual(ds["summary_files"], self.expected["n_summaries"])
        self.assertEqual(ds["concept_files"], self.expected["n_concepts"])

    def test_json_quality_scores_bounded(self):
        r = self._run_eval("--json")
        data = json.loads(r.stdout)
        scorecard = data["quality"]["scorecard"]
        for name, score in scorecard.items():
            self.assertGreaterEqual(score, 0.0, f"{name} below 0")
            self.assertLessEqual(score, 1.0, f"{name} above 1")

    def test_json_overall_score(self):
        r = self._run_eval("--json")
        data = json.loads(r.stdout)
        overall = data["quality"]["overall_score"]
        self.assertGreater(overall, 0.0)
        self.assertLessEqual(overall, 1.0)

    def test_json_perf_has_fts(self):
        r = self._run_eval("--json")
        data = json.loads(r.stdout)
        perf = data["performance"]
        self.assertIn("fts_top-1", perf)
        self.assertIn("fts_top-10", perf)
        self.assertGreater(perf["fts_top-1"]["qps"], 0)

    def test_json_perf_has_vectors(self):
        r = self._run_eval("--json")
        data = json.loads(r.stdout)
        perf = data["performance"]
        self.assertIn("vec_top10", perf)
        self.assertGreater(perf["vec_top10"]["avg"], 0)

    def test_json_perf_sustained(self):
        r = self._run_eval("--json")
        data = json.loads(r.stdout)
        perf = data["performance"]
        self.assertIn("sustained", perf)
        self.assertGreater(perf["sustained"]["throughput"], 0)
        self.assertGreater(perf["sustained"]["ops"], 0)


class TestEvalSearchRecall(unittest.TestCase):
    """Test search recall with known data."""

    @classmethod
    def setUpClass(cls):
        cls.tmpdir = tempfile.mkdtemp(prefix="sage-wiki-test-recall-")
        cls.expected = generate_fixture(cls.tmpdir, n_sources=20, n_concepts=30,
                                         n_summaries=20, n_vectors=15, vec_dims=64,
                                         n_entities=40, n_relations=50)

    @classmethod
    def tearDownClass(cls):
        shutil.rmtree(cls.tmpdir, ignore_errors=True)

    def test_recall_at_10_reasonable(self):
        r = subprocess.run(
            [sys.executable, EVAL_SCRIPT, self.tmpdir, "--json"],
            capture_output=True, text=True, timeout=120,
        )
        data = json.loads(r.stdout)
        recall = data["quality"]["search_recall"]
        # With synthetic data where concept names match content, recall should be high
        self.assertGreater(recall["recall_at_10_rate"], 0.5,
                          f"Recall@10 too low: {recall['recall_at_10_rate']}")

    def test_citation_rate_reasonable(self):
        r = subprocess.run(
            [sys.executable, EVAL_SCRIPT, self.tmpdir, "--json"],
            capture_output=True, text=True, timeout=120,
        )
        data = json.loads(r.stdout)
        cite = data["quality"]["citation"]
        # We set 90% of concepts to have sources
        self.assertGreater(cite["rate"], 0.7)

    def test_alias_rate_matches_fixture(self):
        r = subprocess.run(
            [sys.executable, EVAL_SCRIPT, self.tmpdir, "--json"],
            capture_output=True, text=True, timeout=120,
        )
        data = json.loads(r.stdout)
        alias = data["quality"]["dedup"]["alias_rate"]
        # We set alias_ratio=0.8
        self.assertGreater(alias, 0.6)


class TestEvalEdgeCases(unittest.TestCase):
    """Test eval handles edge cases gracefully."""

    def test_nonexistent_path(self):
        r = subprocess.run(
            [sys.executable, EVAL_SCRIPT, "/nonexistent/path"],
            capture_output=True, text=True, timeout=10,
        )
        self.assertNotEqual(r.returncode, 0)
        self.assertIn("not found", r.stderr)

    def test_empty_wiki(self):
        """Eval should handle a project with empty wiki directories."""
        tmpdir = tempfile.mkdtemp(prefix="sage-wiki-test-empty-")
        try:
            generate_fixture(tmpdir, n_sources=5, n_concepts=3,
                           n_summaries=3, n_vectors=2, vec_dims=32,
                           n_entities=5, n_relations=3)
            r = subprocess.run(
                [sys.executable, EVAL_SCRIPT, tmpdir, "--json"],
                capture_output=True, text=True, timeout=60,
            )
            self.assertEqual(r.returncode, 0, f"Failed on small fixture:\n{r.stderr}")
            data = json.loads(r.stdout)
            self.assertIn("dataset", data)
        finally:
            shutil.rmtree(tmpdir, ignore_errors=True)

    def test_large_fixture(self):
        """Eval should complete on a larger fixture within timeout."""
        tmpdir = tempfile.mkdtemp(prefix="sage-wiki-test-large-")
        try:
            generate_fixture(tmpdir, n_sources=200, n_concepts=300,
                           n_summaries=200, n_vectors=150, vec_dims=128,
                           n_entities=400, n_relations=600)
            r = subprocess.run(
                [sys.executable, EVAL_SCRIPT, tmpdir, "--json"],
                capture_output=True, text=True, timeout=120,
            )
            self.assertEqual(r.returncode, 0, f"Failed on large fixture:\n{r.stderr}")
            data = json.loads(r.stdout)
            self.assertEqual(data["dataset"]["concept_files"], 300)
        finally:
            shutil.rmtree(tmpdir, ignore_errors=True)


# ─── CLI: generate-fixture ─────────────────────────────────────────────────────

if __name__ == "__main__":
    if "--generate-fixture" in sys.argv:
        idx = sys.argv.index("--generate-fixture")
        path = sys.argv[idx + 1] if idx + 1 < len(sys.argv) else "./test-fixture"
        print(f"Generating fixture at {path}...")
        expected = generate_fixture(path, n_sources=50, n_concepts=80,
                                     n_summaries=50, n_vectors=40, vec_dims=128,
                                     n_entities=100, n_relations=120)
        print(f"Done. {expected['n_concepts']} concepts, {expected['n_summaries']} summaries, "
              f"{expected['n_entities']} entities, {expected['n_relations']} relations.")
        print(f"\nRun eval: python3 eval.py {path}")
        sys.exit(0)

    unittest.main()