from sagewiki.models import Article, Entity, ProvenanceResult, SearchResults, Status, TraverseResult


def test_zero_hit_search_real_wire_shape():
    # Real server response for a miss on the default unified pipeline.
    results = SearchResults.from_dict({"results": []})
    assert results.results == []
    assert results.uncompiled_sources == 0


def test_search_results_null_tolerated():
    # The opt-in legacy pipeline emits a nil slice.
    results = SearchResults.from_dict({"results": None})
    assert results.results == []


def test_search_result_items_pascalcase_canonicalized():
    # Untagged DocResult — the actual pre-1.0 wire shape.
    payload = {
        "results": [
            {
                "ID": "attention.md",
                "Content": "Self-attention…",
                "ArticlePath": "wiki/summaries/attention.md",
                "BM25Rank": 1,
                "VectorRank": 0,
                "RRFScore": 0.011,
                "FinalScore": 1.0,
                "SourceDate": 1785449267,
                "Tags": ["article"],
            }
        ],
        "uncompiled_sources": 2,
    }
    results = SearchResults.from_dict(payload)
    assert results.uncompiled_sources == 2
    item = results.results[0]
    assert item.id == "attention.md"
    assert item.content == "Self-attention…"
    assert item.article_path == "wiki/summaries/attention.md"
    assert item.bm25_rank == 1
    assert item.final_score == 1.0
    assert item.tags == ["article"]


def test_search_result_items_snake_case_tolerated():
    payload = {"results": [{"id": "x", "content": "c", "article_path": "p"}]}
    item = SearchResults.from_dict(payload).results[0]
    assert item.id == "x"
    assert item.article_path == "p"


def test_unknown_keys_ignored():
    results = SearchResults.from_dict({"results": [], "future_field": 1})
    assert results.results == []


def test_article_from_dict():
    a = Article.from_dict({"path": "concepts/x.md", "content": "# X"})
    assert a.path == "concepts/x.md"
    assert a.content == "# X"


def test_entity_from_dict():
    e = Entity.from_dict(
        {"id": "attention", "type": "concept", "name": "Attention", "article_path": "wiki/concepts/attention.md"}
    )
    assert e.id == "attention"
    assert e.type == "concept"


def test_status_from_dict_tolerates_sparse():
    s = Status.from_dict({"project": "wiki", "source_count": 0})
    assert s.project == "wiki"


def test_traverse_bare_array_wire_shape():
    # With relations the server emits a bare JSON array (not an object).
    payload = [{"id": "transformer", "type": "concept", "name": "Transformer"}]
    r = TraverseResult.from_dict(payload)
    assert len(r.entities) == 1
    assert r.entities[0].id == "transformer"


def test_traverse_null_result_wire_shape():
    # Without relations the server emits {"result": "null"} (a string).
    r = TraverseResult.from_dict({"result": "null"})
    assert r.entities == []


def test_provenance_source_direction_articles_key():
    # source= queries return {"source", "articles", "total"} — not "sources".
    r = ProvenanceResult.from_dict(
        {"source": "paper.pdf", "articles": [{"concept": "attention", "article_path": "wiki/concepts/attention.md"}], "total": 1}
    )
    assert r.source == "paper.pdf"
    assert r.total == 1
    assert r.articles[0]["concept"] == "attention"
