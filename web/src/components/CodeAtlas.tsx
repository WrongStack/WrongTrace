import { useState, useMemo, useCallback } from 'react';
import {
  ReactFlow,
  Controls,
  Background,
  MiniMap,
  Panel,
  Handle,
  Position,
  MarkerType,
  type Node,
  type Edge,
  type NodeProps,
} from '@xyflow/react';
import '@xyflow/react/dist/style.css';
import {
  Folder,
  FileCode,
  Code2,
  Boxes,
  Layers,
  Workflow,
  Zap,
  Flame,
  Search,
  SlidersHorizontal,
  LayoutGrid,
  ListTree,
  X,
  Sparkles,
  Bot,
  Activity,
  Maximize2,
  RefreshCw,
} from 'lucide-react';
import type { AtlasSnapshot, AtlasPackage, AtlasFile, AtlasSymbol, EventRecord } from '../types';

interface CodeAtlasProps {
  atlas?: AtlasSnapshot;
  recentEvents?: EventRecord[];
  loading: boolean;
  onRefresh?: () => void;
}

// ----------------------------------------------------
// Custom React Flow Nodes
// ----------------------------------------------------

interface PackageNodeData extends Record<string, unknown> {
  pkg: AtlasPackage;
  onSelect: (pkg: AtlasPackage) => void;
}

function PackageNode({ data }: NodeProps<Node<PackageNodeData>>) {
  const { pkg, onSelect } = data;
  return (
    <div
      onClick={() => onSelect(pkg)}
      className={`px-4 py-3 rounded-xl border transition-all cursor-pointer min-w-[240px] shadow-lg backdrop-blur-md ${
        pkg.is_fragile
          ? 'bg-red-950/40 border-red-500/50 hover:border-red-400 hover:shadow-red-500/20'
          : 'bg-slate-900/90 border-indigo-500/40 hover:border-indigo-400 hover:shadow-indigo-500/20'
      }`}
    >
      <Handle type="target" position={Position.Left} className="!bg-indigo-400 !w-2 !h-2" />
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2">
          <div className="p-1.5 rounded-lg bg-indigo-500/20 text-indigo-400">
            <Folder className="h-4 w-4" />
          </div>
          <div>
            <div className="font-semibold text-xs tracking-tight text-white">{pkg.name}</div>
            <div className="text-[10px] text-slate-400 font-mono truncate max-w-[140px]" title={pkg.path}>
              {pkg.path}
            </div>
          </div>
        </div>
        {pkg.is_fragile && (
          <span className="flex items-center gap-0.5 text-[10px] font-semibold text-red-400 bg-red-500/10 px-1.5 py-0.5 rounded-full border border-red-500/30">
            <Flame className="h-3 w-3" />
            fragile
          </span>
        )}
      </div>

      <div className="mt-2.5 pt-2 border-t border-white/5 flex items-center justify-between text-[11px] text-slate-400">
        <span>{pkg.files.length} files</span>
        <span className="font-mono text-slate-300 font-medium">{pkg.total_loc.toLocaleString()} LOC</span>
      </div>
      <Handle type="source" position={Position.Right} className="!bg-indigo-400 !w-2 !h-2" />
    </div>
  );
}

interface FileNodeData extends Record<string, unknown> {
  file: AtlasFile;
  onSelect: (file: AtlasFile) => void;
}

