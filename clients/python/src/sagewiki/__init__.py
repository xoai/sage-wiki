"""Typed client for sage-wiki's /v1 REST API.

Pre-1.0: the surface can change — pin a version.
"""

from sagewiki.errors import (
    Conflict,
    FeatureDisabled,
    Forbidden,
    InternalError,
    InvalidArgument,
    JobFailed,
    JobTimeout,
    NotFound,
    PayloadTooLarge,
    RateLimited,
    SageWikiError,
    Unauthenticated,
    Unavailable,
)
from sagewiki.models import (
    Article,
    CompileDiff,
    Entity,
    GraphQueryResult,
    ProvenanceResult,
    SearchResultItem,
    SearchResults,
    Status,
    TextResult,
    TraverseResult,
)

from sagewiki.client import AsyncSageWiki, SageWiki
from sagewiki.jobs import AsyncJob, Job

__version__ = "0.1.0"

__all__ = [
    "__version__",
    "SageWiki",
    "AsyncSageWiki",
    "Job",
    "AsyncJob",
    "SageWikiError",
    "InvalidArgument",
    "Unauthenticated",
    "Forbidden",
    "NotFound",
    "Conflict",
    "FeatureDisabled",
    "PayloadTooLarge",
    "RateLimited",
    "InternalError",
    "Unavailable",
    "JobTimeout",
    "JobFailed",
    "Article",
    "CompileDiff",
    "Entity",
    "GraphQueryResult",
    "ProvenanceResult",
    "SearchResultItem",
    "SearchResults",
    "Status",
    "TextResult",
    "TraverseResult",
]
