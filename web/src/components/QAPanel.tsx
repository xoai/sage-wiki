import { useState, useRef, useEffect } from 'preact/hooks';
import { renderMarkdown } from '../lib/markdown';
import { streamQuery } from '../lib/sse';

interface Props {
  onNavigate: (path: string) => void;
}

// Keep answer state outside component so it survives remounts (hot reload)
let persistedAnswer = '';
let persistedSources: string[] = [];

export function QAPanel({ onNavigate }: Props) {
  const [question, setQuestion] = useState('');
  const [answer, setAnswer] = useState(persistedAnswer);
  const [sources, setSources] = useState<string[]>(persistedSources);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const controllerRef = useRef<AbortController | null>(null);
  const answerRef = useRef<HTMLDivElement>(null);

  // Abort streaming on unmount
  useEffect(() => {
    return () => { controllerRef.current?.abort(); };
  }, []);

  // Auto-scroll answer area as tokens stream in
  useEffect(() => {
    if (answerRef.current && loading) {
      answerRef.current.scrollTop = answerRef.current.scrollHeight;
    }
  }, [answer, loading]);

  // Persist answer across remounts
  useEffect(() => {
    persistedAnswer = answer;
  }, [answer]);
  useEffect(() => {
    persistedSources = sources;
  }, [sources]);

  const handleSubmit = (e: Event) => {
    e.preventDefault();
    if (!question.trim() || loading) return;

    // Cancel previous request
    controllerRef.current?.abort();

    setAnswer('');
    setSources([]);
    setError(null);
    setLoading(true);
    persistedAnswer = '';
    persistedSources = [];

    controllerRef.current = streamQuery(question, 5, {
      onToken: (text) => {
        setAnswer(prev => prev + text);
      },
      onSources: (paths) => {
        setSources(paths);
      },
      onDone: () => {
        setLoading(false);
      },
      onError: (err) => {
        setError(err);
        setLoading(false);
      },
    });
  };

  const handleSourceClick = (path: string) => {
    const articlePath = path.replace(/^wiki\//, '').replace(/^_wiki\//, '');
    onNavigate(articlePath);
  };

  const handleLinkClick = (e: MouseEvent) => {
    const target = e.target as HTMLElement;
    const link = target.closest('a');
    if (link?.getAttribute('href')?.startsWith('/wiki/')) {
      e.preventDefault();
      const path = link.getAttribute('href')!.replace('/wiki/', '').replace(/\.md$/, '') + '.md';
      onNavigate(path);
    }
  };

  return (
    <div class="border-t border-gray-200 dark:border-gray-700 flex flex-col max-h-[40vh]">
      {/* Answer area */}
      {(answer || loading || error) && (
        <div ref={answerRef} class="flex-1 overflow-y-auto px-6 py-4 min-h-0 relative">
          <button
            onClick={() => {
              controllerRef.current?.abort();
              setAnswer('');
              setSources([]);
              setError(null);
              setLoading(false);
              persistedAnswer = '';
              persistedSources = [];
            }}
            class="absolute top-2 right-2 p-1 rounded hover:bg-gray-200 dark:hover:bg-gray-700 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
            title="Close"
          >
            <svg xmlns="http://www.w3.org/2000/svg" width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M18 6L6 18"/><path d="M6 6l12 12"/></svg>
          </button>
          {error && (
            <div class="text-red-500 text-sm mb-2">Error: {error}</div>
          )}
          {answer && (
            <div
              class="prose dark:prose-invert prose-sm max-w-none"
              onClick={handleLinkClick}
              dangerouslySetInnerHTML={{ __html: renderMarkdown(answer) }}
            />
          )}
          {loading && !answer && (
            <div class="flex items-center gap-2 text-gray-400 text-sm">
              <span class="animate-pulse">Thinking...</span>
            </div>
          )}
          {loading && answer && (
            <span class="inline-block w-2 h-4 bg-blue-500 animate-pulse ml-0.5" />
          )}

          {/* Sources */}
          {sources.length > 0 && (
            <div class="mt-3 pt-3 border-t border-gray-200 dark:border-gray-700">
              <span class="text-xs text-gray-500 font-medium">Sources: </span>
              {sources.map((s, i) => (
                <button
                  key={s}
                  onClick={() => handleSourceClick(s)}
                  class="text-xs text-blue-600 dark:text-blue-400 hover:underline"
                >
                  {s.split('/').pop()?.replace('.md', '')}
                  {i < sources.length - 1 && ', '}
                </button>
              ))}
            </div>
          )}
        </div>
      )}

      {/* Input */}
      <form onSubmit={handleSubmit} class="flex gap-2 px-4 py-3 border-t border-gray-200 dark:border-gray-700 bg-gray-50 dark:bg-gray-800">
        <input
          type="text"
          value={question}
          onInput={(e) => setQuestion((e.target as HTMLInputElement).value)}
          placeholder="Ask a question about the wiki..."
          disabled={loading}
          class="flex-1 px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded-md bg-white dark:bg-gray-900 text-gray-900 dark:text-white focus:ring-2 focus:ring-blue-500 focus:border-transparent disabled:opacity-50"
        />
        <button
          type="submit"
          disabled={loading || !question.trim()}
          class="px-4 py-2 text-sm bg-blue-600 text-white rounded-md hover:bg-blue-700 disabled:opacity-50 disabled:cursor-not-allowed"
        >
          {loading ? '...' : 'Ask'}
        </button>
      </form>
    </div>
  );
}
