// Tests for the wrapper that keeps upstream text out of a response body.
import { afterEach, describe, expect, it, vi } from "vitest";
import { serverFault } from "./errors";

describe("serverFault", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("returns a sentence that carries none of the fault text", () => {
    // This is the whole point: the account read returned this string to the
    // browser verbatim, naming a table to anyone who could provoke it.
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    const message = serverFault(
      "account read",
      new Error('relation "accounts" does not exist'),
    );
    expect(message).toBe("Something went wrong on our side. Please try again.");
    expect(message).not.toContain("accounts");
    expect(message).not.toContain("relation");
    expect(logged).toHaveBeenCalledTimes(1);
  });

  it("logs the whole database error rather than one field of it", () => {
    // A database error is a plain object, so reading it as an Error would drop
    // the details, the hint and the code that make a log worth having.
    const logged = vi.spyOn(console, "error").mockImplementation(() => {});
    const fault = {
      message: "duplicate key value violates unique constraint",
      details: "Key (token)=(abc) already exists.",
      code: "23505",
    };
    serverFault("link token insert", fault);
    expect(logged).toHaveBeenCalledWith("[link token insert]", fault);
  });
});
