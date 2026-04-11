import { useState, useEffect, useRef } from 'preact/hooks';
import { Sidebar } from './components/Sidebar';
import { ArticleView } from './components/ArticleView';
import { GraphView } from './components/GraphView';
import { connectHotReload } from './lib/hotreload';
import './style.css';

function getInitialTheme(): 'light' | 'dark' {
  const stored = localStorage.getItem('sage-wiki-theme');
  if (stored === 'dark' || stored === 'light') return stored;
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

export function App() {
  const [currentPath, setCurrentPath] = useState<string | null>(() => {
    const urlPath = window.location.pathname.replace('/wiki/', '');
    if (urlPath && urlPath !== '/' && !urlPath.startsWith('/')) {
      return urlPath + '.md';
    }
    return null;
  });
  const [sidebarOpen, setSidebarOpen] = useState(true);
  const [theme, setTheme] = useState<'light' | 'dark'>(getInitialTheme);
  const [reloadKey, setReloadKey] = useState(0);
  const searchRef = useRef<HTMLInputElement>(null);

  // Apply dark class to <html>
  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark');
    localStorage.setItem('sage-wiki-theme', theme);
  }, [theme]);

  const toggleTheme = () => setTheme(t => t === 'dark' ? 'light' : 'dark');

  const navigate = (path: string) => {
    setCurrentPath(path);
    window.history.pushState({}, '', `/wiki/${path.replace('.md', '')}`);
  };

  // Handle browser back/forward
  useEffect(() => {
    const onPopState = () => {
      const path = window.location.pathname.replace('/wiki/', '') + '.md';
      if (path !== '.md') setCurrentPath(path);
      else setCurrentPath(null);
    };
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, []);

  // Hot reload: bump reloadKey to force re-fetch in children
  useEffect(() => {
    return connectHotReload(() => {
      setReloadKey(k => k + 1);
    });
  }, []);

  // Keyboard shortcuts
  useEffect(() => {
    const onKeyDown = (e: KeyboardEvent) => {
      // Ignore when typing in an input/textarea
      const tag = (e.target as HTMLElement).tagName;
      if (tag === 'INPUT' || tag === 'TEXTAREA') return;

      if (e.key === '/') {
        e.preventDefault();
        searchRef.current?.focus();
      }
    };
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, []);

  return (
    <div class="flex h-screen bg-white dark:bg-gray-900 text-gray-900 dark:text-gray-100">
      {/* Mobile menu button */}
      <button
        onClick={() => setSidebarOpen(!sidebarOpen)}
        class="lg:hidden fixed top-4 left-4 z-50 p-2 rounded-md bg-white dark:bg-gray-800 shadow"
      >
        {sidebarOpen ? '✕' : '☰'}
      </button>

      {/* Sidebar */}
      <div class={`${sidebarOpen ? 'block' : 'hidden'} lg:block`}>
        <Sidebar onNavigate={navigate} onHome={() => { setCurrentPath(null); window.history.pushState({}, '', '/wiki/'); }} currentPath={currentPath || undefined} theme={theme} onToggleTheme={toggleTheme} reloadKey={reloadKey} searchRef={searchRef} />
      </div>

      {/* Main content */}
      <main class="flex-1 min-h-0 flex">
        {currentPath ? (
          <ArticleView path={currentPath} onNavigate={navigate} reloadKey={reloadKey} />
        ) : (
          <div class="flex-1 h-full">
            <GraphView onNavigate={navigate} />
          </div>
        )}
      </main>
    </div>
  );
}
