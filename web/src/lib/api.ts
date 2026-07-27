import { authHeaders } from './token';

const BASE = '';

export interface FileEntry {
  name: string;
  path: string;
}

export interface TreeData {
  concepts: FileEntry[];
  summaries: FileEntry[];
  outputs: FileEntry[];
  stats: { concepts: number; summaries: number };
}

export interface Article {
  path: string;
  frontmatter: Record<string, string>;
  body: string;
}

export interface SearchHit {
  id: string;
  path: string;
  snippet: string;
  score: number;
}

export interface GraphData {
  nodes: { id: string; type: string; name: string; connections: number }[];
  edges: { source: string; target: string; relation: string }[];
  total: number;
  // Present on neighborhood queries: the entity the graph is actually
  // centered on. May differ from the requested ?center= when the server
  // resolved an alias to its canonical entity.
  center?: string;
}

export interface StatusData {
  project: string;
  entries: number;
  vectors: number;
  dimensions: number;
  entities: number;
  relations: number;
}

export async function fetchTree(): Promise<TreeData> {
  const res = await fetch(`${BASE}/api/tree`, { headers: authHeaders() });
  return res.json();
}

export async function fetchArticle(path: string): Promise<Article> {
  const res = await fetch(`${BASE}/api/articles/${path}`, { headers: authHeaders() });
  if (!res.ok) throw new Error(`Article not found: ${path}`);
  return res.json();
}

export async function fetchSearch(query: string, limit = 10): Promise<{ results: SearchHit[]; total: number }> {
  const res = await fetch(`${BASE}/api/search?q=${encodeURIComponent(query)}&limit=${limit}`, { headers: authHeaders() });
  return res.json();
}

export async function fetchGraph(center?: string, depth = 2): Promise<GraphData> {
  let url = `${BASE}/api/graph`;
  if (center) url += `?center=${encodeURIComponent(center)}&depth=${depth}`;
  const res = await fetch(url, { headers: authHeaders() });
  return res.json();
}

export async function fetchStatus(): Promise<StatusData> {
  const res = await fetch(`${BASE}/api/status`, { headers: authHeaders() });
  return res.json();
}
