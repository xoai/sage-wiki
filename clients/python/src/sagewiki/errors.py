"""Error taxonomy for the sage-wiki /v1 API.

Clients branch on ``code``, never on ``message`` — the code vocabulary is
fixed by the API contract (api/openapi.yaml).
"""

from __future__ import annotations

from typing import Any, Dict, Optional, Type


class SageWikiError(Exception):
    """Base error for all sage-wiki client failures.

    Carries the wire fields: ``code`` (stable vocabulary), ``message``
    (human, unstable), ``details`` (structured extras, may be None).
    """

    def __init__(self, code: str, message: str, details: Optional[Dict[str, Any]] = None):
        super().__init__(f"{code}: {message}")
        self.code = code
        self.message = message
        self.details = details


class InvalidArgument(SageWikiError):
    """400 — missing, malformed, or out-of-range argument."""


class Unauthenticated(SageWikiError):
    """401 — missing or invalid Bearer token."""


class Forbidden(SageWikiError):
    """403 — host not allowed, or path containment violation."""


class NotFound(SageWikiError):
    """404 — article, entity, or job does not exist."""


class Conflict(SageWikiError):
    """409 — concurrent-write conflict (e.g. a compile is already running)."""

    @property
    def active_job_id(self) -> Optional[str]:
        """The conflicting job's ID, when the server supplied one (compile 409)."""
        if isinstance(self.details, dict):
            v = self.details.get("active_job_id")
            return v if isinstance(v, str) else None
        return None


class FeatureDisabled(SageWikiError):
    """412 — a gated feature is off.

    The two real causes: ``as_of`` queries need ``ontology.temporal.enabled``,
    and ``mode=global`` needs ``ontology.communities.enabled``. Enable the
    flag in config.yaml and retry.
    """


class PayloadTooLarge(SageWikiError):
    """413 — capture content over 100 KB."""


class RateLimited(SageWikiError):
    """429 — reserved; not currently emitted by the server."""


class InternalError(SageWikiError):
    """500 — unclassified server failure."""


class Unavailable(SageWikiError):
    """503 — backend or store unavailable."""


class JobTimeout(SageWikiError):
    """Job.wait() expired before the job reached a terminal state."""


class JobFailed(SageWikiError):
    """Job reached status ``failed``; carries the job's error envelope."""


_CODE_CLASSES: Dict[str, Type[SageWikiError]] = {
    "invalid_argument": InvalidArgument,
    "unauthenticated": Unauthenticated,
    "forbidden": Forbidden,
    "not_found": NotFound,
    "conflict": Conflict,
    "feature_disabled": FeatureDisabled,
    "payload_too_large": PayloadTooLarge,
    "rate_limited": RateLimited,
    "internal": InternalError,
    "unavailable": Unavailable,
}


def raise_for_envelope(status: int, body: Any) -> None:
    """Raise the mapped exception for a non-2xx response.

    Unknown codes map to the base class (forward-compatible with new codes).
    Bodies that aren't the error envelope (proxy HTML pages, etc.) map to a
    base error with code ``http_<status>`` — never a parse crash.
    """
    if isinstance(body, dict) and isinstance(body.get("error"), dict):
        env = body["error"]
        code = str(env.get("code", "internal"))
        message = str(env.get("message", ""))
        details = env.get("details")
        cls = _CODE_CLASSES.get(code, SageWikiError)
        raise cls(code, message, details)
    raise SageWikiError(f"http_{status}", f"HTTP {status} with non-JSON error body")
