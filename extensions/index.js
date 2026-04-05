import { accessSync, constants, existsSync, readFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import { fileURLToPath } from "node:url";
import { spawn } from "node:child_process";

import { getAgentDir } from "@mariozechner/pi-coding-agent";
import { Type } from "@sinclair/typebox";

const PACKAGE_ROOT = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const SESSION_STATE_TYPE = "obsh-session-state";
const MAX_OUTPUT_BYTES = 50 * 1024;
const MAX_OUTPUT_LINES = 2000;
const DEFAULT_LIMIT = 20;
const DEFAULT_LOG_LIMIT = 200;
const DEFAULT_SINCE = "30m";
const DEFAULT_STEP = "60s";

const EmptyParams = Type.Object({});

const SearchParams = Type.Object({
  query: Type.String({ description: "Text query to search in the active observability profile" }),
  since: Type.Optional(Type.String({ description: "Lookback window such as 30m" })),
  start: Type.Optional(Type.String({ description: "Absolute start time" })),
  end: Type.Optional(Type.String({ description: "Absolute end time" })),
  limit: Type.Optional(Type.Number({ description: "Maximum hit count" })),
});

const LogsParams = Type.Object({
  query: Type.Optional(Type.String({ description: "Optional free-text filter" })),
  since: Type.Optional(Type.String({ description: "Lookback window such as 30m" })),
  start: Type.Optional(Type.String({ description: "Absolute start time" })),
  end: Type.Optional(Type.String({ description: "Absolute end time" })),
  service: Type.Optional(Type.String({ description: "Service name filter" })),
  operation: Type.Optional(Type.String({ description: "Operation or route filter" })),
  traceId: Type.Optional(Type.String({ description: "Trace id filter" })),
  requestId: Type.Optional(Type.String({ description: "Request id filter" })),
  limit: Type.Optional(Type.Number({ description: "Maximum log record count" })),
});

const TraceParams = Type.Object({
  traceId: Type.String({ description: "Trace id to inspect" }),
});

const SpanParams = Type.Object({
  spanId: Type.String({ description: "Span id to inspect" }),
});

const MetricsParams = Type.Object({
  expr: Type.String({ description: "PromQL expression" }),
  since: Type.Optional(Type.String({ description: "Lookback window such as 30m" })),
  start: Type.Optional(Type.String({ description: "Absolute start time" })),
  end: Type.Optional(Type.String({ description: "Absolute end time" })),
  step: Type.Optional(Type.String({ description: "Range step such as 60s" })),
});

const UseProfileParams = Type.Object({
  name: Type.String({ description: "Configured obsh profile name" }),
});

function isObject(value) {
  return Boolean(value) && typeof value === "object" && !Array.isArray(value);
}

function readJSONFile(path) {
  if (!existsSync(path)) return {};
  try {
    const raw = readFileSync(path, "utf8");
    const parsed = JSON.parse(raw);
    return isObject(parsed) ? parsed : {};
  } catch {
    return {};
  }
}

function mergeNamedMaps(baseMap, overrideMap) {
  const merged = { ...baseMap };
  for (const [name, value] of Object.entries(overrideMap || {})) {
    if (!isObject(value)) continue;
    const prev = isObject(merged[name]) ? merged[name] : {};
    merged[name] = { ...prev, ...value };
    if (isObject(prev.defaults) || isObject(value.defaults)) {
      merged[name].defaults = { ...(prev.defaults || {}), ...(value.defaults || {}) };
    }
    if (isObject(prev.env) || isObject(value.env)) {
      merged[name].env = { ...(prev.env || {}), ...(value.env || {}) };
    }
  }
  return merged;
}

function findNearestProjectSettings(cwd) {
  let current = resolve(cwd);
  while (true) {
    const candidate = join(current, ".pi", "settings.json");
    if (existsSync(candidate)) return candidate;
    const parent = dirname(current);
    if (parent === current) return null;
    current = parent;
  }
}

function extractObshConfig(path) {
  const parsed = readJSONFile(path);
  return isObject(parsed.obsh) ? parsed.obsh : {};
}

function loadConfig(cwd) {
  const globalSettings = join(getAgentDir(), "settings.json");
  const projectSettings = findNearestProjectSettings(cwd);
  const globalConfig = extractObshConfig(globalSettings);
  const projectConfig = projectSettings ? extractObshConfig(projectSettings) : {};

  return {
    enabled: projectConfig.enabled ?? globalConfig.enabled ?? true,
    command: projectConfig.command ?? globalConfig.command,
    defaultProfile: projectConfig.defaultProfile ?? globalConfig.defaultProfile,
    providers: mergeNamedMaps(globalConfig.providers || {}, projectConfig.providers || {}),
    profiles: mergeNamedMaps(globalConfig.profiles || {}, projectConfig.profiles || {}),
  };
}

function loadSessionState(ctx, config) {
  let activeProfile = undefined;
  for (const entry of ctx.sessionManager.getBranch()) {
    if (entry.type !== "custom" || entry.customType !== SESSION_STATE_TYPE || !isObject(entry.data)) continue;
    if (Object.prototype.hasOwnProperty.call(entry.data, "activeProfile")) {
      const value = entry.data.activeProfile;
      activeProfile = typeof value === "string" && value.trim() ? value.trim() : undefined;
    }
  }
  if (!activeProfile && typeof config.defaultProfile === "string" && config.defaultProfile.trim()) {
    activeProfile = config.defaultProfile.trim();
  }
  if (activeProfile && !config.profiles[activeProfile]) {
    activeProfile = undefined;
  }
  return activeProfile;
}

function formatProfiles(config, activeProfile) {
  const names = Object.keys(config.profiles || {}).sort();
  if (names.length === 0) return "No obsh profiles configured.";
  const lines = ["Configured obsh profiles:"];
  for (const name of names) {
    const profile = config.profiles[name] || {};
    const provider = typeof profile.provider === "string" ? profile.provider : "<missing-provider>";
    const desc = typeof profile.description === "string" && profile.description.trim() ? ` - ${profile.description.trim()}` : "";
    const active = name === activeProfile ? " active" : "";
    lines.push(`- ${name} provider=${provider}${active}${desc}`);
  }
  return lines.join("\n");
}

function redactEnv(envMap) {
  const out = {};
  for (const [key, value] of Object.entries(envMap || {})) {
    if (/(token|secret|password|key)/i.test(key)) {
      out[key] = "<redacted>";
      continue;
    }
    out[key] = value;
  }
  return out;
}

function formatStatus(config, activeProfile) {
  const lines = [];
  lines.push(`obsh enabled=${config.enabled !== false}`);
  lines.push(`active_profile=${activeProfile || "<none>"}`);
  const profileCount = Object.keys(config.profiles || {}).length;
  lines.push(`configured_profiles=${profileCount}`);
  if (!activeProfile) return `${lines.join("\n")}\n\n${formatProfiles(config, activeProfile)}`;

  const profile = config.profiles[activeProfile];
  if (!profile) {
    lines.push("warning=active profile missing from configuration");
    return `${lines.join("\n")}\n\n${formatProfiles(config, activeProfile)}`;
  }

  const providerName = typeof profile.provider === "string" ? profile.provider : "";
  const provider = config.providers[providerName] || {};
  lines.push(`provider=${providerName || "<missing-provider>"}`);
  if (provider.type) lines.push(`provider_type=${provider.type}`);
  if (provider.logsUrl) lines.push(`logs_url=${provider.logsUrl}`);
  if (provider.tracesUrl) lines.push(`traces_url=${provider.tracesUrl}`);
  if (provider.metricsUrl) lines.push(`metrics_url=${provider.metricsUrl}`);
  if (provider.victoriaqBin) lines.push(`victoriaq_bin=${provider.victoriaqBin}`);
  const env = redactEnv(provider.env || {});
  if (Object.keys(env).length > 0) {
    lines.push(`env=${JSON.stringify(env)}`);
  }
  return `${lines.join("\n")}\n\n${formatProfiles(config, activeProfile)}`;
}

function validatePositiveInteger(value, fieldName) {
  if (value === undefined || value === null) return undefined;
  if (!Number.isInteger(value) || value <= 0) {
    throw new Error(`${fieldName} must be a positive integer`);
  }
  return value;
}

function findExecutableOnPath(name) {
  const pathValue = process.env.PATH || "";
  const parts = pathValue.split(":").filter(Boolean);
  for (const dir of parts) {
    const candidate = join(dir, name);
    try {
      accessSync(candidate, constants.X_OK);
      return candidate;
    } catch {
      continue;
    }
  }
  return undefined;
}

function resolveCommand(commandConfig) {
  if (Array.isArray(commandConfig) && commandConfig.length > 0 && commandConfig.every((value) => typeof value === "string" && value.trim())) {
    return { argv: commandConfig.map((value) => value.trim()), cwd: process.cwd() };
  }
  if (typeof commandConfig === "string" && commandConfig.trim()) {
    return { argv: [commandConfig.trim()], cwd: process.cwd() };
  }

  const obshPath = findExecutableOnPath("obsh");
  if (obshPath) {
    return { argv: [obshPath], cwd: process.cwd() };
  }

  if (existsSync(join(PACKAGE_ROOT, "go.mod"))) {
    return { argv: ["go", "run", "./cmd/obsh"], cwd: PACKAGE_ROOT };
  }

  throw new Error("No obsh command configured. Set obsh.command in settings.json or install obsh in PATH.");
}

function providerContext(config, activeProfile) {
  if (!activeProfile) {
    throw new Error(
      "No active obsh profile. Call obsh_list_profiles to inspect configured targets, then call obsh_use_profile with one of those names.",
    );
  }
  const profile = config.profiles[activeProfile];
  if (!profile || typeof profile.provider !== "string" || !profile.provider.trim()) {
    throw new Error(`Profile ${activeProfile} is missing provider configuration.`);
  }
  const providerName = profile.provider.trim();
  const provider = config.providers[providerName];
  if (!provider) {
    throw new Error(`Provider ${providerName} referenced by profile ${activeProfile} is not configured.`);
  }
  const providerDefaults = isObject(provider.defaults) ? provider.defaults : {};
  const profileDefaults = isObject(profile.defaults) ? profile.defaults : {};
  return {
    profileName: activeProfile,
    profile,
    providerName,
    provider,
    defaults: { ...providerDefaults, ...profileDefaults },
  };
}

function obshEnv(provider) {
  const env = { ...process.env };
  if (provider.logsUrl) env.VICTORIA_LOGS_URL = provider.logsUrl;
  if (provider.tracesUrl) env.VICTORIA_TRACES_URL = provider.tracesUrl;
  if (provider.metricsUrl) env.VICTORIA_METRICS_URL = provider.metricsUrl;
  if (provider.victoriaqBin) env.OBSH_VICTORIAQ_BIN = provider.victoriaqBin;
  for (const [key, value] of Object.entries(provider.env || {})) {
    if (typeof value === "string") env[key] = value;
  }
  return env;
}

function trimOutput(text) {
  const normalized = String(text || "").replace(/\r\n/g, "\n");
  const lines = normalized.split("\n");
  const limitedLines = lines.slice(0, MAX_OUTPUT_LINES);
  let joined = limitedLines.join("\n");
  let truncated = lines.length > MAX_OUTPUT_LINES;
  if (joined.length > MAX_OUTPUT_BYTES) {
    joined = joined.slice(0, MAX_OUTPUT_BYTES);
    truncated = true;
  }
  if (truncated) {
    joined = `${joined}\n\n[output truncated]`;
  }
  return joined.trimEnd();
}

function runCommand(command, args, options = {}) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env,
      stdio: ["ignore", "pipe", "pipe"],
    });

    let stdout = "";
    let stderr = "";

    child.stdout.on("data", (chunk) => {
      stdout += chunk.toString();
    });
    child.stderr.on("data", (chunk) => {
      stderr += chunk.toString();
    });
    child.on("error", (error) => rejectPromise(error));

    if (options.signal) {
      const abortHandler = () => {
        child.kill("SIGTERM");
      };
      if (options.signal.aborted) {
        abortHandler();
      } else {
        options.signal.addEventListener("abort", abortHandler, { once: true });
        child.on("close", () => {
          options.signal.removeEventListener("abort", abortHandler);
        });
      }
    }

    child.on("close", (code, signal) => {
      if (signal) {
        rejectPromise(new Error(`obsh command terminated by signal ${signal}`));
        return;
      }
      if (code !== 0) {
        const message = trimOutput(stderr || stdout) || `obsh command failed with exit code ${code}`;
        rejectPromise(new Error(message));
        return;
      }
      resolvePromise({ stdout: trimOutput(stdout), stderr: trimOutput(stderr) });
    });
  });
}

