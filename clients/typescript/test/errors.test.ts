import { test } from "node:test";
import assert from "node:assert/strict";
import {
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
  raiseForEnvelope,
} from "../src/errors.js";

const CASES: Array<[number, string, new (...a: never[]) => SageWikiError]> = [
  [400, "invalid_argument", InvalidArgument as never],
  [401, "unauthenticated", Unauthenticated as never],
  [403, "forbidden", Forbidden as never],
  [404, "not_found", NotFound as never],
  [409, "conflict", Conflict as never],
  [412, "feature_disabled", FeatureDisabled as never],
  [413, "payload_too_large", PayloadTooLarge as never],
  [429, "rate_limited", RateLimited as never],
  [500, "internal", InternalError as never],
  [503, "unavailable", Unavailable as never],
];

test("each documented code maps to its subclass", () => {
  for (const [status, code, cls] of CASES) {
    const body = { error: { code, message: "boom", details: { field: "x" } } };
    assert.throws(() => raiseForEnvelope(status, body), (e: unknown) => {
      assert.ok(e instanceof cls, `${code} should map to ${cls.name}`);
      const err = e as SageWikiError;
      assert.equal(err.code, code);
      assert.equal(err.message, "boom");
      assert.deepEqual(err.details, { field: "x" });
      return true;
    });
  }
});

test("instanceof and switch (e.code) both work", () => {
  try {
    raiseForEnvelope(412, { error: { code: "feature_disabled", message: "off" } });
    assert.fail("should have thrown");
  } catch (e) {
    assert.ok(e instanceof FeatureDisabled);
    switch ((e as SageWikiError).code) {
      case "feature_disabled":
        return;
      default:
        assert.fail("wrong code");
    }
  }
});

test("unknown code maps to base with raw code", () => {
  assert.throws(
    () => raiseForEnvelope(500, { error: { code: "future_code", message: "new" } }),
    (e: unknown) => {
      assert.ok(e instanceof SageWikiError);
      assert.equal((e as SageWikiError).constructor, SageWikiError);
      assert.equal((e as SageWikiError).code, "future_code");
      return true;
    },
  );
});

test("non-envelope body maps to http_<status>", () => {
  assert.throws(
    () => raiseForEnvelope(502, "<html>Bad Gateway</html>"),
    (e: unknown) => {
      assert.equal((e as SageWikiError).code, "http_502");
      return true;
    },
  );
});

test("Conflict exposes details.activeJobId", () => {
  const body = {
    error: {
      code: "conflict",
      message: "A compile is already in progress",
      details: { active_job_id: "abc-123" },
    },
  };
  assert.throws(
    () => raiseForEnvelope(409, body),
    (e: unknown) => {
      assert.ok(e instanceof Conflict);
      assert.equal((e as Conflict).activeJobId, "abc-123");
      return true;
    },
  );
});

test("FeatureDisabled hint names both real causes", () => {
  assert.match(FeatureDisabled.hint, /ontology\.temporal\.enabled/);
  assert.match(FeatureDisabled.hint, /ontology\.communities\.enabled/);
});

test("never branches on message", () => {
  const body = (code: string) => ({ error: { code, message: "same text" } });
  assert.throws(() => raiseForEnvelope(404, body("not_found")), NotFound);
  assert.throws(() => raiseForEnvelope(400, body("invalid_argument")), InvalidArgument);
});
