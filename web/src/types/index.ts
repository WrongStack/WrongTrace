// Shared TypeScript types mirroring the Go API contracts.

export type NodeKind = 'function' | 'method' | 'class' | 'struct' | 'arrow_function';
export type Action = 'ADDED' | 'MODIFIED' | 'DELETED';

export interface Overview {
  TotalRuns: number;
  TotalEvents: number;
  TotalCost: number;
  UniqueModels: number;
}

export interface ThrashingRow {
  file_path: string;
  signature: string;
  edit_count: number;
  first_event: string;
  last_event: string;
  window_hours: number;
}

export interface ModelRow {
  model: string;
  total_nodes: number;
  active_nodes: number;
  survival_rate_pct: number;
  avg_longevity_days: number;
  total_cost_usd: number;
  total_survived_nodes: number;
  cost_per_surviving_node: number;
  run_count: number;
}

export interface EventRecord {
  event_id: string;
  run_id: string | null;
  repo_name: string;
  file_path: string;
  node_signature: string;
  node_type: NodeKind;
  action: Action;
  ast_content_hash: string | null;
  lines_of_code: number | null;
  event_time: string;
}

export interface ActiveRun {
  run_id: string;
  agent_name: string;
  model_name: string;
  task_id: string;
  started_at: string;
  last_seen: string;
}

export interface MetricsSnapshot {
  repo: string;
  generated_at: string;
  overview: Overview;
  thrashing: ThrashingRow[];
  models: ModelRow[];
  recent_events: EventRecord[];
  active_runs: ActiveRun[];
}

export interface HealthResponse {
  status: string;
  repo: string;
  timestamp: string;
  ws_clients: number;
}

export interface FileHealth {
  file_path: string;
  health_score: number;
  is_fragile: boolean;
  recent_thrashing_count: number;
  warning: string;
}

export type WSMessage =
  | { type: 'hello'; at: string; repo: string }
  | { type: 'code_event'; event_id: string; payload: WsCodeEvent; at: string }
  | { type: 'run_reported'; payload: ActiveRun; at: string }
  | { type: 'metrics_refresh'; payload: MetricsSnapshot; at: string };

export interface WsCodeEvent {
  RunID: string;
  RepoName: string;
  FilePath: string;
  Signature: string;
  NodeType: NodeKind;
  Action: Action;
  BodyHash: string;
  LOC: number;
  OccurredAt: string;
}
