import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { Navbar } from '../components/Navbar';
import { WrongStackBanner } from '../components/WrongStackBanner';
import { MetricsOverview } from '../components/MetricsOverview';
import { CodeChurnTimeline } from '../components/CodeChurnTimeline';
import { ThrashingHeatmap } from '../components/ThrashingHeatmap';
import { ModelLeaderboard } from '../components/ModelLeaderboard';
import { ModelIntelligenceMatrix } from '../components/ModelIntelligenceMatrix';
import { ModelFrictionMatrix } from '../components/ModelFrictionMatrix';
import { LiveEventFeed } from '../components/LiveEventFeed';
import { ROIAnalysis } from '../components/ROIAnalysis';
import { useHealth, useModels, useOverview, useRecentEvents, useThrashing, useAtlas, useProjects } from '../hooks/useMetrics';
import { useWebSocket } from '../hooks/useWebSocket';
import type { WSMessage } from '../types';

const EMPTY_ARRAY: any[] = [];

const CodeAtlas = lazy(() => import('../components/CodeAtlas').then((m) => ({ default: m.CodeAtlas })));
const DiffInspectorView = lazy(() => import('../components/DiffInspectorView').then((m) => ({ default: m.DiffInspectorView })));
const AgentSessionsView = lazy(() => import('../components/AgentSessionsView').then((m) => ({ default: m.AgentSessionsView })));
const ProfilerTracesView = lazy(() => import('../components/ProfilerTracesView').then((m) => ({ default: m.ProfilerTracesView })));
const ProxyRoutingView = lazy(() => import('../components/ProxyRoutingView').then((m) => ({ default: m.ProxyRoutingView })));
const SettingsView = lazy(() => import('../components/SettingsView').then((m) => ({ default: m.SettingsView })));

function TabFallback() {
	return <div className="py-16 text-center text-xs font-mono text-slate-500">Loading view…</div>;
}

