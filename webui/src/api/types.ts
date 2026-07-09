export interface BuilderInfo {
  id: string;
  ip: string;
  arch: string;
  status: string;
  drvPath?: string;
  created: string;
  lastUsed: string;
  ageSecs: number;
}

export interface PoolStatus {
  builders: BuilderInfo[];
  pendingRequests: number;
  provisioningCount: Record<string, number>;
}

export interface BuildRow {
  id: number;
  drvPath: string;
  pname: string;
  version: string;
  system: string;
  builderId: string;
  status: number;
  statusText: string;
  errorMsg?: string;
  startTime: number;
  stopTime: number;
  durationSecs: number;
}

export interface BuildList {
  total: number;
  builds: BuildRow[];
}

export interface StoreSummary {
  totalBuilds: number;
  successfulBuilds: number;
  failedBuilds: number;
  avgDurationSecs: number;
  p90DurationSecs: number;
}
