"""LangGraph + sage-wiki: memory-backed agent nodes.

Demonstrates the two sage-wiki-specific patterns an agent needs:
  1. retrieval with the compile-on-demand signal — when
     `uncompiled_sources > 0`, matching sources exist that aren't compiled
     yet, so the graph submits a topic compile and waits (explicit timeout);
  2. capture — the agent writes knowledge back, closing the loop.

The LLM step is STUBBED (deterministic echo) so this runs with no API key.
Run against a live server:

    eval "$(../../scripts/p4-fixture-server.sh)"   # or your own serve --ui
    python main.py
"""

from __future__ import annotations

import os
import sys
from typing import Any, Dict, List, TypedDict

from langgraph.graph import END, START, StateGraph

from sagewiki import SageWiki
from sagewiki.errors import JobFailed


class AgentState(TypedDict, total=False):
    question: str
    retrieved: List[str]
    compiled_topic: str
    captured: bool
    answer: str


def make_nodes(client: SageWiki):
    def retrieve(state: AgentState) -> Dict[str, Any]:
        question = state["question"]
        results = client.search(question, limit=5)
        titles = [item.content[:60] for item in results.results]

        out: Dict[str, Any] = {"retrieved": titles}

        # The compile-on-demand signal: > 0 means matching sources are not
        # yet compiled into the wiki — submit a topic compile and wait.
        if results.uncompiled_sources > 0:
            print(f"[example] uncompiled_sources={results.uncompiled_sources} > 0 — "
                  f"submitting topic compile for {question!r}")
            job = client.compile(topic=question)
            try:
                done = job.wait(timeout=120, poll_interval=1)
                print(f"[example] topic compile finished: {done.status}")
            except JobFailed as e:
                # The fixture has no LLM key; a failed topic compile is
                # tolerated — the marker above is what EX-03 asserts.
                print(f"[example] topic compile failed (tolerated: no LLM key): {e.message}")
            out["compiled_topic"] = question
            # Re-retrieve after compilation.
            results = client.search(question, limit=5)
            out["retrieved"] = [item.content[:60] for item in results.results]

        # A graph query complements ranked retrieval with the ontology view.
        gq = client.graph_query(question, hops=1)
        if gq.answer:
            out.setdefault("graph_answer", gq.answer)
        return out

    def capture_node(state: AgentState) -> Dict[str, Any]:
        client.capture(
            f"Agent answered a question about {state['question']}",
            context="langgraph example",
            idempotency_key=f"langgraph-example-{state['question']}",
        )
        return {"captured": True}

    def stub_llm(state: AgentState) -> Dict[str, Any]:
        # STUB: a real graph calls an LLM here. Deterministic echo keeps the
        # example keyless and CI-safe.
        retrieved = state.get("retrieved", [])
        return {"answer": f"[stub] {state['question']}: {len(retrieved)} retrieved snippets"}

    return retrieve, capture_node, stub_llm


def main() -> int:
    question = sys.argv[1] if len(sys.argv) > 1 else "attention"
    client = SageWiki()  # SAGE_WIKI_URL / SAGE_WIKI_TOKEN from env

    retrieve, capture_node, stub_llm = make_nodes(client)
    graph = StateGraph(AgentState)
    graph.add_node("retrieve", retrieve)
    graph.add_node("capture", capture_node)
    graph.add_node("generate", stub_llm)
    graph.add_edge(START, "retrieve")
    graph.add_edge("retrieve", "capture")
    graph.add_edge("capture", "generate")
    graph.add_edge("generate", END)
    app = graph.compile()

    final = app.invoke({"question": question})
    retrieved = final.get("retrieved", [])
    print(f"[example] retrieved {len(retrieved)} snippets:")
    for r in retrieved:
        print(f"  - {r}")
    print(f"[example] answer: {final['answer']}")
    print(f"[example] captured: {final.get('captured')}")

    if not retrieved:
        print("[example] FAIL: no results retrieved")
        return 1
    print("[example] PASS: retrieved >= 1 result")
    return 0


if __name__ == "__main__":
    if not os.environ.get("SAGE_WIKI_URL"):
        print("[example] SAGE_WIKI_URL unset — start a server first")
        sys.exit(2)
    sys.exit(main())