function FileNode({ data }: NodeProps<Node<FileNodeData>>) {
  const { file, onSelect } = data;
  const scoreColor =
    file.health_score >= 80
      ? 'text-emerald-400 bg-emerald-500/10 border-emerald-500/30'
      : file.health_score >= 50
      ? 'text-amber-400 bg-amber-500/10 border-amber-500/30'
      : 'text-red-400 bg-red-500/10 border-red-500/30';

  return (
    <div
      onClick={() => onSelect(file)}
      className={`px-3.5 py-2.5 rounded-xl border transition-all cursor-pointer min-w-[220px] shadow-md backdrop-blur-md ${
        file.is_fragile
          ? 'bg-red-950/30 border-red-500/40 hover:border-red-400'
          : 'bg-slate-900/80 border-slate-700/60 hover:border-slate-500 hover:shadow-cyan-500/10'
      }`}
    >
      <Handle type="target" position={Position.Left} className="!bg-cyan-400 !w-2 !h-2" />
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-2 min-w-0">
          <div className="p-1 rounded bg-cyan-500/20 text-cyan-400 shrink-0">
            <FileCode className="h-3.5 w-3.5" />
          </div>
          <div className="min-w-0">
            <div className="font-medium text-xs text-slate-100 truncate" title={file.name}>
              {file.name}
            </div>
            <div className="text-[10px] text-slate-400 uppercase tracking-wider font-mono">
              {file.language} · {file.total_loc} LOC
            </div>
          </div>
        </div>
        <div className={`px-1.5 py-0.5 rounded text-[10px] font-mono font-semibold border ${scoreColor}`}>
          {file.health_score}%
        </div>
      </div>

      {file.recent_thrashing_count > 0 && (
        <div className="mt-2 text-[10px] flex items-center gap-1 text-amber-400 bg-amber-500/10 px-1.5 py-0.5 rounded">
          <Flame className="h-2.5 w-2.5 shrink-0" />
          <span>{file.recent_thrashing_count} recent edits</span>
        </div>
      )}
      <Handle type="source" position={Position.Right} className="!bg-cyan-400 !w-2 !h-2" />
    </div>
  );
}

interface SymbolNodeData extends Record<string, unknown> {
  symbol: AtlasSymbol;
  file: AtlasFile;
  onSelect: (symbol: AtlasSymbol, file: AtlasFile) => void;
}

function symbolKindIcon(kind: string) {
  switch (kind) {
    case 'function':
      return <Code2 className="h-3 w-3 text-cyan-400" />;
    case 'method':
      return <Workflow className="h-3 w-3 text-purple-400" />;
    case 'class':
      return <Boxes className="h-3 w-3 text-yellow-400" />;
    case 'struct':
      return <Layers className="h-3 w-3 text-blue-400" />;
    case 'arrow_function':
      return <Zap className="h-3 w-3 text-amber-400" />;
    default:
      return <Code2 className="h-3 w-3 text-slate-400" />;
  }
}

function SymbolNode({ data }: NodeProps<Node<SymbolNodeData>>) {
  const { symbol, file, onSelect } = data;
  const isChurned = symbol.edit_count >= 3;

  return (
    <div
      onClick={() => onSelect(symbol, file)}
      className={`px-3 py-2 rounded-lg border transition-all cursor-pointer min-w-[200px] shadow-sm backdrop-blur-md ${
        symbol.status === 'DELETED'
          ? 'bg-red-950/20 border-red-500/30 opacity-60'
          : symbol.status === 'MODIFIED' || isChurned
          ? 'bg-amber-950/20 border-amber-500/40 hover:border-amber-400'
          : 'bg-slate-900/70 border-white/10 hover:border-indigo-400'
      }`}
    >
      <Handle type="target" position={Position.Left} className="!bg-purple-400 !w-2 !h-2" />
      <div className="flex items-center justify-between gap-1.5">
        <div className="flex items-center gap-1.5 min-w-0">
          <div className="p-0.5 rounded bg-white/5 shrink-0">{symbolKindIcon(symbol.kind)}</div>
          <div className="font-mono text-xs text-slate-200 truncate" title={symbol.node_signature}>
            {symbol.name}
          </div>
        </div>
        <span className="text-[9px] font-mono text-slate-400 shrink-0">L{symbol.start_line}</span>
      </div>

      <div className="mt-1 flex items-center justify-between text-[10px] text-slate-400">
        <span className="font-mono text-slate-400">{symbol.lines_of_code} LOC</span>
        {symbol.edit_count > 0 && (
          <span
            className={`font-mono text-[9px] px-1 py-0.2 rounded ${
              isChurned ? 'text-red-400 bg-red-500/15' : 'text-slate-300 bg-white/5'
            }`}
          >
            {symbol.edit_count}x edit
          </span>
        )}
      </div>
    </div>
  );
}

