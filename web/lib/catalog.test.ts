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
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(
        new Response(JSON.stringify([{ id: "p1" }]), { status: 200 }),
      );

    const products = await loadPublicProducts(fetcher);

    expect(products).toEqual([{ id: "p1" }]);
    expect(fetcher).toHaveBeenCalledWith(
      "https://example.supabase.co/rest/v1/products?select=id,name,category,price_paise,stock,trust_score,warranty_years,combo_with,combo_discount_pct,image_url&order=name.asc",
      expect.objectContaining({
        headers: {
          apikey: "public-key",
          Authorization: "Bearer public-key",
        },
      }),
    );
  });

  it("shows the stock even when the image column is not there yet", async () => {
    vi.stubEnv("SUPABASE_URL", "https://example.supabase.co");
    vi.stubEnv("SUPABASE_PUBLISHABLE_KEY", "public-key");
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValueOnce(
        new Response("column products.image_url does not exist", {
          status: 400,
        }),
      )
      .mockResolvedValueOnce(
        new Response(JSON.stringify([{ id: "p1", name: "Nova" }]), {
          status: 200,
        }),
      );

    // Blanking the storefront over a column that only decides how a card is
    // illustrated would turn a cosmetic gap into an outage.
    await expect(loadPublicProducts(fetcher)).resolves.toEqual([
      { id: "p1", name: "Nova", image_url: null },
    ]);
    expect(fetcher).toHaveBeenCalledTimes(2);
    expect(String(fetcher.mock.calls[1][0])).not.toContain("image_url");
  });

  it("returns an empty catalog when the catalog cannot be read at all", async () => {
    vi.stubEnv("SUPABASE_URL", "https://example.supabase.co");
    vi.stubEnv("SUPABASE_PUBLISHABLE_KEY", "public-key");
    const fetcher = vi
      .fn<typeof fetch>()
      .mockResolvedValue(new Response("unavailable", { status: 503 }));

    await expect(loadPublicProducts(fetcher)).resolves.toEqual([]);
  });

  it("returns an empty catalog when public configuration is unavailable", async () => {
    vi.stubEnv("SUPABASE_URL", "");
    vi.stubEnv("SUPABASE_PUBLISHABLE_KEY", "");
    const fetcher = vi.fn<typeof fetch>();

    await expect(loadPublicProducts(fetcher)).resolves.toEqual([]);
    expect(fetcher).not.toHaveBeenCalled();
  });
});
