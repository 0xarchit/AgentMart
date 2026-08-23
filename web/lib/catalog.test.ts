// Verifies the anonymous storefront catalog request contract.
import { afterEach, describe, expect, it, vi } from "vitest";
import { loadPublicProducts } from "./catalog";

describe("loadPublicProducts", () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it("loads products with the publishable key without a user session", async () => {
    vi.stubEnv("SUPABASE_URL", "https://example.supabase.co");
    vi.stubEnv("SUPABASE_PUBLISHABLE_KEY", "public-key");
    const fetcher = vi.fn<typeof fetch>().mockResolvedValue(new Response(JSON.stringify([{ id: "p1" }]), { status: 200 }));

    const products = await loadPublicProducts(fetcher);

    expect(products).toEqual([{ id: "p1" }]);
    expect(fetcher).toHaveBeenCalledWith(
      "https://example.supabase.co/rest/v1/products?select=id,name,category,price_paise,stock,trust_score&order=name.asc",
      expect.objectContaining({
        headers: {
          apikey: "public-key",
          Authorization: "Bearer public-key",
        },
      }),
    );
  });

  it("returns an empty catalog when public configuration is unavailable", async () => {
    vi.stubEnv("SUPABASE_URL", "");
    vi.stubEnv("SUPABASE_PUBLISHABLE_KEY", "");
    const fetcher = vi.fn<typeof fetch>();

    await expect(loadPublicProducts(fetcher)).resolves.toEqual([]);
    expect(fetcher).not.toHaveBeenCalled();
  });
});
