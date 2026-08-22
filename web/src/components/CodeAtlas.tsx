import { useState, useMemo, useCallback, useDeferredValue } from 'react';
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
  ChevronRight,
  ChevronDown,
  FolderOpen,
  ArrowLeft,
  Compass,
  Circle,
  GitFork,
} from 'lucide-react';
import { RichDiffViewer } from './RichDiffViewer';
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

interface RootProjectNodeData extends Record<string, unknown> {
  repo: string;
  isMonorepo?: boolean;
  workspacesCount?: number;
  totalPackages: number;
  totalFiles: number;
  totalLOC: number;
}

function RootProjectNode({ data }: NodeProps<Node<RootProjectNodeData>>) {
  const { repo, isMonorepo, workspacesCount, totalPackages, totalFiles, totalLOC } = data;
  return (
    <div className="px-5 py-4 rounded-2xl border border-accent/40 bg-gradient-to-br from-slate-900/95 via-indigo-950/40 to-slate-900/95 shadow-2xl backdrop-blur-xl min-w-[270px]">
      <div className="flex items-center justify-between gap-2.5">
        <div className="flex items-center gap-2.5">
          <div className="p-2 rounded-xl bg-accent/20 text-accent shadow-inner border border-accent/30">
            <Boxes className="h-5 w-5" />
          </div>
          <div>
            <div className="text-[10px] uppercase font-mono tracking-wider text-accent font-semibold flex items-center gap-1.5">
              {isMonorepo ? 'Monorepo Root' : 'Project Root'}
            </div>
            <div className="font-bold text-sm tracking-tight text-white">{repo || 'Workspace'}</div>
          </div>
        </div>
        {isMonorepo && (
          <span className="chip bg-purple-500/15 text-purple-300 border border-purple-500/30 text-[10px] font-mono font-semibold">
            {workspacesCount} Workspaces
          </span>
        )}
      </div>
      <div className="mt-3 pt-2.5 border-t border-white/10 flex items-center justify-between text-xs text-slate-300 font-mono">
        <span>{totalPackages} packages</span>
        <span>·</span>
        <span>{totalFiles} files</span>
        <span>·</span>
        <span className="text-accent font-semibold">{totalLOC.toLocaleString()} LOC</span>
      </div>
      <Handle type="source" position={Position.Right} className="!bg-accent !w-2.5 !h-2.5" />
    </div>
  );
}

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
            <div className="flex items-center gap-1.5">
              <span className="font-semibold text-xs tracking-tight text-white">{pkg.name}</span>
              {pkg.workspace && pkg.workspace !== 'root' && (
                <span className="text-[9px] px-1.5 py-0.2 rounded bg-indigo-500/15 text-indigo-300 border border-indigo-500/30 font-mono">
                  {pkg.workspace}
                </span>
              )}
            </div>
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

interface MoreItemsNodeData extends Record<string, unknown> {
  count: number;
  label: string;
  onSwitchToTree?: () => void;
}

function MoreItemsNode({ data }: NodeProps<Node<MoreItemsNodeData>>) {
  const { count, label, onSwitchToTree } = data;
  return (
    <div
      onClick={onSwitchToTree}
      className="px-4 py-3 rounded-xl border border-dashed border-indigo-500/40 bg-slate-900/80 hover:bg-indigo-950/40 hover:border-indigo-400 transition-all cursor-pointer text-center backdrop-blur-md min-w-[200px] shadow-lg"
    >
      <Handle type="target" position={Position.Left} className="!bg-indigo-400 !w-2 !h-2" />
      <div className="text-xs font-mono font-semibold text-slate-300">
        + {count} more {label}
      </div>
      <div className="text-[10px] text-accent mt-1 font-medium">
        Explore in Tree View →
      </div>
      <Handle type="source" position={Position.Right} className="!bg-indigo-400 !w-2 !h-2" />
    </div>
  );
}

const nodeTypes = {
  rootProjectNode: RootProjectNode,
  packageNode: PackageNode,
  fileNode: FileNode,
  symbolNode: SymbolNode,
  moreItemsNode: MoreItemsNode,
};

// ----------------------------------------------------
// Main CodeAtlas Component
// ----------------------------------------------------

