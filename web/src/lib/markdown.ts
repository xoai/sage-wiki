import MarkdownIt from 'markdown-it';
import hljs from 'highlight.js';

const md = new MarkdownIt({
  html: false,
  linkify: true,
  typographer: true,
  highlight: (str: string, lang: string, _attrs: string): string => {
    if (lang && hljs.getLanguage(lang)) {
      try {
        return `<pre class="hljs"><code>${hljs.highlight(str, { language: lang }).value}</code></pre>`;
      } catch (_) {}
    }
    return `<pre class="hljs"><code>${md.utils.escapeHtml(str)}</code></pre>`;
  },
});

// Image extensions for ![[image]] embeds
const imageExts = new Set(['.png', '.jpg', '.jpeg', '.gif', '.svg', '.webp']);

function isImage(name: string): boolean {
  const dot = name.lastIndexOf('.');
  return dot >= 0 && imageExts.has(name.slice(dot).toLowerCase());
}

function escapeHtml(s: string): string {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
}

// Wikilink inline rule: matches [[target]] and ![[image.png]] at the token level
// This avoids the children=null crash and works with html:false.
function wikilinkInlineRule(state: any, silent: boolean): boolean {
  const src = state.src;
  const pos = state.pos;

  // Check for ![[  or [[
  const hasBang = src[pos] === '!' && src[pos + 1] === '[' && src[pos + 2] === '[';
  const hasBrackets = src[pos] === '[' && src[pos + 1] === '[';

  if (!hasBang && !hasBrackets) return false;

  const start = hasBang ? pos + 3 : pos + 2;
  const end = src.indexOf(']]', start);
  if (end < 0) return false;

  if (!silent) {
    const target = src.slice(start, end);

    if (hasBang || isImage(target)) {
      // Image embed
      const imgSrc = `/api/files/${target}`;
      const alt = target.split('/').pop()?.replace(/\.[^.]+$/, '') || target;
      const token = state.push('html_inline', '', 0);
      token.content = `<img src="${escapeHtml(imgSrc)}" alt="${escapeHtml(alt)}" loading="lazy" />`;
    } else {
      // Wikilink
      let display = target;
      let linkTarget = target;
      const pipe = target.indexOf('|');
      if (pipe >= 0) {
        display = target.slice(pipe + 1);
        linkTarget = target.slice(0, pipe);
      }

      const href = linkTarget.includes('/')
        ? `/wiki/${linkTarget}`
        : `/wiki/concepts/${linkTarget}`;

      const openToken = state.push('html_inline', '', 0);
      openToken.content = `<a href="${escapeHtml(href)}" class="wikilink text-blue-600 dark:text-blue-400 hover:underline">`;

      const textToken = state.push('text', '', 0);
      textToken.content = display.replace(/-/g, ' ');

      const closeToken = state.push('html_inline', '', 0);
      closeToken.content = '</a>';
    }
  }

  state.pos = end + 2;
  return true;
}

md.inline.ruler.push('wikilink', wikilinkInlineRule);

// Heading ID plugin: adds id attributes to h1-h6 for TOC anchor links
function slugify(text: string): string {
  return text.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/-$/, '').replace(/^-/, '');
}

const originalHeadingOpen = md.renderer.rules.heading_open;
md.renderer.rules.heading_open = function (tokens: any, idx: number, options: any, env: any, self: any) {
  const token = tokens[idx];
  // Get the text content from the next (inline) token
  const contentToken = tokens[idx + 1];
  if (contentToken?.children) {
    const text = contentToken.children.map((t: any) => t.content || '').join('');
    token.attrSet('id', slugify(text));
  }
  if (originalHeadingOpen) {
    return originalHeadingOpen(tokens, idx, options, env, self);
  }
  return self.renderToken(tokens, idx, options);
};

export function renderMarkdown(text: string): string {
  return md.render(text);
}
