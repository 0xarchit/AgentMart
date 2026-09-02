// Loads the public catalog for the storefront without requiring a user session.
// cost_paise is deliberately not selected: it is the merchant's negotiating
// floor, and this read is public.
export type Product = {
  id: string;
  name: string;
  category: string;
  price_paise: number;
  stock: number;
  trust_score: number;
  warranty_years: number;
  combo_with: string | null;
  combo_discount_pct: number | null;
  // Optional. Null means the storefront draws its own placeholder, so a catalog
  // without photography still reads as a shop rather than as a list.
  image_url: string | null;
};

// The columns the catalog has always had. The image column is newer, so it is
// requested separately and its absence is survivable.
const settledColumns =
  "id,name,category,price_paise,stock,trust_score,warranty_years,combo_with,combo_discount_pct";

export async function loadPublicProducts(
  fetcher: typeof fetch = fetch,
): Promise<Product[]> {
  const baseURL = process.env.SUPABASE_URL;
  const publishableKey = process.env.SUPABASE_PUBLISHABLE_KEY;
  if (!baseURL || !publishableKey) {
    return [];
  }

  const read = async (columns: string): Promise<Product[] | null> => {
    const response = await fetcher(
      `${baseURL}/rest/v1/products?select=${columns}&order=name.asc`,
      {
        headers: {
          apikey: publishableKey,
          Authorization: `Bearer ${publishableKey}`,
        },
        next: { revalidate: 30 },
      },
    );
    if (!response.ok) {
      return null;
    }
    return response.json() as Promise<Product[]>;
  };

  const withImages = await read(`${settledColumns},image_url`);
  if (withImages) {
    return withImages;
  }
  // A shop that has not taken the image migration yet still has stock to show.
  // Blanking the storefront over a column that only decides how a card is
  // illustrated would turn a cosmetic gap into an outage.
  const withoutImages = await read(settledColumns);
  if (!withoutImages) {
    return [];
  }
  return withoutImages.map((product) => ({
    ...product,
    image_url: product.image_url ?? null,
  }));
}
