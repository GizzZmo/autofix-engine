/**
 * AutoFix — Discovery contracts (L4 / L5)
 * Sourced from autofix-polyglot types/typescript/discovery.ts
 */

export interface DiscoveryMessage {
  urls: string[];
  discovered_at: string; // ISO-8601
}

export interface DiscoverRequest {
  urls?: string[];
  /** Single-URL convenience field accepted by the healer. */
  url?: string;
}

export interface DiscoverResponse {
  enqueued: number;
}
