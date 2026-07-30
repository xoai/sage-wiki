"""Response models for the sage-wiki /v1 API.

Wire facts these encode (verified against a live server):
- search result items carry PascalCase keys (untagged DocResult) —
  canonicalized here to snake_case, with snake_case tolerated;
- zero-hit search on the default pipeline is ``{"results": []}`` with
  ``uncompiled_sources`` omitted; the legacy pipeline emits ``results: null``;
- unknown keys are ignored (forward compatibility).
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Dict, List, Optional


def _get(d: Dict[str, Any], *keys: str, default: Any = None) -> Any:
    for k in keys:
        if k in d and d[k] is not None:
            return d[k]
    return default


@dataclass
class SearchResultItem:
    id: str = ""
    content: str = ""
    article_path: Optional[str] = None
    bm25_rank: int = 0
    vector_rank: int = 0
    rrf_score: float = 0.0
    final_score: float = 0.0
    source_date: Optional[int] = None
    tags: List[str] = field(default_factory=list)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "SearchResultItem":
        return cls(
            id=str(_get(d, "id", "ID", default="")),
            content=str(_get(d, "content", "Content", default="")),
            article_path=_get(d, "article_path", "ArticlePath") or None,
            bm25_rank=int(_get(d, "bm25_rank", "BM25Rank", default=0)),
            vector_rank=int(_get(d, "vector_rank", "VectorRank", default=0)),
            rrf_score=float(_get(d, "rrf_score", "RRFScore", default=0.0)),
            final_score=float(_get(d, "final_score", "FinalScore", default=0.0)),
            source_date=_get(d, "source_date", "SourceDate"),
            tags=list(_get(d, "tags", "Tags", default=[])),
        )


@dataclass
class SearchResults:
    results: List[SearchResultItem] = field(default_factory=list)
    uncompiled_sources: int = 0
    compile_hint: Optional[str] = None

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "SearchResults":
        raw = d.get("results") or []
        return cls(
            results=[SearchResultItem.from_dict(x) for x in raw],
            uncompiled_sources=int(d.get("uncompiled_sources") or 0),
            compile_hint=d.get("compile_hint"),
        )


@dataclass
class Article:
    path: str
    content: str

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Article":
        return cls(path=str(d.get("path", "")), content=str(d.get("content", "")))


@dataclass
class Status:
    project: str = ""
    mode: str = ""
    source_count: int = 0
    raw: Dict[str, Any] = field(default_factory=dict)

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Status":
        return cls(
            project=str(d.get("project", "")),
            mode=str(d.get("mode", "")),
            source_count=int(d.get("source_count") or 0),
            raw=dict(d),
        )


@dataclass
class Entity:
    id: str = ""
    type: str = ""
    name: str = ""
    article_path: Optional[str] = None

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "Entity":
        return cls(
            id=str(d.get("id", "")),
            type=str(d.get("type", "")),
            name=str(d.get("name", "")),
            article_path=d.get("article_path") or None,
        )


@dataclass
class TraverseResult:
    """Ontology traversal. The wire is a bare JSON array of entities when
    relations exist, or {"result": "null"} (a string) when none do — both
    shapes are handled."""

    entities: List[Entity] = field(default_factory=list)
    raw: Any = None

    @classmethod
    def from_dict(cls, d: Any) -> "TraverseResult":
        if isinstance(d, list):
            return cls(entities=[Entity.from_dict(e) for e in d if isinstance(e, dict)], raw=d)
        if isinstance(d, dict):
            # The no-relations shape: {"result": "null"} (a JSON string).
            return cls(entities=[], raw=d.get("result"))
        return cls(entities=[], raw=d)


@dataclass
class GraphQueryResult:
    answer: str = ""
    cited: List[Any] = field(default_factory=list)
    seeds: List[str] = field(default_factory=list)
    truncated: bool = False

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "GraphQueryResult":
        return cls(
            answer=str(d.get("answer", "")),
            cited=list(d.get("cited") or []),
            seeds=list(d.get("seeds") or []),
            truncated=bool(d.get("truncated", False)),
        )


@dataclass
class ProvenanceResult:
    """Provenance for an article (sources list) or a source (articles list).

    Wire: article queries return {"article", "sources", "total"}; source
    queries return {"source", "articles", "total"}."""

    article: Optional[str] = None
    source: Optional[str] = None
    sources: Any = None
    articles: List[Dict[str, Any]] = field(default_factory=list)
    total: int = 0

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "ProvenanceResult":
        return cls(
            article=d.get("article"),
            source=d.get("source"),
            sources=d.get("sources"),
            articles=list(d.get("articles") or []),
            total=int(d.get("total") or 0),
        )


@dataclass
class TextResult:
    """For endpoints returning {"result": "<text>"} (writes, compile diff)."""

    result: str

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "TextResult":
        return cls(result=str(d.get("result", "")))


@dataclass
class CompileDiff:
    diff: str

    @classmethod
    def from_dict(cls, d: Dict[str, Any]) -> "CompileDiff":
        return cls(diff=str(d.get("diff", "")))
