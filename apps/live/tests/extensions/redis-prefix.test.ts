import { beforeEach, describe, expect, it, vi } from "vitest";

const fakeClient = {
  duplicate: () => fakeClient,
  on: () => fakeClient,
};

vi.mock("@/redis", () => ({ redisManager: { getClient: () => fakeClient } }));
vi.mock("@/env", () => ({ env: { REDIS_KEY_PREFIX: "plane:" } }));
vi.mock("@plane/logger", () => ({ logger: { info: () => {}, warn: () => {}, error: () => {} } }));

describe("Redis extension key prefix", () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it("namespaces document channels, locks and the admin channel with REDIS_KEY_PREFIX", async () => {
    const { Redis } = await import("@/extensions/redis");
    const ext = new Redis() as unknown as {
      configuration: { prefix: string };
      ADMIN_CHANNEL: string;
      pubKey: (documentName: string) => string;
      lockKey: (documentName: string) => string;
    };

    expect(ext.configuration.prefix).toBe("plane:hocuspocus");
    expect(ext.ADMIN_CHANNEL).toBe("plane:hocuspocus:admin");
    expect(ext.pubKey("page-1")).toBe("plane:hocuspocus:page-1");
    expect(ext.lockKey("page-1")).toMatch(/^plane:hocuspocus:page-1/);
  });
});
