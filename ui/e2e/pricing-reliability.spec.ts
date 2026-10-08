import { test, expect } from "@playwright/test";
import { spawn, execFileSync } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

test("cost popup refreshes rates, labels incomplete totals, and preserves measured context", async ({
  page,
  request,
}) => {
  const directory = mkdtempSync(join(tmpdir(), "shelley-pricing-"));
  const database = join(directory, "test.db");
  const portFile = join(directory, "port");
  const server = spawn(
    resolve("../bin/shelley"),
    [
      "--predictable-only",
      "--db",
      database,
      "serve",
      "--port",
      "0",
      "--port-file",
      portFile,
      "--socket",
      "none",
    ],
    { env: { PATH: process.env.PATH, HOME: directory }, stdio: "ignore" },
  );
  try {
    await expect.poll(() => existsSync(portFile)).toBe(true);
    const base = `http://127.0.0.1:${readFileSync(portFile, "utf8").trim()}`;
    const created = await request.post(`${base}/api/conversations/new`, {
      data: { message: "echo: pricing fixture", model: "predictable", cwd: directory },
    });
    expect(created.status()).toBe(201);
    const { conversation_id: id } = await created.json();
    let slug = "";
    await expect(async () => {
      const body = await (await request.get(`${base}/api/conversation/${id}`)).json();
      expect(body.conversation.agent_working).toBe(false);
      expect(
        body.messages.some(
          (message: { type: string; end_of_turn: boolean }) =>
            message.type === "agent" && message.end_of_turn,
        ),
      ).toBe(true);
      slug = body.conversation.slug;
      expect(slug).toBeTruthy();
    }).toPass({ timeout: 30_000 });
    // Seed deterministic measured usage into this disposable database. Keep
    // real messages and exercise the native REST/SSE and context calculation.
    execFileSync("python3", [
      "-c",
      `
import json, sqlite3, sys
db = sqlite3.connect(sys.argv[1])
conversation = sys.argv[2]
message = db.execute("SELECT message_id FROM messages WHERE conversation_id=? AND type='agent' ORDER BY sequence_id DESC LIMIT 1", (conversation,)).fetchone()[0]
db.execute("UPDATE messages SET usage_data=NULL, other_usage_data=NULL WHERE conversation_id=?", (conversation,))
usage = dict(input_tokens=1000000, cache_read_input_tokens=100000, cache_creation_input_tokens=0, output_tokens=100000, cost_usd=0, model='gpt-6.1-sol', url='https://presslts-test.int.exe.xyz/v1/responses')
other = [dict(usage, purpose='slug', input_tokens=1000000, cache_read_input_tokens=0, output_tokens=0)]
db.execute("UPDATE messages SET usage_data=?, other_usage_data=?, model_name=?, llm_api_url=? WHERE message_id=?", (json.dumps(usage), json.dumps(other), usage['model'], usage['url'], message))
db.commit()
`,
      database,
      id,
    ]);
    const measured = await (await request.get(`${base}/api/conversation/${id}`)).json();
    expect(measured.context_window_size).toBe(1_200_000);

    let inputRate = 2;
    let missing = false;
    let failed = false;
    await page.route("**/api/model-costs", async (route) => {
      if (failed) {
        await route.fulfill({ status: 503, body: "Pricing catalog unavailable" });
        return;
      }
      await route.fulfill({
        json: {
          costs: {
            "gpt-6.1-sol": missing
              ? null
              : { input: inputRate, output: 10, cache_read: 0.1, cache_write: 2.5 },
          },
          source: "exe.dev",
          updated_at: new Date().toISOString(),
        },
      });
    });
    await page.route("**/api/conversation/*/subagent-usage", (route) =>
      route.fulfill({
        json: {
          llm_calls: 2,
          estimated_usd: 0.5,
          reported_usd: 0.25,
          unpriced_reported_usd: 0,
          unpriced_models: [],
          unpriced_calls: 0,
        },
      }),
    );
    await page.goto(`${base}/c/${slug}`);
    const popup = page.locator(".chat-context-popup");
    await expect(page.locator(".context-usage-label")).toContainText("1.2M");
    if (!(await popup.isVisible())) await page.locator(".context-usage-label").click();
    await expect(popup).toContainText("tokens in the latest context");
    await expect(popup).toContainText("Estimates use current exe.dev rates");
    const total = page.getByTestId("token-cost-total");
    // Main: $3.01, indirect: $2, subagents: $0.50 estimate + $0.25 reported.
    await expect(total).toContainText("Estimated total");
    await expect(total).toContainText("$5.76");
    const reopen = async () => {
      await page.keyboard.press("Escape");
      await expect(popup).toBeHidden();
      await page.locator(".context-usage-label").click();
      await expect(popup).toBeVisible();
    };
    inputRate = 4;
    await reopen();
    await expect(total).toContainText("$9.76");
    missing = true;
    await reopen();
    await expect(total).toContainText("Known cost (partial)");
    await expect(total).toContainText("$0.750");
    await expect(popup).toContainText("2 calls have no model pricing");
    failed = true;
    await reopen();
    await expect(popup).toContainText("Pricing lookup failed");
    await expect(total).toContainText("Known cost (partial)");
    failed = false;
    missing = false;
    await reopen();
    await expect(total).toContainText("Estimated total");
    await expect(total).toContainText("$9.76");
    await expect(page.locator(".context-usage-label")).toContainText("1.2M");
  } finally {
    server.kill("SIGTERM");
    await new Promise<void>((resolve) =>
      server.exitCode !== null ? resolve() : server.once("exit", () => resolve()),
    );
    rmSync(directory, { recursive: true, force: true });
  }
});
