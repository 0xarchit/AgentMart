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
};

export async function loadPublicProducts(fetcher: typeof fetch = fetch): Promise<Product[]> {
  const baseURL = process.env.SUPABASE_URL;
  const publishableKey = process.env.SUPABASE_PUBLISHABLE_KEY;
  if (!baseURL || !publishableKey) {
    return [];
  }

  const response = await fetcher(`${baseURL}/rest/v1/products?select=id,name,category,price_paise,stock,trust_score,warranty_years,combo_with,combo_discount_pct&order=name.asc`, {
    headers: {
      apikey: publishableKey,
      Authorization: `Bearer ${publishableKey}`,
    },
    next: { revalidate: 30 },
  });
  if (!response.ok) {
    return [];
  }
  return response.json() as Promise<Product[]>;
}