async function runObsh(config, activeProfile, subcommand, extraArgs, signal) {
  const active = providerContext(config, activeProfile);
  const command = resolveCommand(config.command);
  const providerType = typeof active.provider.type === "string" && active.provider.type.trim() ? active.provider.type.trim() : "victoria";
  const args = command.argv.slice(1).concat([subcommand, "--provider", providerType], extraArgs);
  const result = await runCommand(command.argv[0], args, {
    cwd: command.cwd,
    env: obshEnv(active.provider),
    signal,
  });
  return {
    profileName: active.profileName,
    providerName: active.providerName,
    providerType,
    command: [command.argv[0], ...args].join(" "),
    output: result.stdout,
  };
}

function maybePush(args, flag, value) {
  if (value === undefined || value === null || value === "") return;
  args.push(flag, String(value));
}

function firstDefined(...values) {
  for (const value of values) {
    if (value !== undefined && value !== null && value !== "") return value;
  }
  return undefined;
}

function refreshState(ctx) {
  const config = loadConfig(ctx.cwd);
  const activeProfile = loadSessionState(ctx, config);
  return { config, activeProfile };
}

function persistProfile(pi, name) {
  pi.appendEntry(SESSION_STATE_TYPE, { activeProfile: name || null });
}

function routingPrompt(profileName, providerName, provider) {
  const label = typeof provider.description === "string" && provider.description.trim() ? provider.description.trim() : providerName;
  return `\n\n## obsh\nActive obsh profile: ${profileName}.\nConnected provider: ${label}.\nUse obsh_search, obsh_logs, obsh_trace, obsh_span, and obsh_metrics for observability work in this session.\nIf you need to inspect available targets or switch context, use obsh_status, obsh_list_profiles, and obsh_use_profile.`;
}

