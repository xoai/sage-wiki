import { useState, useEffect } from 'preact/hooks';
import type { Ref } from 'preact';
import { fetchTree, fetchSearch, type TreeData, type SearchHit } from '../lib/api';

interface Props {
  onNavigate: (path: string) => void;
  onHome: () => void;
  currentPath?: string;
  theme: 'light' | 'dark';
  onToggleTheme: () => void;
  reloadKey?: number;
  searchRef?: Ref<HTMLInputElement>;
}

export function Sidebar({ onNavigate, onHome, currentPath, theme, onToggleTheme, reloadKey, searchRef }: Props) {
  const [tree, setTree] = useState<TreeData | null>(null);
  const [search, setSearch] = useState('');
  const [results, setResults] = useState<SearchHit[]>([]);
  const [expanded, setExpanded] = useState<Record<string, boolean>>({ concepts: true });

  useEffect(() => {
    fetchTree().then(setTree);
  }, [reloadKey]);

  useEffect(() => {
    if (search.length < 2) {
      setResults([]);
      return;
    }
    const timer = setTimeout(() => {
      fetchSearch(search).then(r => setResults(r.results || []));
    }, 300);
    return () => clearTimeout(timer);
  }, [search]);

  const toggle = (key: string) => {
    setExpanded(prev => ({ ...prev, [key]: !prev[key] }));
  };

  return (
    <aside class="w-64 h-full border-r border-gray-200 dark:border-gray-700 bg-white dark:bg-gray-900 flex flex-col overflow-hidden">
      {/* Header */}
      <div class="p-4 border-b border-gray-200 dark:border-gray-700 flex items-center justify-between">
        <h1 class="text-lg font-semibold text-gray-900 dark:text-white">
          <a href="/wiki/" onClick={(e) => { e.preventDefault(); onHome(); }} class="hover:text-blue-500 cursor-pointer">sage-wiki</a>
        </h1>
        <button
          onClick={onToggleTheme}
          title={theme === 'dark' ? 'Switch to light mode' : 'Switch to dark mode'}
          class="p-1.5 rounded-md hover:bg-gray-100 dark:hover:bg-gray-800 text-gray-500 dark:text-gray-400 transition-colors"
        >
          {theme === 'dark' ? (
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/></svg>
          ) : (
            <svg xmlns="http://www.w3.org/2000/svg" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>
          )}
        </button>
      </div>

      {/* Search */}
      <div class="p-3">
        <input
          ref={searchRef}
          type="text"
          placeholder="Search... ( / )"
          value={search}
          onInput={(e) => setSearch((e.target as HTMLInputElement).value)}
          onKeyDown={(e) => { if (e.key === 'Escape') { setSearch(''); (e.target as HTMLInputElement).blur(); } }}
          class="w-full px-3 py-2 text-sm border border-gray-300 dark:border-gray-600 rounded-md bg-gray-50 dark:bg-gray-800 text-gray-900 dark:text-white focus:ring-2 focus:ring-blue-500 focus:border-transparent"
        />
      </div>

      {/* Search results */}
      {results.length > 0 && (
        <div class="px-3 pb-2">
          <div class="text-xs text-gray-500 mb-1">{results.length} results</div>
          {results.map(r => (
            <button
              key={r.id}
              onClick={() => { onNavigate(r.path); setSearch(''); }}
              class="w-full text-left px-2 py-1.5 text-sm rounded hover:bg-gray-100 dark:hover:bg-gray-800 truncate"
            >
              <span class="text-blue-600 dark:text-blue-400">{r.path.split('/').pop()?.replace('.md', '')}</span>
              <span class="text-gray-400 text-xs ml-1">{r.score.toFixed(3)}</span>
            </button>
          ))}
        </div>
      )}

      {/* File tree */}
      <nav class="flex-1 overflow-y-auto px-3 py-2">
        {tree && ['concepts', 'summaries', 'outputs'].map(section => {
          const files = (tree as any)[section] as any[] || [];
          if (files.length === 0) return null;

          return (
            <div key={section} class="mb-3">
              <button
                onClick={() => toggle(section)}
                class="flex items-center w-full text-xs font-semibold uppercase text-gray-500 dark:text-gray-400 mb-1 hover:text-gray-700"
              >
                <span class="mr-1">{expanded[section] ? '▼' : '▶'}</span>
                {section} ({files.length})
              </button>
              {expanded[section] && files.map((f: any) => (
                <button
                  key={f.path}
                  onClick={() => onNavigate(f.path)}
                  class={`w-full text-left px-2 py-1 text-sm rounded truncate ${
                    currentPath === f.path
                      ? 'bg-blue-100 dark:bg-blue-900 text-blue-700 dark:text-blue-300'
                      : 'hover:bg-gray-100 dark:hover:bg-gray-800 text-gray-700 dark:text-gray-300'
                  }`}
                >
                  {f.name}
                </button>
              ))}
            </div>
          );
        })}
      </nav>
    </aside>
  );
}
