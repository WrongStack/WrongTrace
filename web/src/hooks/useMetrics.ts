import { useQuery, useQueryClient } from '@tanstack/react-query';
import type { FileHealth, HealthResponse, MetricsSnapshot, ModelRow, ThrashingRow, EventRecord, AtlasSnapshot } from '../types';

const base = '/api';

// Small fetch wrapper that adds a JSON content type, a reasonable timeout, and AbortSignal support.
// Throws on non-2xx so React Query can retry / surface errors uniformly.
async function jget<T>(url: string, signal?: AbortSignal, init?: RequestInit): Promise<T> {
  const r = await fetch(url, { ...init, signal, headers: { Accept: 'application/json', ...(init?.headers ?? {}) } });
  if (!r.ok) {
    let errMsg = `${r.status} ${r.statusText}`;
    try {
      const errJson = await r.json();
      if (errJson?.error) errMsg = errJson.error;
    } catch {}
    throw new Error(errMsg);
  }
  return (await r.json()) as T;
}

export function useOverview(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<MetricsSnapshot>({
    queryKey: ['overview', projectId || 'active'],
    queryFn: ({ signal }) => jget<MetricsSnapshot>(`${base}/metrics/overview${q}`, signal),
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useThrashing(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<ThrashingRow[]>({
    queryKey: ['thrashing', projectId || 'active'],
    queryFn: ({ signal }) => jget<ThrashingRow[]>(`${base}/metrics/thrashing${q}`, signal),
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useModels(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<ModelRow[]>({
    queryKey: ['models', projectId || 'active'],
    queryFn: ({ signal }) => jget<ModelRow[]>(`${base}/metrics/models${q}`, signal),
    staleTime: 5_000,
    refetchInterval: 20_000,
  });
}

// Cursor-based recent-events fetch: one full page on mount, then only rows
// at-or-after the newest event_time already held. Rows are merged locally,
// deduped by event_id, and capped at the page size, so consumers see the
// same shape the old full poll produced while the daemon ships a handful of
// rows per tick instead of re-serializing all 500.
export function useRecentEvents(projectId?: string | null, limit: number = 500) {
  const queryClient = useQueryClient();
  const pParam = projectId ? `project_id=${encodeURIComponent(projectId)}` : '';
  const lParam = `limit=${limit}`;
  const baseQ = [pParam, lParam].filter(Boolean).join('&');

  return useQuery<EventRecord[]>({
    queryKey: ['recent', projectId || 'active', limit],
    queryFn: async ({ signal }) => {
      const previous = queryClient.getQueryData<EventRecord[]>(['recent', projectId || 'active', limit]);
      let q = baseQ;
      if (previous && previous.length > 0) {
        const cursor = previous.reduce((a, e) => (e.event_time > a ? e.event_time : a), previous[0].event_time);
        q = `${baseQ}&since=${encodeURIComponent(cursor)}`;
      }
      const fresh = await jget<EventRecord[]>(`${base}/metrics/recent?${q}`, signal);
      if (!previous || previous.length === 0) return fresh;
      if (fresh.length === 0) return previous;
      // fresh is newest-first from the server; keep any older rows it did not
      // replace, then cap at the page size.
      const seen = new Set(fresh.map((e) => e.event_id));
      const merged = [...fresh, ...previous.filter((e) => !seen.has(e.event_id))];
      // Both inputs are newest-first, but equal-timestamp rows can flip order
      // between polls and defensive re-sorting keeps the cap deterministic.
      merged.sort((a, b) => b.event_time.localeCompare(a.event_time) || a.event_id.localeCompare(b.event_id));
      return merged.slice(0, limit);
    },
    staleTime: 3_000,
    refetchInterval: 10_000,
  });
}

export function useHealth() {
  return useQuery<HealthResponse>({
    queryKey: ['health'],
    queryFn: ({ signal }) => jget<HealthResponse>(`${base}/health`, signal),
    // The socket path never changes during a daemon's lifetime; the refetch
    // interval only re-syncs the navbar after a daemon restart.
    staleTime: 60_000,
    refetchInterval: 30_000,
  });
}

export function useFileHealth(path: string | null) {
  return useQuery<FileHealth>({
    queryKey: ['file_health', path],
    queryFn: ({ signal }) => jget<FileHealth>(`${base}/file/health?path=${encodeURIComponent(path ?? '')}`, signal),
    enabled: !!path,
    staleTime: 30_000,
  });
}

export function useAtlas(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<AtlasSnapshot>({
    queryKey: ['atlas', projectId || 'active'],
    queryFn: ({ signal }) => jget<AtlasSnapshot>(`${base}/atlas${q}`, signal),
    staleTime: 5_000,
    refetchInterval: 20_000,
  });
}

export function useModelCatalog() {
  return useQuery<import('../types').ModelInfo[]>({
    queryKey: ['model_catalog'],
    queryFn: ({ signal }) => jget<import('../types').ModelInfo[]>(`${base}/models/catalog`, signal),
    staleTime: 60_000,
  });
}

export function useProviderCatalog() {
  return useQuery<import('../types').ProviderInfo[]>({
    queryKey: ['provider_catalog'],
    queryFn: ({ signal }) => jget<import('../types').ProviderInfo[]>(`${base}/models/providers`, signal),
    staleTime: 60_000,
  });
}

export function useProxyRoutes() {
  return useQuery<import('../types').ProxyRoute[]>({
    queryKey: ['proxy_routes'],
    queryFn: ({ signal }) => jget<import('../types').ProxyRoute[]>(`${base}/proxy/routes`, signal),
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useProjects() {
  return useQuery<import('../types').Project[]>({
    queryKey: ['projects'],
    queryFn: ({ signal }) => jget<import('../types').Project[]>(`${base}/projects`, signal),
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useProxyTraffic(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').ProxyTrafficRecord[]>({
    queryKey: ['proxy_traffic', projectId || 'active'],
    queryFn: ({ signal }) => jget<import('../types').ProxyTrafficRecord[]>(`${base}/proxy/traffic${q}`, signal),
    staleTime: 2_000,
    refetchInterval: 6_000,
  });
}

export function useSettings() {
  return useQuery<import('../types').AppSettings>({
    queryKey: ['app_settings'],
    queryFn: ({ signal }) => jget<import('../types').AppSettings>(`${base}/settings`, signal),
    staleTime: 60_000,
  });
}

export function useProfilerTraces(limit: number = 50, projectId?: string | null) {
  const pParam = projectId ? `&project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').RuntimeTrace[]>({
    queryKey: ['profiler_traces', limit, projectId || 'active'],
    queryFn: ({ signal }) => jget<import('../types').RuntimeTrace[]>(`${base}/profiler/traces?limit=${limit}${pParam}`, signal),
    staleTime: 3_000,
    refetchInterval: 10_000,
  });
}

export function useProfilerHotspots(limit: number = 25) {
  return useQuery<import('../types').ProfilerHotspot[]>({
    queryKey: ['profiler_hotspots', limit],
    queryFn: ({ signal }) => jget<import('../types').ProfilerHotspot[]>(`${base}/profiler/hotspots?limit=${limit}`, signal),
    refetchInterval: 10_000,
  });
}

export function useProfilerOverview() {
  return useQuery<import('../types').ProfilerOverview>({
    queryKey: ['profiler_overview'],
    queryFn: ({ signal }) => jget<import('../types').ProfilerOverview>(`${base}/profiler/overview`, signal),
    refetchInterval: 10_000,
  });
}

export function useAtlasStatus(projectId?: string | null) {
  const q = projectId ? `?project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').IndexProgress>({
    queryKey: ['atlas_status', projectId || 'active'],
    queryFn: ({ signal }) => jget<import('../types').IndexProgress>(`${base}/atlas/status${q}`, signal),
    staleTime: 2_000,
    refetchInterval: 6_000,
  });
}

export function useRecentReads(limit: number = 50, projectId?: string | null) {
  const pParam = projectId ? `&project_id=${encodeURIComponent(projectId)}` : '';
  return useQuery<import('../types').FileReadRecord[]>({
    queryKey: ['recent_reads', limit, projectId || 'active'],
    queryFn: ({ signal }) => jget<import('../types').FileReadRecord[]>(`${base}/reads/recent?limit=${limit}${pParam}`, signal),
    staleTime: 3_000,
    refetchInterval: 10_000,
  });
}

export function useFileReadStats(path: string | null) {
  return useQuery<import('../types').FileReadStats>({
    queryKey: ['file_read_stats', path],
    queryFn: ({ signal }) => jget<import('../types').FileReadStats>(`${base}/files/reads?path=${encodeURIComponent(path ?? '')}`, signal),
    enabled: !!path,
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useFileReadHeatmap(path: string | null) {
  return useQuery<import('../types').LineReadHeatmap[]>({
    queryKey: ['file_read_heatmap', path],
    queryFn: ({ signal }) => jget<import('../types').LineReadHeatmap[]>(`${base}/files/heatmap?path=${encodeURIComponent(path ?? '')}`, signal),
    enabled: !!path,
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useRecentFileEvents(filePath: string | null, limit: number = 50) {
  return useQuery<EventRecord[]>({
    queryKey: ['recent_file_events', filePath, limit],
    queryFn: ({ signal }) => jget<EventRecord[]>(`${base}/metrics/recent?file_path=${encodeURIComponent(filePath ?? '')}&limit=${limit}`, signal),
    enabled: !!filePath,
    staleTime: 3_000,
    refetchInterval: 10_000,
  });
}

export function useSymbolHistory(filePath: string | null, signature: string | null, limit: number = 100) {
  const pParam = filePath ? `path=${encodeURIComponent(filePath)}` : '';
  const sParam = signature ? `signature=${encodeURIComponent(signature)}` : '';
  const queryStr = [pParam, sParam, `limit=${limit}`].filter(Boolean).join('&');

  return useQuery<import('../types').SymbolHistoryRecord[]>({
    queryKey: ['symbol_history', filePath, signature, limit],
    queryFn: ({ signal }) => jget<import('../types').SymbolHistoryRecord[]>(`${base}/symbol/history?${queryStr}`, signal),
    enabled: !!(filePath || signature),
    staleTime: 3_000,
    refetchInterval: 10_000,
  });
}

export function useFileModelActivity(filePath: string | null) {
  return useQuery<import('../types').ModelActivitySummary[]>({
    queryKey: ['file_model_activity', filePath],
    queryFn: ({ signal }) => jget<import('../types').ModelActivitySummary[]>(`${base}/files/activity?path=${encodeURIComponent(filePath ?? '')}`, signal),
    enabled: !!filePath,
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useModelFriction(limit: number = 200) {
  return useQuery<import('../types').InterAgentFrictionReport>({
    queryKey: ['model_friction', limit],
    queryFn: ({ signal }) => jget<import('../types').InterAgentFrictionReport>(`${base}/metrics/friction?limit=${limit}`, signal),
    staleTime: 5_000,
    refetchInterval: 15_000,
  });
}

export function useIPCTraffic(enabled: boolean = true) {
  return useQuery<import('../types').IPCTrafficRecord[]>({
    queryKey: ['ipc_traffic'],
    queryFn: ({ signal }) => jget<import('../types').IPCTrafficRecord[]>(`${base}/ipc/traffic`, signal),
    enabled,
    staleTime: 2_000,
    refetchInterval: 5_000,
  });
}



