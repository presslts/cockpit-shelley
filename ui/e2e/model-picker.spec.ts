import { test, expect } from "@playwright/test";
import { createConversationViaAPIWithDetails } from "./helpers";

// The unified model + effort picker (ChatStatusContent -> ModelPicker.vue) is
// built on PrimeVue <Select>. It renders on the new-conversation screen. Here
// we exercise the PrimeVue-specific open/select behavior, the inline
// reasoning-effort pill row, the pinned "Manage models…" footer action, and
// persistence of the chosen model + effort to localStorage.
test.describe("Model picker (PrimeVue)", () => {
  test("embedded history remains readable with an empty picker and can refresh and send using the existing model", async ({ page, request }) => {
    const conversation = await createConversationViaAPIWithDetails(request, "hello");
    await page.addInitScript(() => {
      let init: unknown;
      Object.defineProperty(window, "__SHELLEY_INIT__", {
        configurable: true,
        get: () => init,
        set: (value) => { init = { ...value, presslts_embedded: true, models: [], default_model: "" }; },
      });
    });
    await page.route("**/api/models", route => route.fulfill({ json: [] }));
    await page.goto(`/c/${conversation.slug}`);
    await expect(page.locator(".messages-container")).toContainText("hello");
    await page.route("**/api/models/refresh", route => route.fulfill({ json: [{
      id: "predictable", display_name: "Refreshed integration model", ready: true,
      is_default: true, supports_images: false, supports_reasoning: false,
    }] }));
    const picker = page.locator(".model-picker.p-select");
    await expect(picker).toBeVisible();
    await picker.click();
    const panel = page.locator(".model-picker-panel");
    await panel.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(panel).toContainText("Refreshed integration model");
    await panel.locator(".p-select-option").filter({ hasText: "Refreshed integration model" }).click();
    await expect(panel).toBeHidden();
    const input = page.getByTestId("message-input");
    await input.fill("hello after refreshing the models");
    const sent = page.waitForResponse(response => response.url().endsWith(`/api/conversation/${conversation.conversationId}/chat`) && response.request().method() === "POST");
    await page.getByTestId("send-button").click();
    expect((await sent).status()).toBe(202);
    await expect(page.locator(".messages-container")).toContainText("hello after refreshing the models");
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(page.locator(".messages-container")).toContainText("hello");
  });
  test("embedded picker refreshes integration models without opening credential settings", async ({ page }) => {
    await page.addInitScript(() => {
      let init: unknown;
      Object.defineProperty(window, "__SHELLEY_INIT__", {
        configurable: true,
        get: () => init,
        set: (value) => { init = { ...value, presslts_embedded: true }; },
      });
    });
    let refreshes = 0;
    await page.route("**/api/models/refresh", async (route) => {
      expect(route.request().method()).toBe("POST");
      refreshes++;
      await route.fulfill({ json: [{
        id: "predictable", display_name: "Refreshed integration model", ready: true,
        is_default: true, supports_images: false, supports_reasoning: false,
      }] });
    });
    await page.goto("/new");
    const picker = page.locator(".model-picker.p-select");
    await expect(picker).toBeVisible();
    await picker.click();
    const panel = page.locator(".model-picker-panel");
    await expect(panel.getByRole("button", { name: "Manage models…" })).toHaveCount(0);
    await panel.getByRole("button", { name: "Refresh", exact: true }).click();
    await expect(panel).toContainText("Refreshed integration model");
    expect(refreshes).toBe(1);
    await expect(page.getByRole("dialog")).toHaveCount(0);
  });

  test("opens, lists models, selecting one persists, footer opens manage modal", async ({
    page,
  }) => {
    test.setTimeout(60000);

    await page.goto("/new");
    await page.waitForLoadState("domcontentloaded");

    const picker = page.locator(".model-picker.p-select");
    await expect(picker).toBeVisible({ timeout: 10000 });

    // Open the overlay.
    await picker.click();
    const panel = page.locator(".model-picker-panel");
    await expect(panel).toBeVisible();

    // At least one model is offered and the footer actions are present.
    const options = panel.locator(".p-select-option");
    expect(await options.count()).toBeGreaterThanOrEqual(1);
    const manageBtn = panel.getByRole("button", { name: "Manage models…" });
    await expect(manageBtn).toBeVisible();
    await expect(panel.getByRole("button", { name: "Refresh" })).toBeVisible();

    // In a single-source install, no source sub-labels are rendered.
    await expect(panel.locator(".model-picker-option-source")).toHaveCount(0);

    // Pick the first model -> its label shows in the trigger and the raw model
    // id (not the pretty label) persists to localStorage.
    const firstName = (await options
      .first()
      .locator(".model-picker-option-name")
      .textContent())!.trim();
    await options.first().click();
    await expect(panel).toBeHidden();
    await expect(picker.locator(".model-picker-value-name")).toHaveText(firstName);
    expect(await page.evaluate(() => localStorage.getItem("shelley_selected_model"))).toBe(
      "predictable",
    );

    // The footer action opens the manage-models modal.
    await picker.click();
    await expect(panel).toBeVisible();
    await panel.getByRole("button", { name: "Manage models…" }).click();
    await expect(page.getByRole("dialog")).toBeVisible();
  });

  test("keeps model and directory inline when they fit, then wraps when needed", async ({
    page,
  }) => {
    // Pin the directory so layout doesn't depend on the length of the
    // server's checkout path (which varies across CI agents and wraps the
    // Dir chip onto its own line when long).
    await page.addInitScript(() => localStorage.setItem("shelley_selected_cwd", "/tmp/e2e-dir"));
    await page.setViewportSize({ width: 412, height: 915 });
    await page.goto("/new");

    const fieldTops = () =>
      page.evaluate(() => ({
        model: document.querySelector(".status-field-model")!.getBoundingClientRect().top,
        cwd: document.querySelector(".status-field-cwd")!.getBoundingClientRect().top,
      }));

    let tops = await fieldTops();
    expect(Math.abs(tops.model - tops.cwd)).toBeLessThan(2);

    await page.setViewportSize({ width: 320, height: 700 });
    tops = await fieldTops();
    expect(Math.abs(tops.model - tops.cwd)).toBeGreaterThan(2);
  });

  test("effort pills select a level, persist it, and keep the popover open", async ({ page }) => {
    test.setTimeout(60000);

    await page.goto("/new");
    await page.waitForLoadState("domcontentloaded");

    // Reset persisted level so assertions are deterministic across workers.
    await page.evaluate(() => localStorage.removeItem("shelley.thinkingLevel.v2"));
    await page.reload();
    await page.waitForLoadState("domcontentloaded");

    const picker = page.locator(".model-picker.p-select");
    await expect(picker).toBeVisible({ timeout: 10000 });
    await picker.click();
    const panel = page.locator(".model-picker-panel");
    await expect(panel).toBeVisible();

    // Models without explicit capability metadata use the standard levels
    // through xhigh; rare max support must be advertised by the model.
    const pills = panel.locator(".model-picker-effort-pill");
    expect(await pills.count()).toBeGreaterThanOrEqual(6);
    await expect(pills.filter({ hasText: /^max$/ })).toHaveCount(0);

    // Pick "high" -> persists, popover stays open, trigger shows the suffix.
    await pills.filter({ hasText: /^high$/ }).click();
    await expect(panel).toBeVisible();
    expect(await page.evaluate(() => localStorage.getItem("shelley.thinkingLevel.v2"))).toBe(
      "high",
    );
    await expect(pills.filter({ hasText: /^high$/ })).toHaveAttribute("aria-checked", "true");

    // Close the popover; the trigger reflects the effort and keeps it after reload.
    await page.keyboard.press("Escape");
    await expect(panel).toBeHidden();
    await expect(picker.locator(".model-picker-value-effort")).toHaveText("· high");
    await page.reload();
    await expect(picker.locator(".model-picker-value-effort")).toHaveText("· high");
  });
});
