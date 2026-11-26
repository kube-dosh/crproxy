import { describe, expect, it } from "vitest";
import { formatBytes, percent, shortDigest } from "./utils";

describe("formatBytes", () => {
  it("handles invalid and zero values", () => {
    expect(formatBytes(0)).toBe("0 B");
    expect(formatBytes(-1)).toBe("0 B");
    expect(formatBytes(Number.NaN)).toBe("0 B");
  });
  it("formats IEC units", () => {
    expect(formatBytes(1023)).toBe("1023 B");
    expect(formatBytes(1024)).toBe("1.0 KiB");
    expect(formatBytes(10 * 1024)).toBe("10 KiB");
    expect(formatBytes(5 * 1024 ** 3)).toBe("5.0 GiB");
  });
});

describe("percent", () => {
  it("clamps and rejects invalid totals", () => {
    expect(percent(50, 100)).toBe(50);
    expect(percent(500, 100)).toBe(100);
    expect(percent(-1, 100)).toBe(0);
    expect(percent(10, 0)).toBe(0);
  });
});

describe("shortDigest", () => {
  it("preserves short values and abbreviates long digests", () => {
    expect(shortDigest("sha256:1234")).toBe("sha256:1234");
    expect(shortDigest("sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")).toBe("sha256:0123456789ab…abcdef");
  });
});
