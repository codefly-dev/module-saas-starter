import { afterEach, expect, it, vi } from "vitest";
import { getToken, setToken } from "../token-store";

afterEach(() => { setToken(null); vi.unstubAllGlobals(); });

it("notifies stable-getter consumers synchronously without distributing credentials", () => {
 const target = new EventTarget();
 vi.stubGlobal("window", target);
 const observations: (string | null)[] = [];
 const listener = vi.fn((event: Event) => {
  expect(event.type).toBe("codefly:auth-changed");
  expect("detail" in event).toBe(false);
  observations.push(getToken());
 });
 target.addEventListener("codefly:auth-changed", listener);
 setToken("test-viewer-a");
 expect(observations).toEqual(["test-viewer-a"]);
 setToken("test-viewer-a");
 expect(listener).toHaveBeenCalledTimes(1);
 setToken("test-viewer-b");
 setToken(null);
 expect(observations).toEqual(["test-viewer-a", "test-viewer-b", null]);
 target.removeEventListener("codefly:auth-changed", listener);
});

it("can update tokens outside a browser", () => {
 vi.stubGlobal("window", undefined);
 setToken("test-server-token");
 expect(getToken()).toBe("test-server-token");
});