export function CodeAtlas({ atlas, recentEvents, loading, onRefresh }: CodeAtlasProps) {
  const [viewMode, setViewMode] = useState<'graph' | 'tree'>('graph');
  const [searchQuery, setSearchQuery] = useState('');
  const [selectedWorkspace, setSelectedWorkspace] = useState<string>('all');
  const [selectedKind, setSelectedKind] = useState<string>('all');
  const [selectedFilter, setSelectedFilter] = useState<'all' | 'fragile' | 'modified'>('all');
  const [expandedFolders, setExpandedFolders] = useState<Set<string>>(new Set(['.']));
  const [expandedFiles, setExpandedFiles] = useState<Set<string>>(new Set());
  const [selectedItem, setSelectedItem] = useState<{
    type: 'package' | 'file' | 'symbol';
    pkg?: AtlasPackage;
    file?: AtlasFile;
    symbol?: AtlasSymbol;
  } | null>(null);

  const toggleFolder = (path: string, e?: React.MouseEvent) => {
    e?.stopPropagation();
    setExpandedFolders((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };

  const toggleFile = (path: string, e?: React.MouseEvent) => {
    e?.stopPropagation();
    setExpandedFiles((prev) => {
      const next = new Set(prev);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  };

  const expandAll = () => {
    if (!atlas?.packages) return;
    const folders = new Set<string>();
    const files = new Set<string>();
    atlas.packages.forEach((p) => {
      folders.add(p.path);
      p.files.forEach((f) => files.add(f.path));
    });
    setExpandedFolders(folders);
    setExpandedFiles(files);
  };

  const collapseAll = () => {
    setExpandedFolders(new Set());
    setExpandedFiles(new Set());
  };

  const deferredQuery = useDeferredValue(searchQuery);

  // Filtered packages, files, symbols
  const filteredPackages = useMemo(() => {
    if (!atlas?.packages) return [];
    const q = deferredQuery.toLowerCase().trim();

    return atlas.packages
      .filter((pkg) => {
        if (selectedWorkspace !== 'all' && pkg.workspace !== selectedWorkspace) {
          return false;
        }
        return true;
      })
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
  }, [atlas, deferredQuery, selectedWorkspace, selectedKind, selectedFilter]);

  const [focusedScope, setFocusedScope] = useState<
    | { level: 'all' }
    | { level: 'package'; pkg: AtlasPackage }
    | { level: 'file'; pkg: AtlasPackage; file: AtlasFile }
  >({ level: 'all' });

  const [graphLayout, setGraphLayout] = useState<'radial' | 'tree' | 'grid'>('radial');

  // Layout React Flow Nodes & Edges (Supporting Radial / Orbit, Hierarchical Tree, and Grid)
  const { nodes, edges } = useMemo(() => {
    const nList: Node[] = [];
    const eList: Edge[] = [];

    const handleSelectPkg = (pkg: AtlasPackage) => {
      setSelectedItem({ type: 'package', pkg });
      setFocusedScope({ level: 'package', pkg });
    };

    const handleSelectFile = (file: AtlasFile, pkg?: AtlasPackage) => {
      setSelectedItem({ type: 'file', file });
      const parentPkg = pkg || (focusedScope.level !== 'all' ? focusedScope.pkg : filteredPackages.find((p) => p.files.some((f) => f.path === file.path)));
      if (parentPkg) {
        setFocusedScope({ level: 'file', pkg: parentPkg, file });
      }
    };

    const handleSelectSymbol = (symbol: AtlasSymbol, file: AtlasFile) => {
      setSelectedItem({ type: 'symbol', symbol, file });
    };

    if (focusedScope.level === 'all') {
      const rootNodeId = 'root-project-node';

      if (graphLayout === 'radial') {
        // RADIAL / ORBIT MODE: Solar System Orbit around Project / Monorepo Root
        const CENTER_X = 450;
        const CENTER_Y = 340;
        const total = Math.max(1, filteredPackages.length);
        const R = Math.max(340, total * 38);

        nList.push({
          id: rootNodeId,
          type: 'rootProjectNode',
          position: { x: CENTER_X - 135, y: CENTER_Y - 50 },
          data: {
            repo: atlas?.repo || 'Workspace',
            isMonorepo: atlas?.is_monorepo,
            workspacesCount: atlas?.workspaces?.length || 0,
            totalPackages: atlas?.packages?.length || filteredPackages.length,
            totalFiles: atlas?.total_files || 0,
            totalLOC: atlas?.total_loc || 0,
          },
        });

        filteredPackages.forEach((pkg, pIdx) => {
          const theta = (2 * Math.PI * pIdx) / total - Math.PI / 2;
          const px = CENTER_X + R * Math.cos(theta) - 120;
          const py = CENTER_Y + R * Math.sin(theta) - 40;
          const pkgNodeId = `pkg-${pIdx}-${pkg.path}`;

          nList.push({
            id: pkgNodeId,
            type: 'packageNode',
            position: { x: px, y: py },
            data: { pkg, onSelect: handleSelectPkg },
          });

          eList.push({
            id: `edge-${rootNodeId}-${pkgNodeId}`,
            source: rootNodeId,
            target: pkgNodeId,
            animated: pkg.is_fragile,
            style: {
              stroke: pkg.is_fragile ? '#ef4444' : 'rgba(99, 102, 241, 0.45)',
              strokeWidth: 1.5,
            },
            markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#818cf8' },
          });
        });
      } else if (graphLayout === 'tree') {
        // HIERARCHICAL TREE MODE: Left-to-Right Branch Tree
        const rootY = Math.max(80, (filteredPackages.length * 110) / 2 - 40);

        nList.push({
          id: rootNodeId,
          type: 'rootProjectNode',
          position: { x: 40, y: rootY },
          data: {
            repo: atlas?.repo || 'Workspace',
            isMonorepo: atlas?.is_monorepo,
            workspacesCount: atlas?.workspaces?.length || 0,
            totalPackages: atlas?.packages?.length || filteredPackages.length,
            totalFiles: atlas?.total_files || 0,
            totalLOC: atlas?.total_loc || 0,
          },
        });

        const PKG_COLS = filteredPackages.length > 8 ? 2 : 1;
        const PKG_COL_WIDTH = 290;
        const PKG_ROW_HEIGHT = 115;

        filteredPackages.forEach((pkg, pIdx) => {
          const pCol = pIdx % PKG_COLS;
          const pRow = Math.floor(pIdx / PKG_COLS);
          const pkgNodeId = `pkg-${pIdx}-${pkg.path}`;

          nList.push({
            id: pkgNodeId,
            type: 'packageNode',
            position: { x: 380 + pCol * PKG_COL_WIDTH, y: 40 + pRow * PKG_ROW_HEIGHT },
            data: { pkg, onSelect: handleSelectPkg },
          });

          eList.push({
            id: `edge-${rootNodeId}-${pkgNodeId}`,
            source: rootNodeId,
            target: pkgNodeId,
            style: { stroke: 'rgba(99, 102, 241, 0.45)', strokeWidth: 1.5 },
            markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#818cf8' },
          });
        });
      } else {
        // GRID MODU: Clean Matrix
        const COLS = 3;
        const COL_WIDTH = 300;
        const ROW_HEIGHT = 140;

        filteredPackages.forEach((pkg, pIdx) => {
          const col = pIdx % COLS;
          const row = Math.floor(pIdx / COLS);
          const pkgNodeId = `pkg-${pIdx}-${pkg.path}`;

          nList.push({
            id: pkgNodeId,
            type: 'packageNode',
            position: { x: 50 + col * COL_WIDTH, y: 50 + row * ROW_HEIGHT },
            data: { pkg, onSelect: handleSelectPkg },
          });
        });
      }
    } else if (focusedScope.level === 'package') {
      const { pkg } = focusedScope;
      const pkgNodeId = `pkg-focus-${pkg.path}`;

      const MAX_CANVAS_FILES = 14;
      const sortedFiles = [...pkg.files].sort((a, b) => {
        if (a.is_fragile !== b.is_fragile) return b.is_fragile ? 1 : -1;
        return b.total_loc - a.total_loc;
      });
      const visibleFiles = sortedFiles.slice(0, MAX_CANVAS_FILES);
      const remainingFiles = sortedFiles.length - visibleFiles.length;

      if (graphLayout === 'radial') {
        const CENTER_X = 400;
        const CENTER_Y = 300;
        const total = Math.max(1, visibleFiles.length + (remainingFiles > 0 ? 1 : 0));
        const R = Math.max(260, total * 30);

        nList.push({
          id: pkgNodeId,
          type: 'packageNode',
          position: { x: CENTER_X - 120, y: CENTER_Y - 40 },
          data: { pkg, onSelect: handleSelectPkg },
        });

        visibleFiles.forEach((file, fIdx) => {
          const theta = (2 * Math.PI * fIdx) / total - Math.PI / 2;
          const fx = CENTER_X + R * Math.cos(theta) - 110;
          const fy = CENTER_Y + R * Math.sin(theta) - 35;
          const fileNodeId = `file-focus-${fIdx}-${file.path}`;

          nList.push({
            id: fileNodeId,
            type: 'fileNode',
            position: { x: fx, y: fy },
            data: { file, onSelect: (f: AtlasFile) => handleSelectFile(f, pkg) },
          });

          eList.push({
            id: `edge-${pkgNodeId}-${fileNodeId}`,
            source: pkgNodeId,
            target: fileNodeId,
            style: { stroke: 'rgba(99, 102, 241, 0.5)', strokeWidth: 1.5 },
            markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#818cf8' },
          });
        });

        if (remainingFiles > 0) {
          const theta = (2 * Math.PI * visibleFiles.length) / total - Math.PI / 2;
          const moreNodeId = `more-files-${pkg.path}`;
          nList.push({
            id: moreNodeId,
            type: 'moreItemsNode',
            position: { x: CENTER_X + R * Math.cos(theta) - 100, y: CENTER_Y + R * Math.sin(theta) - 30 },
            data: { count: remainingFiles, label: 'files', onSwitchToTree: () => setViewMode('tree') },
          });
          eList.push({
            id: `edge-${pkgNodeId}-${moreNodeId}`,
            source: pkgNodeId,
            target: moreNodeId,
            style: { stroke: 'rgba(148, 163, 184, 0.3)', strokeDasharray: '4 4' },
          });
        }
      } else {
        // Tree / Linear mode for package
        nList.push({
          id: pkgNodeId,
          type: 'packageNode',
          position: { x: 50, y: 160 },
          data: { pkg, onSelect: handleSelectPkg },
        });

        const FILE_COLS = 2;
        const FILE_COL_WIDTH = 280;
        const FILE_ROW_HEIGHT = 110;

        visibleFiles.forEach((file, fIdx) => {
          const fileNodeId = `file-focus-${fIdx}-${file.path}`;
          const fCol = fIdx % FILE_COLS;
          const fRow = Math.floor(fIdx / FILE_COLS);

          nList.push({
            id: fileNodeId,
            type: 'fileNode',
            position: { x: 380 + fCol * FILE_COL_WIDTH, y: 50 + fRow * FILE_ROW_HEIGHT },
            data: { file, onSelect: (f: AtlasFile) => handleSelectFile(f, pkg) },
          });

          eList.push({
            id: `edge-${pkgNodeId}-${fileNodeId}`,
            source: pkgNodeId,
            target: fileNodeId,
            style: { stroke: 'rgba(99, 102, 241, 0.5)', strokeWidth: 1.5 },
            markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#818cf8' },
          });
        });

        if (remainingFiles > 0) {
          const fIdx = visibleFiles.length;
          const fCol = fIdx % FILE_COLS;
          const fRow = Math.floor(fIdx / FILE_COLS);
          const moreNodeId = `more-files-${pkg.path}`;

          nList.push({
            id: moreNodeId,
            type: 'moreItemsNode',
            position: { x: 380 + fCol * FILE_COL_WIDTH, y: 50 + fRow * FILE_ROW_HEIGHT },
            data: { count: remainingFiles, label: 'files', onSwitchToTree: () => setViewMode('tree') },
          });

          eList.push({
            id: `edge-${pkgNodeId}-${moreNodeId}`,
            source: pkgNodeId,
            target: moreNodeId,
            style: { stroke: 'rgba(148, 163, 184, 0.3)', strokeDasharray: '4 4' },
          });
        }
      }
    } else if (focusedScope.level === 'file') {
      const { pkg, file } = focusedScope;
      const fileNodeId = `file-focus-${file.path}`;

      const MAX_CANVAS_SYMBOLS = 14;
      const sortedSymbols = [...file.symbols].sort((a, b) => {
        if (a.status !== b.status) return a.status === 'MODIFIED' ? -1 : 1;
        if (a.edit_count !== b.edit_count) return b.edit_count - a.edit_count;
        return b.lines_of_code - a.lines_of_code;
      });
      const visibleSymbols = sortedSymbols.slice(0, MAX_CANVAS_SYMBOLS);
      const remainingSymbols = sortedSymbols.length - visibleSymbols.length;

      if (graphLayout === 'radial') {
        const CENTER_X = 380;
        const CENTER_Y = 280;
        const total = Math.max(1, visibleSymbols.length + (remainingSymbols > 0 ? 1 : 0));
        const R = Math.max(220, total * 24);

        nList.push({
          id: fileNodeId,
          type: 'fileNode',
          position: { x: CENTER_X - 110, y: CENTER_Y - 35 },
          data: { file, onSelect: (f: AtlasFile) => handleSelectFile(f, pkg) },
        });

        visibleSymbols.forEach((sym, sIdx) => {
          const theta = (2 * Math.PI * sIdx) / total - Math.PI / 2;
          const sx = CENTER_X + R * Math.cos(theta) - 100;
          const sy = CENTER_Y + R * Math.sin(theta) - 25;
          const symNodeId = `sym-focus-${sIdx}-${sym.node_signature}`;

          nList.push({
            id: symNodeId,
            type: 'symbolNode',
            position: { x: sx, y: sy },
            data: { symbol: sym, file, onSelect: handleSelectSymbol },
          });

          eList.push({
            id: `edge-${fileNodeId}-${symNodeId}`,
            source: fileNodeId,
            target: symNodeId,
            animated: sym.status === 'MODIFIED',
            style: {
              stroke: sym.status === 'MODIFIED' ? '#f59e0b' : 'rgba(148, 163, 184, 0.3)',
              strokeWidth: 1.5,
            },
            markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#64748b' },
          });
        });

        if (remainingSymbols > 0) {
          const theta = (2 * Math.PI * visibleSymbols.length) / total - Math.PI / 2;
          const moreNodeId = `more-syms-${file.path}`;
          nList.push({
            id: moreNodeId,
            type: 'moreItemsNode',
            position: { x: CENTER_X + R * Math.cos(theta) - 90, y: CENTER_Y + R * Math.sin(theta) - 25 },
            data: { count: remainingSymbols, label: 'symbols', onSwitchToTree: () => setViewMode('tree') },
          });
          eList.push({
            id: `edge-${fileNodeId}-${moreNodeId}`,
            source: fileNodeId,
            target: moreNodeId,
            style: { stroke: 'rgba(148, 163, 184, 0.3)', strokeDasharray: '4 4' },
          });
        }
      } else {
        // Tree mode for symbols
        nList.push({
          id: fileNodeId,
          type: 'fileNode',
          position: { x: 50, y: 160 },
          data: { file, onSelect: (f: AtlasFile) => handleSelectFile(f, pkg) },
        });

        const SYM_COLS = 2;
        const SYM_COL_WIDTH = 260;
        const SYM_ROW_HEIGHT = 65;

        visibleSymbols.forEach((sym, sIdx) => {
          const symNodeId = `sym-focus-${sIdx}-${sym.node_signature}`;
          const sCol = sIdx % SYM_COLS;
          const sRow = Math.floor(sIdx / SYM_COLS);

          nList.push({
            id: symNodeId,
            type: 'symbolNode',
            position: { x: 380 + sCol * SYM_COL_WIDTH, y: 50 + sRow * SYM_ROW_HEIGHT },
            data: { symbol: sym, file, onSelect: handleSelectSymbol },
          });

          eList.push({
            id: `edge-${fileNodeId}-${symNodeId}`,
            source: fileNodeId,
            target: symNodeId,
            animated: sym.status === 'MODIFIED',
            style: {
              stroke: sym.status === 'MODIFIED' ? '#f59e0b' : 'rgba(148, 163, 184, 0.3)',
              strokeWidth: 1.5,
            },
            markerEnd: { type: MarkerType.ArrowClosed, width: 10, height: 10, color: '#64748b' },
          });
        });

        if (remainingSymbols > 0) {
          const sIdx = visibleSymbols.length;
          const sCol = sIdx % SYM_COLS;
          const sRow = Math.floor(sIdx / SYM_COLS);
          const moreNodeId = `more-syms-${file.path}`;

          nList.push({
            id: moreNodeId,
            type: 'moreItemsNode',
            position: { x: 380 + sCol * SYM_COL_WIDTH, y: 50 + sRow * SYM_ROW_HEIGHT },
            data: { count: remainingSymbols, label: 'symbols', onSwitchToTree: () => setViewMode('tree') },
          });

          eList.push({
            id: `edge-${fileNodeId}-${moreNodeId}`,
            source: fileNodeId,
            target: moreNodeId,
            style: { stroke: 'rgba(148, 163, 184, 0.3)', strokeDasharray: '4 4' },
          });
        }
      }
    }

    return { nodes: nList, edges: eList };
  }, [filteredPackages, focusedScope, graphLayout]);

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

          {/* Monorepo Workspace Filter */}
          {atlas?.is_monorepo && (
            <select
              value={selectedWorkspace}
              onChange={(e) => setSelectedWorkspace(e.target.value)}
              className="text-xs bg-slate-900 border border-purple-500/30 rounded-lg px-2.5 py-1.5 text-purple-300 font-mono focus:outline-none focus:border-purple-400"
            >
              <option value="all">All Workspaces ({atlas.workspaces?.length || 0})</option>
              {atlas.workspaces?.map((ws) => (
                <option key={ws} value={ws}>
                  📦 {ws}
                </option>
              ))}
            </select>
          )}

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
                  <Panel position="top-left" className="bg-slate-900/90 backdrop-blur-md px-2.5 py-1.5 rounded-lg border border-white/10 text-xs shadow-lg flex items-center gap-2">
                    {/* Drill-down Breadcrumb */}
                    <div className="flex items-center gap-1">
                      <button
                        type="button"
                        onClick={() => setFocusedScope({ level: 'all' })}
                        className={`flex items-center gap-1 px-2 py-0.5 rounded transition-all ${
                          focusedScope.level === 'all'
                            ? 'bg-indigo-500/20 text-indigo-300 font-semibold'
                            : 'text-slate-400 hover:text-white'
                        }`}
                      >
                        <Boxes className="h-3.5 w-3.5 text-indigo-400" />
                        All Packages
                      </button>
                      {focusedScope.level !== 'all' && (
                        <>
                          <ChevronRight className="h-3 w-3 text-slate-600" />
                          <button
                            type="button"
                            onClick={() => setFocusedScope({ level: 'package', pkg: focusedScope.pkg })}
                            className={`flex items-center gap-1 px-2 py-0.5 rounded transition-all ${
                              focusedScope.level === 'package'
                                ? 'bg-indigo-500/20 text-indigo-300 font-semibold'
                                : 'text-slate-400 hover:text-white'
                            }`}
                          >
                            <Folder className="h-3.5 w-3.5 text-indigo-400" />
                            {focusedScope.pkg.name}
                          </button>
                        </>
                      )}
                      {focusedScope.level === 'file' && (
                        <>
                          <ChevronRight className="h-3 w-3 text-slate-600" />
                          <span className="flex items-center gap-1 px-2 py-0.5 rounded bg-cyan-500/20 text-cyan-300 font-semibold">
                            <FileCode className="h-3.5 w-3.5 text-cyan-400" />
                            {focusedScope.file.name}
                          </span>
                        </>
                      )}
                    </div>

                    <div className="h-3.5 w-px bg-white/10" />

                    {/* Layout Switcher (Radial / Tree / Grid) */}
                    <div className="flex items-center bg-slate-950/80 border border-white/10 rounded-md p-0.5 text-[11px]">
                      <button
                        type="button"
                        onClick={() => setGraphLayout('radial')}
                        className={`flex items-center gap-1 px-2 py-0.5 rounded transition-all ${
                          graphLayout === 'radial' ? 'bg-accent text-white font-medium shadow-sm' : 'text-slate-400 hover:text-white'
                        }`}
                        title="Radial / Orbit (Orbital Graph Layout)"
                      >
                        <Circle className="h-3 w-3" />
                        Orbit
                      </button>
                      <button
                        type="button"
                        onClick={() => setGraphLayout('tree')}
                        className={`flex items-center gap-1 px-2 py-0.5 rounded transition-all ${
                          graphLayout === 'tree' ? 'bg-accent text-white font-medium shadow-sm' : 'text-slate-400 hover:text-white'
                        }`}
                        title="Hierarchical Tree Layout"
                      >
                        <GitFork className="h-3 w-3" />
                        Tree
                      </button>
                      <button
                        type="button"
                        onClick={() => setGraphLayout('grid')}
                        className={`flex items-center gap-1 px-2 py-0.5 rounded transition-all ${
                          graphLayout === 'grid' ? 'bg-accent text-white font-medium shadow-sm' : 'text-slate-400 hover:text-white'
                        }`}
                        title="Grid Matrix Layout"
                      >
                        <LayoutGrid className="h-3 w-3" />
                        Grid
                      </button>
                    </div>
                  </Panel>

                  <Panel position="top-right" className="bg-slate-900/80 backdrop-blur-md px-3 py-1.5 rounded-lg border border-white/10 text-[11px] text-slate-400 flex items-center gap-3">
                    <span className="flex items-center gap-1"><span className="h-2 w-2 rounded-full bg-indigo-500" /> Package</span>
                    <span className="flex items-center gap-1"><span className="h-2 w-2 rounded-full bg-cyan-500" /> File</span>
                    <span className="flex items-center gap-1"><span className="h-2 w-2 rounded-full bg-purple-500" /> Symbol</span>
                  </Panel>
                </ReactFlow>
              )}
            </div>
          ) : (
            /* Interactive Hierarchical Tree Explorer View */
            <div className="panel max-h-[650px] overflow-y-auto space-y-3">
              {/* Tree Quick Actions & Active Breadcrumb */}
              <div className="flex flex-wrap items-center justify-between gap-2 pb-2 border-b border-white/5 text-xs">
                <div className="flex items-center gap-1.5 font-mono text-[11px] text-slate-400 truncate max-w-[65%]">
                  <span className="text-slate-500">Path:</span>
                  <span className="text-indigo-400 font-semibold">{atlas?.repo || 'root'}</span>
                  {selectedItem?.pkg && (
                    <>
                      <span className="text-slate-600">/</span>
                      <span className="text-indigo-300">{selectedItem.pkg.name}</span>
                    </>
                  )}
                  {selectedItem?.file && (
                    <>
                      <span className="text-slate-600">/</span>
                      <span className="text-cyan-300 font-medium">{selectedItem.file.name}</span>
                    </>
                  )}
                  {selectedItem?.symbol && (
                    <>
                      <span className="text-slate-600">/</span>
                      <span className="text-purple-300 font-bold">{selectedItem.symbol.name}</span>
                    </>
                  )}
                </div>

                <div className="flex items-center gap-1.5">
                  <button
                    type="button"
                    onClick={expandAll}
                    className="px-2 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-[11px] text-slate-300 border border-white/5 transition-colors"
                  >
                    Expand All
                  </button>
                  <button
                    type="button"
                    onClick={collapseAll}
                    className="px-2 py-0.5 rounded bg-slate-800 hover:bg-slate-700 text-[11px] text-slate-300 border border-white/5 transition-colors"
                  >
                    Collapse All
                  </button>
                </div>
              </div>

              {filteredPackages.map((pkg) => {
                const isFolderOpen = expandedFolders.has(pkg.path);
                const isPkgSelected = selectedItem?.type === 'package' && selectedItem.pkg?.path === pkg.path;

                return (
                  <div
                    key={pkg.path}
                    className={`border rounded-xl bg-slate-900/50 overflow-hidden transition-all ${
                      isPkgSelected ? 'border-indigo-500/80 shadow-md shadow-indigo-500/10' : 'border-white/5'
                    }`}
                  >
                    <div
                      onClick={() => {
                        setSelectedItem({ type: 'package', pkg });
                        toggleFolder(pkg.path);
                      }}
                      className={`px-3 py-2 flex items-center justify-between cursor-pointer transition-colors ${
                        isPkgSelected ? 'bg-indigo-500/15' : 'bg-white/5 hover:bg-white/10'
                      }`}
                    >
                      <div className="flex items-center gap-2">
                        <span onClick={(e) => toggleFolder(pkg.path, e)} className="p-0.5 hover:bg-white/10 rounded">
                          {isFolderOpen ? (
                            <ChevronDown className="h-3.5 w-3.5 text-slate-400" />
                          ) : (
                            <ChevronRight className="h-3.5 w-3.5 text-slate-400" />
                          )}
                        </span>
                        {isFolderOpen ? (
                          <FolderOpen className="h-4 w-4 text-indigo-400" />
                        ) : (
                          <Folder className="h-4 w-4 text-indigo-400" />
                        )}
                        <span className="font-semibold text-xs text-white">{pkg.name}</span>
                        <span className="text-[11px] text-slate-500 font-mono">({pkg.path})</span>
                      </div>
                      <div className="flex items-center gap-2 text-xs font-mono text-slate-400">
                        <span>{pkg.files.length} files</span>
                        <span>·</span>
                        <span>{pkg.total_loc.toLocaleString()} LOC</span>
                      </div>
                    </div>

                    {isFolderOpen && (
                      <div className="p-2 space-y-2">
                        {pkg.files.map((file) => {
                          const isFileOpen = expandedFiles.has(file.path);
                          const isFileSelected = selectedItem?.type === 'file' && selectedItem.file?.path === file.path;

                          return (
                            <div
                              key={file.path}
                              className={`pl-2 border-l-2 space-y-2 transition-all rounded-r-lg ${
                                isFileSelected
                                  ? 'border-cyan-400 bg-cyan-500/10'
                                  : 'border-slate-800 hover:border-slate-700'
                              }`}
                            >
                              <div
                                onClick={() => {
                                  setSelectedItem({ type: 'file', file });
                                  toggleFile(file.path);
                                }}
                                className="flex items-center justify-between py-1 px-1.5 rounded cursor-pointer hover:text-cyan-300"
                              >
                                <div className="flex items-center gap-2">
                                  <span onClick={(e) => toggleFile(file.path, e)} className="p-0.5 hover:bg-white/10 rounded">
                                    {isFileOpen ? (
                                      <ChevronDown className="h-3 w-3 text-slate-400" />
                                    ) : (
                                      <ChevronRight className="h-3 w-3 text-slate-400" />
                                    )}
                                  </span>
                                  <FileCode className="h-3.5 w-3.5 text-cyan-400" />
                                  <span className={`text-xs font-medium ${isFileSelected ? 'text-cyan-300 font-bold' : 'text-slate-200'}`}>
                                    {file.name}
                                  </span>
                                  <span className="text-[10px] text-slate-500 uppercase font-mono">{file.language}</span>
                                </div>
                                <div className="flex items-center gap-2">
                                  <span className="text-[10px] font-mono text-slate-400">{file.symbols.length} symbols</span>
                                  <span className="text-[11px] font-mono text-emerald-400">{file.health_score}% health</span>
                                </div>
                              </div>

                              {isFileOpen && (
                                <div className="pl-6 pb-2 grid grid-cols-1 sm:grid-cols-2 md:grid-cols-3 gap-1.5">
                                  {file.symbols.map((sym) => {
                                    const isSymSelected =
                                      selectedItem?.type === 'symbol' &&
                                      selectedItem.symbol?.node_signature === sym.node_signature;

                                    return (
                                      <div
                                        key={sym.node_signature}
                                        onClick={() => setSelectedItem({ type: 'symbol', symbol: sym, file })}
                                        className={`px-2 py-1 rounded text-xs cursor-pointer flex items-center justify-between transition-all ${
                                          isSymSelected
                                            ? 'bg-purple-950/80 border border-purple-400 shadow-sm shadow-purple-500/20 text-white'
                                            : 'bg-slate-950/60 border border-white/5 hover:border-purple-500/40 text-slate-300'
                                        }`}
                                      >
                                        <div className="flex items-center gap-1.5 truncate">
                                          {symbolKindIcon(sym.kind)}
                                          <span className="font-mono truncate" title={sym.node_signature}>
                                            {sym.name}
                                          </span>
                                        </div>
                                        <span className="text-[10px] font-mono text-slate-500 shrink-0">{sym.lines_of_code}L</span>
                                      </div>
                                    );
                                  })}
                                </div>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    )}
                  </div>
                );
              })}
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
                      <div className="text-slate-400 mb-1.5 flex items-center justify-between text-xs">
                        <span>Latest Code Change Diff ({ev.action})</span>
                      </div>
                      <RichDiffViewer
                        diff={ev.diff_snippet}
                        filePath={ev.file_path}
                        signature={ev.node_signature}
                        action={ev.action}
                        startLine={ev.start_line}
                        endLine={ev.end_line}
                        maxHeight="220px"
                      />
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
