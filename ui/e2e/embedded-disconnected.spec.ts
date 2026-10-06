import { test, expect } from "@playwright/test";
import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

test("embedded empty-model Shelley saves drafts across reload and shows only PressLTS setup", async ({ page, request }) => {
  const directory = mkdtempSync(join(tmpdir(), "shelley-empty-models-"));
  const config = join(directory, "config.json");
  const portFile = join(directory, "port");
  writeFileSync(config, "{}");
  const server = spawn(process.env.SHELLEY_TEST_BINARY || resolve("../bin/shelley"), [
    "-config", config, "-db", join(directory, "test.db"), "-disable-gateway", "-disable-llm-integration",
    "serve", "-port", "0", "-port-file", portFile, "-socket", "none",
  ], { env: { PATH: process.env.PATH, HOME: directory }, stdio: "ignore" });
  try {
    await expect.poll(() => existsSync(portFile)).toBe(true);
    const base = `http://127.0.0.1:${readFileSync(portFile, "utf8").trim()}`;
    await expect.poll(async () => (await request.get(`${base}/api/models`)).json()).toEqual([]);
    await page.addInitScript(() => {
      let init: unknown;
      Object.defineProperty(window, "__SHELLEY_INIT__", {
        configurable: true, get: () => init,
        set: (value) => { init = { ...value, presslts_embedded: true, is_exe_dev: true }; },
      });
    });
    await page.goto(`${base}/new`);
    await expect(page.locator(".no-models-message")).toContainText("PressLTS Settings");
    await expect(page.locator('a[href*="exe.dev/suggest"]')).toHaveCount(0);
    const input = page.getByTestId("message-input");
    const created = page.waitForResponse(response => response.url().endsWith("/api/conversations/draft") && response.request().method() === "POST");
    await input.fill("A draft written without an AI connection");
    const creation = await created;
    expect(creation.status()).toBe(201);
    let inferenceRequests = 0;
    page.on("request", request => {
      if (request.method() === "POST" && /\/(chat|new|continue|retry)$/.test(new URL(request.url()).pathname)) inferenceRequests++;
    });
    await page.getByTestId("send-button").click();
    await expect(page.locator(".status-no-models")).toContainText("PressLTS Settings");
    expect(inferenceRequests).toBe(0);
    await expect(input).toHaveValue("A draft written without an AI connection");
    const draft = await creation.json();
    await expect.poll(() => new URL(page.url()).searchParams.get("chat")).toBe(draft.conversation_id);
    await page.reload();
    await expect(input).toHaveValue("A draft written without an AI connection");
    const saved = page.waitForResponse(response => response.url().endsWith(`/api/conversation/${draft.conversation_id}/draft`) && response.request().method() === "PUT");
    await input.fill("Updated draft survives another reload");
    expect((await saved).status()).toBe(200);
    await page.reload();
    await expect(input).toHaveValue("Updated draft survives another reload");
    const pricing = await request.post(`${base}/api/model-costs`, { data: { models: [] } });
    expect(pricing.status()).toBe(200);
    const renamed = await request.post(`${base}/api/conversation/${draft.conversation_id}/rename`, { data: { slug: "disconnected-draft" } });
    expect(renamed.status()).toBe(200);
    const stored = await request.get(`${base}/api/conversation/${draft.conversation_id}`);
    expect((await stored.json()).conversation.draft).toBe("Updated draft survives another reload");
  } finally {
    server.kill("SIGTERM");
    await new Promise<void>(resolve => server.exitCode !== null ? resolve() : server.once("exit", () => resolve()));
    rmSync(directory, { recursive: true, force: true });
  }
});
