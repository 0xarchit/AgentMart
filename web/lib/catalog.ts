// Loads the public catalog for the storefront without requiring a user session.
export type Product = {
  id: string;
  name: string;
  category: string;
  price_paise: number;
  stock: number;
  trust_score: number;
};

export async function loadPublicProducts(fetcher: typeof fetch = fetch): Promise<Product[]> {
  const baseURL = process.env.SUPABASE_URL;
  const publishableKey = process.env.SUPABASE_PUBLISHABLE_KEY;
  if (!baseURL || !publishableKey) {
    throw new Error("Supabase public catalog configuration is missing");
  }

  const response = await fetcher(`${baseURL}/rest/v1/products?select=id,name,category,price_paise,stock,trust_score&order=name.asc`, {
    headers: {
      apikey: publishableKey,
      Authorization: `Bearer ${publishableKey}`,
    },
    next: { revalidate: 30 },
  });
  if (!response.ok) {
    throw new Error(`Catalog request failed with status ${response.status}`);
  }
  return response.json() as Promise<Product[]>;
}
