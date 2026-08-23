/**
 * AutoFix — LinkRecord (L2 shared state)
 * Sourced from autofix-polyglot types/typescript/link-record.ts
 * Source of truth: schemas/link-record.schema.json
 */

export type LinkStatus = "PENDING" | "HEALED" | "DEAD" | "HEALTHY";

export interface LinkRecord {
  status: LinkStatus;
  original_url: string;
  resolved_url?: string;
  discovered_at?: string; // ISO-8601
  healed_at?: string; // ISO-8601
  reason?: string;
}

/** Canonical KV key for an absolute URL (must match Go types.KeyFor). */
export function urlKey(url: string): string {
  if (typeof TextEncoder !== "undefined") {
    const bytes = new TextEncoder().encode(url);
    let binary = "";
    for (let i = 0; i < bytes.length; i++) {
      binary += String.fromCharCode(bytes[i]!);
    }
    return btoa(binary);
  }
  return btoa(unescape(encodeURIComponent(url)));
}