export default function obshExtension(pi) {
  let config = { enabled: true, providers: {}, profiles: {} };
  let activeProfile;

  function syncState(ctx) {
    const next = refreshState(ctx);
    config = next.config;
    activeProfile = next.activeProfile;
  }

  function ensureEnabled() {
    if (config.enabled === false) {
      throw new Error("obsh extension is disabled by configuration.");
    }
  }

  async function activateProfile(name) {
    const trimmed = String(name || "").trim();
    if (!trimmed) throw new Error("Profile name is required.");
    if (!config.profiles[trimmed]) {
      const available = Object.keys(config.profiles || {}).sort();
      const suffix = available.length > 0 ? ` Available profiles: ${available.join(", ")}.` : " No profiles are configured.";
      throw new Error(`Unknown obsh profile ${trimmed}.${suffix}`);
    }
    activeProfile = trimmed;
    persistProfile(pi, trimmed);
    return trimmed;
  }

  function clearProfile() {
    activeProfile = undefined;
    persistProfile(pi, null);
  }

  pi.on("session_start", async (_event, ctx) => {
    syncState(ctx);
  });

  pi.on("session_tree", async (_event, ctx) => {
    syncState(ctx);
  });

  pi.on("session_fork", async (_event, ctx) => {
    syncState(ctx);
  });

  pi.on("before_agent_start", async (event, ctx) => {
    syncState(ctx);
    if (!activeProfile) return undefined;
    const profile = config.profiles[activeProfile];
    if (!profile || !profile.routingHints) return undefined;
    const provider = config.providers[profile.provider] || {};
    return {
      systemPrompt: event.systemPrompt + routingPrompt(activeProfile, profile.provider, provider),
    };
  });

  pi.registerCommand("obsh-status", {
    description: "Show active obsh profile and configured targets",
    handler: async (_args, ctx) => {
      syncState(ctx);
      ctx.ui.notify(formatStatus(config, activeProfile), "info");
    },
  });

  pi.registerCommand("obsh-profile", {
    description: "Set or clear active obsh profile: /obsh-profile <name|clear>",
    handler: async (args, ctx) => {
      syncState(ctx);
      const value = String(args || "").trim();
      if (!value) {
        ctx.ui.notify(formatProfiles(config, activeProfile), "info");
        return;
      }
      if (value === "clear") {
        clearProfile();
        ctx.ui.notify("Cleared active obsh profile", "info");
        return;
      }
      const name = await activateProfile(value);
      ctx.ui.notify(`Active obsh profile: ${name}`, "info");
    },
  });

  pi.registerTool({
    name: "obsh_list_profiles",
    label: "obsh Profiles",
    description: "List configured obsh observability profiles",
    promptSnippet: "List configured obsh observability profiles before selecting a target",
    promptGuidelines: [
      "Use this tool when the task mentions a known observability target such as MinT, Victoria, request ids, traces, or production telemetry and no obsh profile is active yet.",
    ],
    parameters: EmptyParams,
    async execute(_toolCallId, _params, _signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const text = formatProfiles(config, activeProfile);
      return {
        content: [{ type: "text", text }],
        details: { activeProfile, profiles: Object.keys(config.profiles || {}).sort() },
      };
    },
  });

  pi.registerTool({
    name: "obsh_status",
    label: "obsh Status",
    description: "Show active obsh profile, provider, and configured targets",
    promptSnippet: "Inspect the active obsh profile and provider state",
    promptGuidelines: ["Use this tool to verify whether an obsh profile is active before querying logs, traces, or metrics."],
    parameters: EmptyParams,
    async execute(_toolCallId, _params, _signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const text = formatStatus(config, activeProfile);
      return {
        content: [{ type: "text", text }],
        details: { activeProfile, profiles: Object.keys(config.profiles || {}).sort() },
      };
    },
  });

  pi.registerTool({
    name: "obsh_use_profile",
    label: "Use obsh Profile",
    description: "Activate one configured obsh profile for this session",
    promptSnippet: "Activate an obsh profile before observability queries",
    promptGuidelines: [
      "If the prompt implies a known observability target such as MinT, call this tool before obsh_search, obsh_logs, obsh_trace, obsh_span, or obsh_metrics.",
    ],
    parameters: UseProfileParams,
    async execute(_toolCallId, params, _signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const name = await activateProfile(params.name);
      const profile = config.profiles[name] || {};
      return {
        content: [{ type: "text", text: `Active obsh profile: ${name}\nprovider=${profile.provider || "<missing-provider>"}` }],
        details: { activeProfile: name, profile },
      };
    },
  });

  pi.registerTool({
    name: "obsh_clear_profile",
    label: "Clear obsh Profile",
    description: "Clear the active obsh profile for this session",
    promptSnippet: "Clear the current obsh profile when observability context should not persist",
    parameters: EmptyParams,
    async execute(_toolCallId, _params, _signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      clearProfile();
      return {
        content: [{ type: "text", text: "Active obsh profile cleared." }],
        details: { activeProfile: null },
      };
    },
  });

  pi.registerTool({
    name: "obsh_search",
    label: "obsh Search",
    description: "Search logs, traces, and metrics in the active obsh profile",
    promptSnippet: "Search observability data in the active obsh profile",
    promptGuidelines: ["Do not call this tool until an obsh profile is active. Use obsh_list_profiles and obsh_use_profile first when needed."],
    parameters: SearchParams,
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const limit = validatePositiveInteger(params.limit, "limit");
      const active = providerContext(config, activeProfile);
      const args = [];
      maybePush(args, "--since", firstDefined(params.since, active.defaults.since, DEFAULT_SINCE));
      maybePush(args, "--start", params.start);
      maybePush(args, "--end", params.end);
      maybePush(args, "--limit", firstDefined(limit, active.defaults.limit, DEFAULT_LIMIT));
      args.push(params.query);
      const result = await runObsh(config, activeProfile, "search", args, signal);
      return {
        content: [{ type: "text", text: result.output }],
        details: result,
      };
    },
  });

  pi.registerTool({
    name: "obsh_logs",
    label: "obsh Logs",
    description: "Read log records from the active obsh profile",
    promptSnippet: "Read log records from the active obsh profile",
    promptGuidelines: ["Do not call this tool until an obsh profile is active. Use obsh_use_profile first when needed."],
    parameters: LogsParams,
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const limit = validatePositiveInteger(params.limit, "limit");
      const active = providerContext(config, activeProfile);
      const args = [];
      maybePush(args, "--since", firstDefined(params.since, active.defaults.since, DEFAULT_SINCE));
      maybePush(args, "--start", params.start);
      maybePush(args, "--end", params.end);
      maybePush(args, "--service", params.service);
      maybePush(args, "--operation", params.operation);
      maybePush(args, "--trace-id", params.traceId);
      maybePush(args, "--request-id", params.requestId);
      maybePush(args, "--limit", firstDefined(limit, active.defaults.logLimit, active.defaults.limit, DEFAULT_LOG_LIMIT));
      if (params.query) args.push(params.query);
      const result = await runObsh(config, activeProfile, "logs", args, signal);
      return {
        content: [{ type: "text", text: result.output }],
        details: result,
      };
    },
  });

  pi.registerTool({
    name: "obsh_trace",
    label: "obsh Trace",
    description: "Inspect one trace tree from the active obsh profile",
    promptSnippet: "Inspect a trace tree in the active obsh profile",
    promptGuidelines: ["Use this after obsh_search or obsh_logs when you already have a trace id."],
    parameters: TraceParams,
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const result = await runObsh(config, activeProfile, "trace", [params.traceId], signal);
      return {
        content: [{ type: "text", text: result.output }],
        details: result,
      };
    },
  });

  pi.registerTool({
    name: "obsh_span",
    label: "obsh Span",
    description: "Inspect one span from the active obsh profile",
    promptSnippet: "Inspect one span in the active obsh profile",
    promptGuidelines: ["Use this when a trace view hides attrs or events and you already have a span id."],
    parameters: SpanParams,
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const result = await runObsh(config, activeProfile, "span", [params.spanId], signal);
      return {
        content: [{ type: "text", text: result.output }],
        details: result,
      };
    },
  });

  pi.registerTool({
    name: "obsh_metrics",
    label: "obsh Metrics",
    description: "Query metrics from the active obsh profile",
    promptSnippet: "Query metrics in the active obsh profile",
    promptGuidelines: ["Use this after activating an obsh profile when you need direct metric confirmation."],
    parameters: MetricsParams,
    async execute(_toolCallId, params, signal, _onUpdate, ctx) {
      syncState(ctx);
      ensureEnabled();
      const active = providerContext(config, activeProfile);
      const args = [];
      maybePush(args, "--since", firstDefined(params.since, active.defaults.since, DEFAULT_SINCE));
      maybePush(args, "--start", params.start);
      maybePush(args, "--end", params.end);
      maybePush(args, "--step", firstDefined(params.step, active.defaults.step, DEFAULT_STEP));
      args.push(params.expr);
      const result = await runObsh(config, activeProfile, "metrics", args, signal);
      return {
        content: [{ type: "text", text: result.output }],
        details: result,
      };
    },
  });
}
