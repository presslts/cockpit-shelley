import type { InitData } from "../types";

function initData(): InitData | undefined {
  return typeof window === "undefined" ? undefined : window.__SHELLEY_INIT__;
}

function configuredBase(): string {
  const base = initData()?.base_url;
  return base ? new URL(base, window.location.href).toString().replace(/\/$/, "") : "";
}

/** Resolve a Shelley-owned absolute path without changing standalone Shelley. */
export function shelleyURL(input: string | URL): string {
  const raw = String(input);
  if (/^(?:[a-z][a-z\d+.-]*:|\/\/)/i.test(raw)) return raw;
  const base = configuredBase();
  return base ? new URL(raw.replace(/^\//, ""), `${base}/`).toString() : raw;
}

export function pluginStorageKey(key: string): string {
  const plugin = initData()?.presslts_plugin_id;
  return plugin ? `${key}:${plugin}` : key;
}

/** Shelley requests use the short-lived Core session when embedded. */
export function shelleyFetch(input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> {
  const url = shelleyURL(typeof input === "string" || input instanceof URL ? input : input.url);
  return fetch(url, {
    ...init,
    credentials: init.credentials ?? (configuredBase() ? "include" : "same-origin"),
  });
}

export function shelleyEventSource(path: string): EventSource {
  return new EventSource(shelleyURL(path), { withCredentials: Boolean(configuredBase()) });
}