// Dashboard is the single-page surface for WrongTrace. It loads via TanStack
// Query and is incrementally refreshed by a single WebSocket subscription.
// Only time-decaying health/overview snapshots retain a slow safety poll.
export function Dashboard() {
  const queryClient = useQueryClient();
  const [activeTab, setActiveTab] = useState<'dashboard' | 'atlas' | 'diffs' | 'sessions' | 'profiler' | 'gateway' | 'settings'>('dashboard');
  const [selectedProjectId, setSelectedProjectId] = useState<string | null>(null);

  const { data: projects = EMPTY_ARRAY } = useProjects();

  const currentProject = useMemo(() => {
    if (selectedProjectId) {
      return projects.find((p) => p.id === selectedProjectId) || null;
    }
    return projects.find((p) => p.is_active) || projects[0] || null;
  }, [projects, selectedProjectId]);

  const activeProjId = currentProject?.id ?? null;
  const dashboardVisible = activeTab === 'dashboard';
  const recentVisible = dashboardVisible || activeTab === 'atlas' || activeTab === 'diffs' || activeTab === 'sessions';
  const modelsVisible = dashboardVisible || activeTab === 'sessions';
  const recentLimit = activeTab === 'diffs' ? 500 : 250;

  const overview = useOverview(activeProjId);
  const thrashing = useThrashing(activeProjId, dashboardVisible);
  const models = useModels(activeProjId, modelsVisible);
  const recent = useRecentEvents(activeProjId, recentLimit, recentVisible);
  const atlas = useAtlas(activeProjId, activeTab === 'atlas');

	// useAtlas stays mounted while tabs switch, so React Query would otherwise
	// keep a multi-megabyte symbol graph "active" indefinitely and gcTime would
	// never apply. Drop it as soon as the Atlas surface is left; re-entry already
	// performs an explicit fetch through the enabled flag above.
	useEffect(() => {
		if (activeTab !== 'atlas') {
			queryClient.removeQueries({ queryKey: ['atlas'] });
		}
	}, [activeTab, queryClient]);

  const wsTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const pendingWsTypesRef = useRef(new Set<WSMessage['type']>());
	const seenHelloRef = useRef(false);
	// Frames observed inside the current coalescing window, and the window that
	// observation earned. A fixed 250ms window meant a busy agent run fired four
	// full refresh storms per second -- roughly thirty requests a second, each
	// re-rendering the whole dashboard. The window widens under sustained
	// traffic and snaps back as soon as things go quiet.
	const wsBurstRef = useRef(0);
	const missedWhileHiddenRef = useRef(false);

  // Aggregate socket notifications without putting every frame into React
  // state. One burst triggers at most one render-independent cache refresh,
  // and inactive tab queries stay stale without fetching until they are shown.
  const handleSocketMessage = useCallback((message: WSMessage) => {
	if (message.type === 'hello') {
		if (seenHelloRef.current) {
			// A second hello means the socket reconnected. Refresh only mounted
			// queries to close any event gap while the connection was down.
			void queryClient.invalidateQueries({ refetchType: 'active' });
		}
		seenHelloRef.current = true;
		return;
	}
    pendingWsTypesRef.current.add(message.type);
    wsBurstRef.current += 1;

    // A hidden tab must not fetch at all: its queries are invisible, and the
    // browser throttles the timers driving them anyway. Remember that we fell
    // behind and catch up in one pass when the tab comes back.
    if (typeof document !== 'undefined' && document.hidden) {
      missedWhileHiddenRef.current = true;
      return;
    }

    if (wsTimerRef.current) return;

    const coalesceMs = wsBurstRef.current > 20 ? 1000 : wsBurstRef.current > 5 ? 500 : 250;
    wsTimerRef.current = setTimeout(() => {
      wsTimerRef.current = null;
      wsBurstRef.current = 0;
      const pending = pendingWsTypesRef.current;
      pendingWsTypesRef.current = new Set();
      const invalidate = (queryKey: string[], refetchType: 'active' | 'none' = 'active') => {
        void queryClient.invalidateQueries({ queryKey, refetchType });
      };

      if (pending.has('code_event')) {
        invalidate(['recent']);
		invalidate(['overview']);
		invalidate(['thrashing']);
		invalidate(['models']);
		invalidate(['model_friction']);
		invalidate(['recent_file_events']);
		invalidate(['symbol_history']);
		invalidate(['file_model_activity']);
        // The full symbol graph can be several MB. Mark it stale, but let its
		// next tab entry or explicit Refresh button perform the fetch.
        invalidate(['atlas'], 'none');
      }
      if (pending.has('run_reported')) {
        invalidate(['overview']);
		invalidate(['models']);
      }
      if (pending.has('proxy_traffic')) {
        invalidate(['proxy_traffic']);
        invalidate(['overview']);
      }
      if (pending.has('profiler_trace')) {
        invalidate(['profiler_traces']);
        invalidate(['profiler_hotspots']);
        invalidate(['profiler_overview']);
      }
	  if (pending.has('file_read_event')) {
		invalidate(['recent_reads']);
		invalidate(['file_read_stats']);
		invalidate(['file_read_heatmap']);
	  }
	  if (pending.has('index_progress')) {
		invalidate(['atlas_status']);
		invalidate(['atlas'], 'none');
	  }
	  if (pending.has('ipc_traffic')) {
		invalidate(['ipc_traffic']);
	  }
	  if (pending.has('metrics_refresh')) {
		invalidate(['overview']);
		invalidate(['thrashing']);
		invalidate(['models']);
	  }
      if (pending.has('project_switched')) {
        ['projects', 'overview', 'thrashing', 'models', 'recent', 'atlas', 'proxy_traffic', 'profiler_traces'].forEach((key) => invalidate([key]));
      }
    }, coalesceMs);
  }, [queryClient]);

  const ws = useWebSocket(handleSocketMessage);

  // One catch-up refresh when a backgrounded tab is brought forward, instead of
  // every frame that arrived while nobody was looking.
  useEffect(() => {
    const onVisible = () => {
      if (document.hidden || !missedWhileHiddenRef.current) return;
      missedWhileHiddenRef.current = false;
      pendingWsTypesRef.current = new Set();
      wsBurstRef.current = 0;
      void queryClient.invalidateQueries({ refetchType: 'active' });
    };
    document.addEventListener('visibilitychange', onVisible);
    return () => document.removeEventListener('visibilitychange', onVisible);
  }, [queryClient]);

  useEffect(() => {
    return () => {
      if (wsTimerRef.current) {
        clearTimeout(wsTimerRef.current);
        wsTimerRef.current = null;
      }
      pendingWsTypesRef.current.clear();
    };
  }, []);

  // Socket path as REPORTED BY THE DAEMON (/api/health socket_path), so a
  // custom --socket is shown correctly on every platform. Falls back to a
  // platform default only while the first health response is in flight.
  const health = useHealth();
  const socketPath =
    health.data?.socket_path ||
    (typeof navigator !== 'undefined' && /win/i.test(navigator.userAgent || '') ? '\\\\.\\pipe\\wrongtrace' : '/tmp/wrongtrace.sock');

  const activeRunCount = overview.data?.active_runs?.length ?? 0;

  return (
    <div className="min-h-full flex flex-col">
      <Navbar
        repo={overview.data?.repo ?? 'wrongtrace'}
        wsConnected={ws.connected}
        agentCount={activeRunCount}
        socketPath={socketPath}
        activeTab={activeTab}
        onTabChange={setActiveTab}
        selectedProjectId={selectedProjectId}
        onProjectChange={(p) => setSelectedProjectId(p?.id ?? null)}
      />

      <main className="mx-auto max-w-7xl px-4 sm:px-6 py-6 space-y-6 flex-1 w-full">
        {/* WrongStack Ecosystem Flagship Banner */}
        <WrongStackBanner />

        {activeTab === 'dashboard' && (
          <>
            <MetricsOverview
              overview={overview.data?.overview}
              thrashing={thrashing.data ?? EMPTY_ARRAY}
              models={models.data ?? EMPTY_ARRAY}
              loading={overview.isLoading || thrashing.isLoading || models.isLoading}
              currentProject={currentProject}
            />

            <CodeChurnTimeline
              events={recent.data ?? EMPTY_ARRAY}
              loading={recent.isLoading}
            />

            <div className="grid grid-cols-1 xl:grid-cols-2 gap-6">
              <ThrashingHeatmap rows={thrashing.data ?? EMPTY_ARRAY} loading={thrashing.isLoading} />
              <ModelLeaderboard models={models.data ?? EMPTY_ARRAY} loading={models.isLoading} />
            </div>

            {/* Model Telemetry & Code Durability Intelligence Matrix */}
            <ModelIntelligenceMatrix
              models={models.data ?? EMPTY_ARRAY}
              events={recent.data ?? EMPTY_ARRAY}
              loading={models.isLoading}
              projectId={activeProjId}
            />

            {/* Inter-Agent Friction & Cross-Thrashing Matrix */}
            <ModelFrictionMatrix />

            <div className="grid grid-cols-1 xl:grid-cols-3 gap-6">
              <div className="xl:col-span-2">
                <LiveEventFeed events={recent.data ?? EMPTY_ARRAY} loading={recent.isLoading} />
              </div>
              <ROIAnalysis models={models.data ?? EMPTY_ARRAY} />
            </div>
          </>
        )}

        {activeTab === 'atlas' && (
		  <Suspense fallback={<TabFallback />}>
			<CodeAtlas
			  atlas={atlas.data}
			  recentEvents={recent.data}
			  loading={atlas.isLoading}
			  onRefresh={() => atlas.refetch()}
			/>
		  </Suspense>
        )}

        {activeTab === 'diffs' && (
		  <Suspense fallback={<TabFallback />}>
			<DiffInspectorView
			  events={recent.data ?? []}
			  loading={recent.isLoading}
			  currentProject={currentProject}
			/>
		  </Suspense>
        )}

        {activeTab === 'sessions' && (
		  <Suspense fallback={<TabFallback />}>
			<AgentSessionsView
			  activeRuns={overview.data?.active_runs ?? []}
			  models={models.data ?? []}
			  overview={overview.data?.overview}
			  recentEvents={recent.data ?? []}
			  loading={overview.isLoading}
			  currentProject={currentProject}
			/>
		  </Suspense>
        )}

        {activeTab === 'profiler' && (
		  <Suspense fallback={<TabFallback />}>
			<ProfilerTracesView />
		  </Suspense>
        )}

        {activeTab === 'gateway' && (
		  <Suspense fallback={<TabFallback />}>
			<ProxyRoutingView currentProject={currentProject} />
		  </Suspense>
        )}

        {activeTab === 'settings' && (
		  <Suspense fallback={<TabFallback />}>
			<SettingsView />
		  </Suspense>
        )}

        {/* Global Footer with WrongStack Ecosystem Link */}
        <footer className="border-t border-white/5 pt-6 pb-10 text-xs text-slate-500 flex flex-col sm:flex-row items-center justify-between gap-4">
          <div className="flex items-center gap-2">
            <span>Powered by</span>
            <a
              href="https://github.com/wrongstack/wrongstack"
              target="_blank"
              rel="noreferrer"
              className="font-bold text-cyan-400 hover:text-cyan-300 transition-colors"
            >
              WrongStack
            </a>
            <span>· Universal AI Observability & Multi-Agent Engine</span>
          </div>

          <div className="flex items-center gap-4 text-slate-400 font-mono text-[11px]">
            <a href="https://github.com/wrongstack/wrongstack" target="_blank" rel="noreferrer" className="hover:text-white transition-colors">
              WrongStack GitHub ↗
            </a>
            <span className="text-slate-700">|</span>
            <a href="https://github.com/wrongstack/wrongtrace" target="_blank" rel="noreferrer" className="hover:text-white transition-colors">
              WrongTrace Repository
            </a>
            <span className="text-slate-700">|</span>
            <span className="text-slate-500">BUSL-1.1</span>
          </div>
        </footer>
      </main>
    </div>
  );
}