const nodeTypes = {
  packageNode: PackageNode,
  fileNode: FileNode,
  symbolNode: SymbolNode,
};

// ----------------------------------------------------
// Main CodeAtlas Component
// ----------------------------------------------------

export function CodeAtlas({ atlas, recentEvents, loading, onRefresh }: CodeAtlasProps) {
  const [viewMode, setViewMode] = useState<'graph' | 'tree'>('graph');
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedKind, setSelectedKind] = useState<string>('all');
  const [selectedFilter, setSelectedFilter] = useState<'all' | 'fragile' | 'modified'>('all');
  const [selectedItem, setSelectedItem] = useState<{
    type: 'package' | 'file' | 'symbol';
    pkg?: AtlasPackage;
    file?: AtlasFile;
    symbol?: AtlasSymbol;
  } | null>(null);

  // Filtered packages, files, symbols
  const filteredPackages = useMemo(() => {
    if (!atlas?.packages) return [];
    const q = searchQuery.toLowerCase().trim();

    return atlas.packages
      .map((pkg) => {
        const filteredFiles = pkg.files
          .map((file) => {
            const filteredSymbols = file.symbols.filter((sym) => {
              if (selectedKind !== 'all' && sym.kind !== selectedKind) return false;
              if (selectedFilter === 'modified' && sym.status !== 'MODIFIED') return false;
              if (selectedFilter === 'fragile' && sym.edit_count < 3) return false;
              if (q && !sym.name.toLowerCase().includes(q) && !sym.node_signature.toLowerCase().includes(q)) {
                return false;
              }
              return true;
            });

            if (selectedFilter === 'fragile' && !file.is_fragile && filteredSymbols.length === 0) {
              return null;
            }

            if (
              q &&
              !file.name.toLowerCase().includes(q) &&
              !file.path.toLowerCase().includes(q) &&
              filteredSymbols.length === 0
            ) {
              return null;
            }

            return { ...file, symbols: filteredSymbols };
          })
          .filter((f): f is AtlasFile => f !== null);

        if (filteredFiles.length === 0 && q && !pkg.name.toLowerCase().includes(q) && !pkg.path.toLowerCase().includes(q)) {
          return null;
        }

        return { ...pkg, files: filteredFiles };
      })
      .filter((p): p is AtlasPackage => p !== null);
  }, [atlas, searchQuery, selectedKind, selectedFilter]);

  // Layout React Flow Nodes & Edges
  const { nodes, edges } = useMemo(() => {
    const nList: Node[] = [];
    const eList: Edge[] = [];

    let currentY = 50;
    const PKG_X = 50;
    const FILE_X = 380;
    const SYM_X = 700;

    const handleSelectPkg = (pkg: AtlasPackage) => setSelectedItem({ type: 'package', pkg });
    const handleSelectFile = (file: AtlasFile) => setSelectedItem({ type: 'file', file });
    const handleSelectSymbol = (symbol: AtlasSymbol, file: AtlasFile) =>
      setSelectedItem({ type: 'symbol', symbol, file });

    filteredPackages.forEach((pkg, pIdx) => {
      const pkgNodeId = `pkg-${pIdx}-${pkg.path}`;
      const pkgStartY = currentY;

      let fileY = currentY;

      pkg.files.forEach((file, fIdx) => {
        const fileNodeId = `file-${pIdx}-${fIdx}-${file.path}`;
        const fileStartY = fileY;

        let symY = fileY;

        file.symbols.slice(0, 10).forEach((sym, sIdx) => {
          const symNodeId = `sym-${pIdx}-${fIdx}-${sIdx}-${sym.node_signature}`;
          nList.push({
            id: symNodeId,
            type: 'symbolNode',
            position: { x: SYM_X, y: symY },
            data: { symbol: sym, file, onSelect: handleSelectSymbol },
          });

          eList.push({
            id: `edge-${fileNodeId}-${symNodeId}`,
            source: fileNodeId,
            target: symNodeId,
            animated: sym.status === 'MODIFIED',
            style: {
              stroke: sym.status === 'MODIFIED' ? '#f59e0b' : 'rgba(148, 163, 184, 0.25)',
              strokeWidth: 1.5,
            },
            markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#64748b' },
          });

          symY += 60;
        });

        const fileSpan = Math.max(75, file.symbols.length * 60);
        nList.push({
          id: fileNodeId,
          type: 'fileNode',
          position: { x: FILE_X, y: fileStartY },
          data: { file, onSelect: handleSelectFile },
        });

        eList.push({
          id: `edge-${pkgNodeId}-${fileNodeId}`,
          source: pkgNodeId,
          target: fileNodeId,
          style: { stroke: 'rgba(99, 102, 241, 0.4)', strokeWidth: 1.5 },
          markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#818cf8' },
        });

        fileY += fileSpan + 25;
      });

      const pkgSpan = Math.max(80, fileY - pkgStartY);
      nList.push({
        id: pkgNodeId,
        type: 'packageNode',
        position: { x: PKG_X, y: pkgStartY },
        data: { pkg, onSelect: handleSelectPkg },
      });

      currentY += pkgSpan + 50;
    });

    return { nodes: nList, edges: eList };
  }, [filteredPackages]);

  return (
    <div className="space-y-4">
      {/* Top Header & Filter Toolbar */}
      <div className="panel flex flex-wrap items-center justify-between gap-4">
        <div className="flex items-center gap-3">
          <div className="p-2 rounded-lg bg-accent/20 text-accent">
            <Boxes className="h-5 w-5" />
          </div>
          <div>
            <h2 className="font-semibold tracking-tight text-base flex items-center gap-2">
              Code Atlas
              <span className="text-xs font-normal text-slate-400">
                · {atlas?.total_files ?? 0} files, {atlas?.total_nodes ?? 0} symbols
              </span>
            </h2>
            <p className="text-xs text-slate-400">
              Interactive architectural map across packages, files, and AST nodes.
            </p>
          </div>
        </div>

        {/* Controls */}
        <div className="flex flex-wrap items-center gap-2.5">
          {/* Search */}
          <div className="relative">
            <Search className="h-3.5 w-3.5 absolute left-2.5 top-1/2 -translate-y-1/2 text-slate-400" />
            <input
              type="text"
              placeholder="Search symbols, files, packages…"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              className="pl-8 pr-3 py-1.5 text-xs bg-slate-900/90 border border-white/10 rounded-lg text-slate-200 placeholder-slate-500 focus:outline-none focus:border-accent w-48 sm:w-64"
            />
          </div>

          {/* Kind Filter */}
          <select
            value={selectedKind}
            onChange={(e) => setSelectedKind(e.target.value)}
            className="text-xs bg-slate-900 border border-white/10 rounded-lg px-2.5 py-1.5 text-slate-300 focus:outline-none focus:border-accent"
          >
            <option value="all">All Symbols</option>
            <option value="function">Functions</option>
            <option value="method">Methods</option>
            <option value="class">Classes</option>
            <option value="struct">Structs</option>
            <option value="arrow_function">Arrow Functions</option>
          </select>

          {/* Status Filter */}
          <select
            value={selectedFilter}
            onChange={(e) => setSelectedFilter(e.target.value as any)}
            className="text-xs bg-slate-900 border border-white/10 rounded-lg px-2.5 py-1.5 text-slate-300 focus:outline-none focus:border-accent"
          >
            <option value="all">All States</option>
            <option value="fragile">Thrashing Only (≥3x)</option>
            <option value="modified">Modified Only</option>
          </select>

          {/* View Mode Toggle */}
          <div className="flex items-center bg-slate-900 border border-white/10 rounded-lg p-0.5">
            <button
              onClick={() => setViewMode('graph')}
              className={`flex items-center gap-1 px-2.5 py-1 text-xs rounded-md transition-all ${
                viewMode === 'graph' ? 'bg-accent text-white font-medium shadow-sm' : 'text-slate-400 hover:text-white'
              }`}
            >
              <LayoutGrid className="h-3.5 w-3.5" />
              Graph
            </button>
            <button
              onClick={() => setViewMode('tree')}
              className={`flex items-center gap-1 px-2.5 py-1 text-xs rounded-md transition-all ${
                viewMode === 'tree' ? 'bg-accent text-white font-medium shadow-sm' : 'text-slate-400 hover:text-white'
              }`}
            >
              <ListTree className="h-3.5 w-3.5" />
              Tree
            </button>
          </div>

          {onRefresh && (
            <button
              onClick={onRefresh}
              className="p-1.5 text-slate-400 hover:text-white bg-slate-900 border border-white/10 rounded-lg hover:border-slate-600 transition-all"
              title="Refresh Code Atlas"
            >
              <RefreshCw className={`h-4 w-4 ${loading ? 'animate-spin' : ''}`} />
            </button>
          )}
        </div>
      </div>

      {/* Main Content Area */}
      <div className="grid grid-cols-1 lg:grid-cols-12 gap-4">
        {/* Canvas or Tree View */}
        <div className={`${selectedItem ? 'lg:col-span-8' : 'lg:col-span-12'} transition-all`}>
          {viewMode === 'graph' ? (
            <div className="panel p-0 h-[650px] overflow-hidden rounded-xl relative border border-white/10 bg-[#0c1017]">
              {loading && nodes.length === 0 ? (
                <div className="flex items-center justify-center h-full text-slate-400 text-sm gap-2">
                  <RefreshCw className="h-4 w-4 animate-spin text-accent" />
                  Building Code Atlas map…
                </div>
              ) : nodes.length === 0 ? (
                <div className="flex flex-col items-center justify-center h-full text-slate-500 text-sm gap-2">
                  <Boxes className="h-8 w-8 text-slate-600" />
                  <span>No code nodes match the current filters.</span>
                </div>
              ) : (
                <ReactFlow
                  nodes={nodes}
                  edges={edges}
                  nodeTypes={nodeTypes}
                  fitView
                  minZoom={0.2}
                  maxZoom={1.8}
                  proOptions={{ hideAttribution: true }}
                >
                  <Background color="#1e293b" gap={20} size={1} />
                  <Controls className="!bg-slate-900 !border-white/10 !fill-slate-300" />
                  <MiniMap
                    nodeColor={(n) => {
                      if (n.type === 'packageNode') return '#6366f1';
                      if (n.type === 'fileNode') return '#06b6d4';
                      return '#a855f7';
                    }}
                    maskColor="rgba(15, 23, 42, 0.7)"
                    className="!bg-slate-950 !border-white/10 !rounded-lg"
                  />
                  <Panel position="top-right" className="bg-slate-900/80 backdrop-blur-md px-3 py-1.5 rounded-lg border border-white/10 text-[11px] text-slate-400 flex items-center gap-3">
                    <span className="flex items-center gap-1"><span className="h-2 w-2 rounded-full bg-indigo-500" /> Package</span>
                    <span className="flex items-center gap-1"><span className="h-2 w-2 rounded-full bg-cyan-500" /> File</span>
                    <span className="flex items-center gap-1"><span className="h-2 w-2 rounded-full bg-purple-500" /> Symbol</span>
                  </Panel>
                </ReactFlow>
              )}
            </div>
          ) : (
            /* Tree Explorer View */
            <div className="panel max-h-[650px] overflow-y-auto space-y-3">
              {filteredPackages.map((pkg) => (
                <div key={pkg.path} className="border border-white/5 rounded-xl bg-slate-900/50 overflow-hidden">
                  <div
                    onClick={() => setSelectedItem({ type: 'package', pkg })}
                    className="px-4 py-2.5 bg-white/5 flex items-center justify-between cursor-pointer hover:bg-white/10"
                  >
                    <div className="flex items-center gap-2">
                      <Folder className="h-4 w-4 text-indigo-400" />
                      <span className="font-semibold text-xs text-white">{pkg.name}</span>
                      <span className="text-[11px] text-slate-500 font-mono">({pkg.path})</span>
                    </div>
                    <span className="text-xs font-mono text-slate-400">{pkg.total_loc} LOC</span>
                  </div>

                  <div className="p-3 space-y-3">
                    {pkg.files.map((file) => (
                      <div key={file.path} className="pl-3 border-l-2 border-slate-700/50 space-y-2">
                        <div
                          onClick={() => setSelectedItem({ type: 'file', file })}
                          className="flex items-center justify-between cursor-pointer hover:text-cyan-300"
                        >
                          <div className="flex items-center gap-2">
                            <FileCode className="h-3.5 w-3.5 text-cyan-400" />
                            <span className="text-xs font-medium text-slate-200">{file.name}</span>
                            <span className="text-[10px] text-slate-500 uppercase font-mono">{file.language}</span>
                          </div>
                          <span className="text-[11px] font-mono text-emerald-400">{file.health_score}% health</span>
                        </div>

                        <div className="pl-4 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-1.5">
                          {file.symbols.map((sym) => (
                            <div
                              key={sym.node_signature}
                              onClick={() => setSelectedItem({ type: 'symbol', symbol: sym, file })}
                              className="px-2 py-1 rounded bg-slate-950/60 border border-white/5 flex items-center justify-between text-xs cursor-pointer hover:border-purple-500/50"
                            >
                              <div className="flex items-center gap-1.5 truncate">
                                {symbolKindIcon(sym.kind)}
                                <span className="font-mono text-slate-300 truncate" title={sym.node_signature}>
                                  {sym.name}
                                </span>
                              </div>
                              <span className="text-[10px] font-mono text-slate-500 shrink-0">{sym.lines_of_code}L</span>
                            </div>
                          ))}
                        </div>
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>

        {/* Detail Inspector Drawer */}
        {selectedItem && (
          <div className="lg:col-span-4 panel space-y-4 animate-in fade-in slide-in-from-right-4 duration-200">
            <div className="flex items-center justify-between border-b border-white/10 pb-3">
              <div className="flex items-center gap-2">
                <Sparkles className="h-4 w-4 text-accent" />
                <h3 className="font-semibold text-sm capitalize">
                  {selectedItem.type} Inspector
                </h3>
              </div>
              <button
                onClick={() => setSelectedItem(null)}
                className="p-1 rounded-md text-slate-400 hover:text-white hover:bg-white/5"
              >
                <X className="h-4 w-4" />
              </button>
            </div>

            {selectedItem.type === 'symbol' && selectedItem.symbol && (
              <div className="space-y-3 text-xs">
                <div>
                  <div className="text-slate-400 mb-1">Symbol Signature</div>
                  <div className="font-mono text-slate-200 bg-slate-950 p-2 rounded-lg border border-white/5 break-all">
                    {selectedItem.symbol.node_signature}
                  </div>
                </div>

                <div className="grid grid-cols-2 gap-2">
                  <div className="panel-raised p-2.5">
                    <div className="text-slate-400 text-[11px]">Kind</div>
                    <div className="font-medium text-slate-200 capitalize mt-0.5">
                      {selectedItem.symbol.kind}
                    </div>
                  </div>
                  <div className="panel-raised p-2.5">
                    <div className="text-slate-400 text-[11px]">Lines of Code</div>
                    <div className="font-mono text-slate-200 mt-0.5">
                      {selectedItem.symbol.lines_of_code} (L{selectedItem.symbol.start_line}-L{selectedItem.symbol.end_line})
                    </div>
                  </div>
                  <div className="panel-raised p-2.5">
                    <div className="text-slate-400 text-[11px]">Edit / Churn Count</div>
                    <div className="font-mono text-slate-200 mt-0.5">
                      {selectedItem.symbol.edit_count} times
                    </div>
                  </div>
                  <div className="panel-raised p-2.5">
                    <div className="text-slate-400 text-[11px]">Status</div>
                    <div className="font-medium text-accent mt-0.5">
                      {selectedItem.symbol.status}
                    </div>
                  </div>
                </div>

                {selectedItem.symbol.last_model && (
                  <div className="panel-raised p-2.5 flex items-center gap-2">
                    <Bot className="h-4 w-4 text-accent" />
                    <div>
                      <div className="text-[10px] text-slate-400">Last touched by AI Model</div>
                      <div className="font-mono text-slate-200 font-semibold">{selectedItem.symbol.last_model}</div>
                    </div>
                  </div>
                )}

                {selectedItem.symbol.ast_content_hash && (
                  <div>
                    <div className="text-slate-400 mb-1">AST Normalized Hash</div>
                    <div className="font-mono text-[10px] text-slate-400 bg-slate-950 p-1.5 rounded border border-white/5 truncate">
                      {selectedItem.symbol.ast_content_hash}
                    </div>
                  </div>
                )}

                {/* Latest Diff Snippet if symbol was modified */}
                {(() => {
                  const ev = recentEvents?.find((r) => r.node_signature === selectedItem.symbol?.node_signature && !!r.diff_snippet);
                  if (!ev) return null;
                  return (
                    <div>
                      <div className="text-slate-400 mb-1 flex items-center justify-between">
                        <span>Latest Code Change Diff ({ev.action})</span>
                        {(ev.added_lines || ev.deleted_lines) && (
                          <span className="font-mono text-[10px]">
                            {ev.added_lines ? <span className="text-emerald-400">+{ev.added_lines} </span> : null}
                            {ev.deleted_lines ? <span className="text-red-400">-{ev.deleted_lines}</span> : null}
                          </span>
                        )}
                      </div>
                      <div className="p-2.5 max-h-48 overflow-y-auto rounded-lg bg-[#0a0e14] border border-white/10 font-mono text-[10px] leading-relaxed space-y-0.5 select-text">
                        {ev.diff_snippet?.split('\n').map((line, idx) => {
                          const isAdd = line.startsWith('+ ');
                          const isDel = line.startsWith('- ');
                          return (
                            <div
                              key={idx}
                              className={
                                isAdd
                                  ? 'text-emerald-400 bg-emerald-500/10 -mx-2 px-2 py-0.2 border-l-2 border-emerald-500'
                                  : isDel
                                  ? 'text-red-400 bg-red-500/10 -mx-2 px-2 py-0.2 border-l-2 border-red-500'
                                  : 'text-slate-300'
                              }
                            >
                              {line}
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  );
                })()}

                <div className="p-3 rounded-lg bg-indigo-500/10 border border-indigo-500/20 text-indigo-200 text-[11px]">
                  💡 <strong>Agent Guardrail:</strong>{' '}
                  {selectedItem.symbol.edit_count >= 5
                    ? 'High thrashing detected on this symbol. Avoid rewriting unless addressing failing tests.'
                    : 'Symbol is stable and safe for refactoring.'}
                </div>
              </div>
            )}

            {selectedItem.type === 'file' && selectedItem.file && (
              <div className="space-y-3 text-xs">
                <div>
                  <div className="text-slate-400 mb-1">File Path</div>
                  <div className="font-mono text-slate-200 bg-slate-950 p-2 rounded-lg border border-white/5 break-all">
                    {selectedItem.file.path}
                  </div>
                </div>

                <div className="grid grid-cols-2 gap-2">
                  <div className="panel-raised p-2.5">
                    <div className="text-slate-400 text-[11px]">Health Score</div>
                    <div className="font-bold text-emerald-400 text-sm mt-0.5">
                      {selectedItem.file.health_score}/100
                    </div>
                  </div>
                  <div className="panel-raised p-2.5">
                    <div className="text-slate-400 text-[11px]">Total Symbols</div>
                    <div className="font-mono text-slate-200 mt-0.5">
                      {selectedItem.file.symbols.length} nodes
                    </div>
                  </div>
                </div>
              </div>
            )}

            {selectedItem.type === 'package' && selectedItem.pkg && (
              <div className="space-y-3 text-xs">
                <div>
                  <div className="text-slate-400 mb-1">Package Module</div>
                  <div className="font-mono text-slate-200 bg-slate-950 p-2 rounded-lg border border-white/5 break-all">
                    {selectedItem.pkg.path}
                  </div>
                </div>
                <div className="panel-raised p-2.5">
                  <div className="text-slate-400 text-[11px]">Files Contained</div>
                  <div className="font-medium text-slate-200 mt-0.5">
                    {selectedItem.pkg.files.length} files ({selectedItem.pkg.total_loc} total LOC)
                  </div>
                </div>
              </div>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
