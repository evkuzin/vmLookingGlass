// Response shapes served by the vmLookingGlass API.

export type Labels = Record<string, string>;

export type Target = {
  discoveredLabels: Labels;
  labels: Labels;
  scrapePool: string;
  scrapeUrl: string;
  lastError: string;
  lastScrape: string;
  lastScrapeDuration: number;
  lastSamplesScraped: number;
  health: string;

  // Added by the aggregator: which agents scrape this target.
  agent: string;
  agents: string[];
  multiScraped: boolean;
  // Present only when the agents disagree about this target's health.
  disputed?: boolean;
  healthByAgent?: Record<string, string>;
};

export type SearchResult = {
  targets: Target[];
  total: number;
  limit: number;
  offset: number;
  truncated: boolean;
  takenAt: string;
};

export type Agent = {
  name: string;
  url: string;
  error?: string;
  lastPoll: string;
  lastSuccess: string;
  durationMs: number;
  intervalMs: number;
  activeTargets: number;
  droppedTargets: number;
  upTargets: number;
  downTargets: number;
  healthy: boolean;
  stale: boolean;
};

export type AgentsResult = {
  agents: Agent[];
  activeCount: number;
  droppedCount: number;
  upCount: number;
  downCount: number;
  takenAt: string;
};

export type ScrapePoolsResult = {
  scrapePools: string[];
};
