// Tests for the operator access check. Its only job is to fail closed, so every
// way the read can go wrong is asserted to produce a customer.
import { beforeEach, describe, expect, it, vi } from "vitest";

const getUser = vi.fn();
const maybeSingle = vi.fn();

vi.mock("./supabase/server", () => ({
  createClient: async () => ({
    auth: { getUser },
    from: () => ({
      select: () => ({
        eq: () => ({ maybeSingle }),
      }),
    }),
  }),
}));

const { currentIdentity } = await import("./roles");

// signedIn puts a user behind the session.
function signedIn() {
  getUser.mockResolvedValue({
    data: { user: { id: "user-1", email: "person@example.com" } },
  });
}

describe("currentIdentity", () => {
  beforeEach(() => {
    getUser.mockReset();
    maybeSingle.mockReset();
  });

  it("returns nothing for a visitor who is not signed in", async () => {
    getUser.mockResolvedValue({ data: { user: null } });
    expect(await currentIdentity()).toBeNull();
  });

  it("reads an operator account as an operator", async () => {
    signedIn();
    maybeSingle.mockResolvedValue({
      data: { account_type: "admin" },
      error: null,
    });

    const identity = await currentIdentity();
    expect(identity?.role).toBe("admin");
    expect(identity?.email).toBe("person@example.com");
  });

  it("reads a customer account as a customer", async () => {
    signedIn();
    maybeSingle.mockResolvedValue({
      data: { account_type: "customer" },
      error: null,
    });
    expect((await currentIdentity())?.role).toBe("customer");
  });

  it("treats a refused read as a customer", async () => {
    signedIn();
    maybeSingle.mockResolvedValue({
      data: null,
      error: { message: "permission denied" },
    });
    expect((await currentIdentity())?.role).toBe("customer");
  });

  it("treats a missing account row as a customer", async () => {
    signedIn();
    maybeSingle.mockResolvedValue({ data: null, error: null });
    expect((await currentIdentity())?.role).toBe("customer");
  });

  it("treats anything other than the exact operator value as a customer", async () => {
    // Case, whitespace and near misses must not open the operator view. Only the
    // value the database constraint allows counts.
    for (const value of ["Admin", "ADMIN", " admin", "administrator", "", null]) {
      signedIn();
      maybeSingle.mockResolvedValue({
        data: { account_type: value },
        error: null,
      });
      expect((await currentIdentity())?.role).toBe("customer");
    }
  });
});
