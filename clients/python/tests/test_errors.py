import pytest

from sagewiki.errors import (
    Conflict,
    FeatureDisabled,
    Forbidden,
    InternalError,
    InvalidArgument,
    NotFound,
    PayloadTooLarge,
    RateLimited,
    SageWikiError,
    Unauthenticated,
    Unavailable,
    raise_for_envelope,
)


@pytest.mark.parametrize(
    "status,code,cls",
    [
        (400, "invalid_argument", InvalidArgument),
        (401, "unauthenticated", Unauthenticated),
        (403, "forbidden", Forbidden),
        (404, "not_found", NotFound),
        (409, "conflict", Conflict),
        (412, "feature_disabled", FeatureDisabled),
        (413, "payload_too_large", PayloadTooLarge),
        (429, "rate_limited", RateLimited),
        (500, "internal", InternalError),
        (503, "unavailable", Unavailable),
    ],
)
def test_each_code_maps_to_its_class(status, code, cls):
    body = {"error": {"code": code, "message": "boom", "details": {"field": "x"}}}
    with pytest.raises(cls) as exc_info:
        raise_for_envelope(status, body)
    err = exc_info.value
    assert err.code == code
    assert err.message == "boom"
    assert err.details == {"field": "x"}


def test_unknown_code_maps_to_base_with_raw_code():
    body = {"error": {"code": "future_code", "message": "new"}}
    with pytest.raises(SageWikiError) as exc_info:
        raise_for_envelope(500, body)
    assert type(exc_info.value) is SageWikiError
    assert exc_info.value.code == "future_code"


def test_non_envelope_body_gives_http_status_code():
    with pytest.raises(SageWikiError) as exc_info:
        raise_for_envelope(502, "<html>Bad Gateway</html>")
    assert exc_info.value.code == "http_502"


def test_conflict_exposes_active_job_id():
    body = {
        "error": {
            "code": "conflict",
            "message": "A compile is already in progress",
            "details": {"active_job_id": "abc-123"},
        }
    }
    with pytest.raises(Conflict) as exc_info:
        raise_for_envelope(409, body)
    assert exc_info.value.active_job_id == "abc-123"


def test_feature_disabled_docstring_names_both_causes():
    doc = FeatureDisabled.__doc__
    assert "ontology.temporal.enabled" in doc
    assert "ontology.communities.enabled" in doc


def test_never_branches_on_message():
    # Same message, different codes → different classes.
    body = lambda code: {"error": {"code": code, "message": "same text"}}
    with pytest.raises(NotFound):
        raise_for_envelope(404, body("not_found"))
    with pytest.raises(InvalidArgument):
        raise_for_envelope(400, body("invalid_argument"))
