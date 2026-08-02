// Human-readable formatting for target timings and counts.

export function formatDuration(seconds: number): string {
  if (seconds < 0.001) {
    return "<1ms";
  }
  if (seconds < 1) {
    return `${Math.round(seconds * 1000)}ms`;
  }
  return `${seconds.toFixed(2)}s`;
}

export function formatRelative(timestamp: string): string {
  const then = new Date(timestamp).getTime();
  if (!Number.isFinite(then) || then <= 0) {
    return "never";
  }
  const seconds = (Date.now() - then) / 1000;
  if (seconds < 0) {
    return "just now";
  }
  if (seconds < 60) {
    return `${Math.round(seconds)}s ago`;
  }
  if (seconds < 3600) {
    return `${Math.round(seconds / 60)}m ago`;
  }
  if (seconds < 86400) {
    return `${Math.round(seconds / 3600)}h ago`;
  }
  return `${Math.round(seconds / 86400)}d ago`;
}

export function formatCount(n: number): string {
  return n.toLocaleString();
}
