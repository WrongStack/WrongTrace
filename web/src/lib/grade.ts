// Quality-tier grading for the Model Intelligence Matrix.
//
// Extracted from ModelIntelligenceMatrix.tsx so the grading rule is a pure,
// testable function (same pattern as lib/patch.ts). The ROI column in that
// matrix renders "—" (no data) when total_survived_nodes == 0, because the Go
// db.ModelComparison only computes cost_per_surviving_node when that count is
// > 0 and otherwise leaves it 0. So a model with no proven ROI arrives here
// with costPerNode == 0. This module owns the badge that must stay consistent
// with that column.
export type GradeBadge = { label: string; color: string };

export function getGradeBadge(
  survivalRate: number,
  costPerNode: number,
  blendedCost: number,
  hasRoiData: boolean,
): GradeBadge {
  // hasRoiData mirrors the row's total_survived_nodes > 0. When it is false the
  // Go side never computed a real cost (it stays 0), and the ROI column on the
  // same row renders "—". Without this gate a model with UNKNOWN ROI trivially
  // satisfied "costPerNode <= blendedCost" (0 <= anything) and was promoted to
  // the top S-TIER badge — a headline signal contradicting its own source value.
  if (hasRoiData && survivalRate >= 85 && (costPerNode <= blendedCost || costPerNode === 0)) {
    return { label: 'S-TIER', color: 'bg-emerald-500/20 text-emerald-300 border-emerald-500/40 shadow-emerald-500/10' };
  }
  if (survivalRate >= 70) {
    return { label: 'A-TIER', color: 'bg-cyan-500/20 text-cyan-300 border-cyan-500/40 shadow-cyan-500/10' };
  }
  if (survivalRate >= 50) {
    return { label: 'B-TIER', color: 'bg-amber-500/20 text-amber-300 border-amber-500/40 shadow-amber-500/10' };
  }
  return { label: 'C-TIER', color: 'bg-rose-500/20 text-rose-300 border-rose-500/40 shadow-rose-500/10' };
}
