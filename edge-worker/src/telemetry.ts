/**
 * Edge observability helpers (polyglot OBSERVABILITY.md).
 * Cloudflare Workers: structured JSON logs + W3C trace context propagation.
 * Full OTLP SDK is not used here (isolate size / runtime); healer owns OTLP + Prometheus.
 */

export type LogLevel = "debug" | "info" | "warn" | "error";

export interface LogFields {
  event: string;
  level?: LogLevel;
  layer?: string;
  url?: string;
  url_key?: string;
  status?: string;
  reason?: string;
  resolved_url?: string;
  duration_ms?: number;
  attempt?: number;
  circuit?: string;
  circuit_name?: string;
  msg_id?: string;
  count?: number;
  error?: string;
  trace_id?: string;
  [key: string]: unknown;
}

/** Structured log line matching polyglot contract. */
export function logEvent(fields: LogFields): void {
  const level = fields.level ?? "info";
  const line = {
    ts: new Date().toISOString(),
    level,
    component: "edge",
    layer: fields.layer ?? "L5",
    ...fields,
  };
  const msg = JSON.stringify(line);
  switch (level) {
    case "error":
      console.error(msg);
      break;
    case "warn":
      console.warn(msg);
      break;
    case "debug":
      console.debug(msg);
      break;
    default:
      console.log(msg);
  }
}

/** Create a W3C traceparent for outbound healer calls (sampled). */
export function newTraceparent(): { traceparent: string; traceId: string } {
  const traceId = randomHex(16);
  const spanId = randomHex(8);
  return {
    traceparent: `00-${traceId}-${spanId}-01`,
    traceId,
  };
}

function randomHex(bytes: number): string {
  const arr = new Uint8Array(bytes);
  crypto.getRandomValues(arr);
  return [...arr].map((b) => b.toString(16).padStart(2, "0")).join("");
}
