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
  start_line?: number;
  end_line?: number;
  diff_snippet?: string;
  added_lines?: number;
  deleted_lines?: number;
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
  socket_path: string;
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
  StartLine?: number;
  EndLine?: number;
  DiffSnippet?: string;
  AddedLines?: number;
  DeletedLines?: number;
  OccurredAt: string;
}

export interface AtlasSymbol {
  node_signature: string;
  name: string;
  kind: NodeKind | string;
  start_line: number;
  end_line: number;
  lines_of_code: number;
  status: 'ACTIVE' | 'MODIFIED' | 'ADDED' | 'DELETED' | string;
  edit_count: number;
  last_action?: string;
  last_model?: string;
  last_event_time?: string;
  ast_content_hash: string;
}

export interface AtlasFile {
  path: string;
  name: string;
  language: string;
  health_score: number;
  is_fragile: boolean;
  recent_thrashing_count: number;
  total_loc: number;
  symbols: AtlasSymbol[];
}

export interface AtlasPackage {
  path: string;
  name: string;
  files: AtlasFile[];
  total_loc: number;
  is_fragile: boolean;
}

export interface AtlasSnapshot {
  repo: string;
  generated_at: string;
  packages: AtlasPackage[];
  total_files: number;
  total_loc: number;
  total_nodes: number;
}

export interface ModelInfo {
  id: string;
  name: string;
  provider: string;
  input_price_per_m: number;
  output_price_per_m: number;
  cache_read_price_per_m: number;
  context_window: number;
  description: string;
  is_custom?: boolean;
}

export interface CalculateCostResponse {
  model: string;
  prompt_tokens: number;
  completion_tokens: number;
  total_cost_usd: number;
}
