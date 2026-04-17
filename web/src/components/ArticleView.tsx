import { useState, useEffect, useRef } from 'preact/hooks';
import { fetchArticle, fetchTree, type Article } from '../lib/api';
import { renderMarkdown } from '../lib/markdown';
import { GraphView } from './GraphView';
import { QAPanel } from './QAPanel';

interface Props {
  path: string;
  onNavigate: (path: string) => void;
  reloadKey?: number;
}

export function ArticleView({ path, onNavigate, reloadKey }: Props) {
  const [article, setArticle] = useState<Article | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [headings, setHeadings] = useState<{ id: string; text: string; level: number }[]>([]);
  const [activeId, setActiveId] = useState<string | null>(null);
  const [rightPanel, setRightPanel] = useState<'toc' | 'graph'>('toc');
  const [knownPaths, setKnownPaths] = useState<Set<string>>(new Set());
  const scrollRef = useRef<HTMLDivElement>(null);

  // Extract concept ID from path for graph centering
  const conceptId = path.replace(/^concepts\//, '').replace(/\.md$/, '');

  // Load known article paths for broken link detection
  useEffect(() => {
    fetchTree().then(tree => {
      const paths = new Set<string>();
      for (const section of ['concepts', 'summaries', 'outputs'] as const) {
        const files = (tree as any)[section] as any[] || [];
        for (const f of files) {
          // Store both with and without .md for matching
          paths.add(f.path);
          paths.add(f.path.replace('.md', ''));
          paths.add(f.name);
        }
      }
      setKnownPaths(paths);
    });
  }, []);

  useEffect(() => {
    setError(null);
    setArticle(null);
    fetchArticle(path)
      .then(a => {
        setArticle(a);
        const h = [...a.body.matchAll(/^(#{2,3})\s+(.+)$/gm)].map(m => ({
          id: m[2].toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/-$/, ''),
          text: m[2],
          level: m[1].length,
        }));
        setHeadings(h);
        const title = a.frontmatter?.concept || path.split('/').pop()?.replace('.md', '') || 'Article';
        document.title = `${title} — sage-wiki`;
        // Scroll to top on article change
        scrollRef.current?.scrollTo(0, 0);
      })
      .catch(e => setError(e.message));
  }, [path, reloadKey]);

  // Scroll-spy: observe headings within the scroll container
  useEffect(() => {
    if (!scrollRef.current || headings.length === 0) return;

    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          if (entry.isIntersecting) {
            setActiveId(entry.target.id);
          }
        }
      },
      { root: scrollRef.current, rootMargin: '-10% 0px -80% 0px', threshold: 0 }
    );

    const els = scrollRef.current.querySelectorAll('h2[id], h3[id]');
    els.forEach(el => observer.observe(el));

    return () => observer.disconnect();
  }, [article, headings]);

  // Mark broken wikilinks after render
  useEffect(() => {
    if (!scrollRef.current || !article || knownPaths.size === 0) return;

    const links = scrollRef.current.querySelectorAll('a.wikilink');
    links.forEach(link => {
      const href = link.getAttribute('href');
      if (!href || !href.startsWith('/wiki/')) return;

      // Extract the target path: /wiki/concepts/foo → concepts/foo
      const targetPath = href.replace('/wiki/', '');
      const targetWithMd = targetPath + '.md';
      const targetName = targetPath.split('/').pop() || '';

      if (!knownPaths.has(targetPath) && !knownPaths.has(targetWithMd) && !knownPaths.has(targetName)) {
        link.classList.add('wikilink-broken');
        link.classList.remove('wikilink');
        link.setAttribute('title', 'Article not found');
      }
    });
  }, [article, knownPaths]);

  // Handle clicks: wikilinks + TOC anchor scrolling
  const handleClick = (e: MouseEvent) => {
    const target = e.target as HTMLElement;
    const link = target.closest('a');
    if (!link) return;

    const href = link.getAttribute('href');
    if (!href) return;

    if (href.startsWith('/wiki/')) {
      e.preventDefault();
      const articlePath = href.replace('/wiki/', '').replace(/\.md$/, '') + '.md';
      onNavigate(articlePath);
    } else if (href.startsWith('#')) {
      e.preventDefault();
      const id = href.slice(1);
      const el = document.getElementById(id);
      if (el) {
        el.scrollIntoView({ behavior: 'smooth', block: 'start' });
      }
    }
  };

  if (error) {
    return (
      <div class="flex-1 flex items-center justify-center">
        <div class="text-center">
          <h2 class="text-xl text-gray-500 mb-2">Article not found</h2>
          <p class="text-gray-400 text-sm">{path}</p>
        </div>
      </div>
    );
  }

  if (!article) {
    return (
      <div class="flex-1 flex items-center justify-center">
        <div class="animate-pulse text-gray-400">Loading...</div>
      </div>
    );
  }

  const html = renderMarkdown(article.body);

  return (
    <div class="flex flex-1 flex-col min-h-0">
      {/* Scrollable area: article + sticky TOC side by side */}
      <div ref={scrollRef} class="flex-1 overflow-y-auto" onClick={handleClick}>
        <div class="flex">
          {/* Article content */}
          <article class="flex-1 min-w-0 px-8 py-6 max-w-4xl">
            {/* Breadcrumb */}
            <nav class="mb-3 text-xs text-gray-400 dark:text-gray-500">
              {path.split('/').map((segment, i, arr) => {
                const isLast = i === arr.length - 1;
                const display = isLast ? segment.replace('.md', '').replace(/-/g, ' ') : segment;
                return (
                  <span key={i}>
                    {i > 0 && <span class="mx-1">/</span>}
                    {isLast
                      ? <span class="text-gray-600 dark:text-gray-300">{display}</span>
                      : <span>{display}</span>
                    }
                  </span>
                );
              })}
            </nav>

            {/* Frontmatter bar */}
            {article.frontmatter && Object.keys(article.frontmatter).length > 0 && (
              <div class="mb-4 flex flex-wrap gap-2">
                {article.frontmatter.confidence && (
                  <span class={`px-2 py-0.5 rounded text-xs font-medium ${
                    article.frontmatter.confidence === 'high' ? 'bg-green-100 text-green-700 dark:bg-green-900 dark:text-green-300' :
                    article.frontmatter.confidence === 'medium' ? 'bg-yellow-100 text-yellow-700 dark:bg-yellow-900 dark:text-yellow-300' :
                    'bg-red-100 text-red-700 dark:bg-red-900 dark:text-red-300'
                  }`}>
                    {article.frontmatter.confidence}
                  </span>
                )}
                {article.frontmatter.concept && (
                  <span class="px-2 py-0.5 rounded text-xs bg-blue-100 text-blue-700 dark:bg-blue-900 dark:text-blue-300">
                    {article.frontmatter.concept}
                  </span>
                )}
                {article.frontmatter.source && (
                  <span class="px-2 py-0.5 rounded text-xs bg-gray-100 text-gray-600 dark:bg-gray-800 dark:text-gray-400">
                    source: {article.frontmatter.source}
                  </span>
                )}
              </div>
            )}

            <div
              class="prose dark:prose-invert max-w-none prose-headings:scroll-mt-4"
              dangerouslySetInnerHTML={{ __html: html }}
            />
          </article>

          {/* Right panel: sticky TOC or Graph */}
          <div class="hidden lg:block w-56 shrink-0">
            <div class="sticky top-0 border-l border-gray-200 dark:border-gray-700">
              {/* Panel toggle */}
              <div class="flex border-b border-gray-200 dark:border-gray-700">
                <button
                  onClick={() => setRightPanel('toc')}
                  class={`flex-1 px-3 py-2 text-xs font-medium ${
                    rightPanel === 'toc'
                      ? 'text-blue-600 border-b-2 border-blue-600'
                      : 'text-gray-500 hover:text-gray-700'
                  }`}
                >
                  Contents
                </button>
                <button
                  onClick={() => setRightPanel('graph')}
                  class={`flex-1 px-3 py-2 text-xs font-medium ${
                    rightPanel === 'graph'
                      ? 'text-blue-600 border-b-2 border-blue-600'
                      : 'text-gray-500 hover:text-gray-700'
                  }`}
                >
                  Graph
                </button>
              </div>

              {/* Panel content */}
              {rightPanel === 'toc' ? (
                headings.length > 0 ? (
                  <nav class="px-4 py-4 max-h-[calc(100vh-8rem)] overflow-y-auto">
                    {headings.map(h => (
                      <a
                        key={h.id}
                        href={`#${h.id}`}
                        class={`block py-1 text-sm transition-colors ${
                          h.level === 3 ? 'pl-4' : ''
                        } ${
                          activeId === h.id
                            ? 'text-blue-600 dark:text-blue-400 font-medium'
                            : 'text-gray-600 dark:text-gray-400 hover:text-blue-600'
                        }`}
                      >
                        {h.text}
                      </a>
                    ))}
                  </nav>
                ) : (
                  <div class="px-4 py-4 text-sm text-gray-400">No headings</div>
                )
              ) : (
                <div class="h-[calc(100vh-8rem)]">
                  <GraphView currentArticle={conceptId} onNavigate={onNavigate} />
                </div>
              )}
            </div>
          </div>
        </div>
      </div>

      {/* Q&A Panel pinned at bottom */}
      <QAPanel onNavigate={onNavigate} />
    </div>
  );
}
