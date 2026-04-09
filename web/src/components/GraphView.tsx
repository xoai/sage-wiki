import { useEffect, useRef, useState } from 'preact/hooks';
import ForceGraph2D from 'force-graph';
import { fetchGraph, type GraphData } from '../lib/api';

interface Props {
  currentArticle?: string; // e.g. "self-attention"
  onNavigate: (path: string) => void;
}

const TYPE_COLORS: Record<string, string> = {
  concept: '#3b82f6',   // blue
  technique: '#22c55e', // green
  source: '#6b7280',    // gray
  artifact: '#f59e0b',  // amber
  person: '#ec4899',    // pink
};

export function GraphView({ currentArticle, onNavigate }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const graphRef = useRef<any>(null);
  const [loading, setLoading] = useState(true);
  const [nodeCount, setNodeCount] = useState(0);

  useEffect(() => {
    if (!containerRef.current) return;

    const container = containerRef.current;
    const width = container.clientWidth;
    const height = container.clientHeight;

    // Create graph instance
    const graph = new ForceGraph2D(container)
      .width(width)
      .height(height)
      .nodeLabel((node: any) => node.name)
      .nodeColor((node: any) => {
        if (currentArticle && node.id === currentArticle) return '#ef4444'; // red for current
        return TYPE_COLORS[node.type] || '#6b7280';
      })
      .nodeVal((node: any) => Math.max(2, Math.min(8, node.connections || 1)))
      .linkColor(() => 'rgba(156, 163, 175, 0.3)')
      .linkWidth(0.5)
      .linkDirectionalArrowLength(3)
      .linkDirectionalArrowRelPos(1)
      .onNodeClick((node: any) => {
        const path = `concepts/${node.id}.md`;
        onNavigate(path);
      })
      .cooldownTicks(100)
      .onEngineStop(() => graph.zoomToFit(400, 40));

    graphRef.current = graph;

    // Fetch data — use neighborhood if current article, else full graph
    const fetchData = currentArticle
      ? fetchGraph(currentArticle, 3)
      : fetchGraph();

    fetchData.then((data: GraphData) => {
      setNodeCount(data.total);
      setLoading(false);

      // Transform to force-graph format
      const graphData = {
        nodes: data.nodes.map(n => ({ ...n })),
        links: data.edges.map(e => ({
          source: e.source,
          target: e.target,
          relation: e.relation,
        })),
      };

      graph.graphData(graphData);
    }).catch(() => setLoading(false));

    // Handle resize
    const observer = new ResizeObserver(() => {
      if (containerRef.current) {
        graph.width(containerRef.current.clientWidth);
        graph.height(containerRef.current.clientHeight);
      }
    });
    observer.observe(container);

    return () => {
      observer.disconnect();
      graph._destructor?.();
    };
  }, [currentArticle]);

  return (
    <div class="h-full flex flex-col">
      <div class="px-3 py-2 border-b border-gray-200 dark:border-gray-700 flex items-center justify-between">
        <h3 class="text-xs font-semibold uppercase text-gray-500">
          Graph {nodeCount > 0 && `(${nodeCount})`}
        </h3>
        <div class="flex gap-2 text-xs text-gray-400">
          <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-blue-500 inline-block" /> concept</span>
          <span class="flex items-center gap-1"><span class="w-2 h-2 rounded-full bg-green-500 inline-block" /> technique</span>
        </div>
      </div>
      <div ref={containerRef} class="flex-1 relative">
        {loading && (
          <div class="absolute inset-0 flex items-center justify-center text-gray-400">
            Loading graph...
          </div>
        )}
      </div>
    </div>
  );
}
