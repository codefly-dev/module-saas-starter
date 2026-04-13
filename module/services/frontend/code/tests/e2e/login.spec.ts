import { test, expect } from "@playwright/test";

test.describe("Login flow", () => {
  test("login page loads without sidebar", async ({ page }) => {
    await page.goto("/auth/login");

    // Should show the login card
    await expect(page.getByText("Sign in")).toBeVisible();

    // Should NOT show the sidebar/admin nav
    await expect(page.locator("text=Dashboard")).not.toBeVisible();
    await expect(page.locator("text=Users & Access")).not.toBeVisible();
  });

  test("fixture users are displayed in dev mode", async ({ page }) => {
    await page.goto("/auth/login");

    // Wait for fixture users to load
    await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("Alice Johnson")).toBeVisible();
    await expect(page.getByText("Bob Williams")).toBeVisible();
    await expect(page.getByText("Carol Davis")).toBeVisible();

    // Roles should be visible
    await expect(page.getByText("super_admin")).toBeVisible();
    await expect(page.getByText("member").first()).toBeVisible();
  });

  test("clicking fixture user logs in and redirects to dashboard", async ({ page }) => {
    await page.goto("/auth/login");

    // Wait for fixture users
    await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 10000 });

    // Click Sarah Chen (super_admin)
    await page.getByText("Sarah Chen").click();

    // Should redirect to dashboard
    await page.waitForURL("/", { timeout: 15000 });

    // Should see the welcome page
    await expect(page.getByText("Welcome back")).toBeVisible();
  });

  test("super_admin sees admin navigation", async ({ page }) => {
    await page.goto("/auth/login");
    await expect(page.getByText("Sarah Chen")).toBeVisible({ timeout: 10000 });
    await page.getByText("Sarah Chen").click();
    await page.waitForURL("/", { timeout: 15000 });

    // Admin nav sections should be visible for super_admin
    await expect(page.getByText("Users & Access")).toBeVisible();
    await expect(page.getByText("Platform")).toBeVisible();
  });

  test("unauthenticated user redirected to login", async ({ page }) => {
    // Try to access a dashboard page directly
    await page.goto("/users");

    // Should redirect to login
    await page.waitForURL(/\/auth\/login/, { timeout: 10000 });
    await expect(page.getByText("Sign in")).toBeVisible();
  });
});
