#!/usr/bin/env node
/** Cross-platform launcher for setup-cloudflare.sh */
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import path from "node:path";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const script = path.join(__dirname, "setup-cloudflare.sh");

const r = spawnSync("bash", [script], { stdio: "inherit" });
process.exit(r.status ?? 1);
